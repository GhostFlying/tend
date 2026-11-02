package main

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestCapacityAdaptsToEffectiveCPUsAndSupportsSequentialRecovery(t *testing.T) {
	for _, tc := range []struct {
		cpus int
		want budget
	}{
		{64, budget{7, 4, 3, 4, false}},
		{32, budget{7, 4, 1, 4, false}},
		{16, budget{4, 2, 1, 4, true}},
		{8, budget{2, 1, 1, 4, true}},
		{2, budget{1, 1, 1, 2, true}},
	} {
		got, err := capacity(tc.cpus, 0, 0)
		if err != nil || got != tc.want {
			t.Errorf("capacity(%d) = %+v, %v; want %+v", tc.cpus, got, err, tc.want)
		}
	}
	b, err := capacity(64, 1, 0)
	if err != nil || b.jobs != 1 || b.shards != 1 || !b.exclusiveE2E {
		t.Fatal(b, err)
	}
	if _, err := capacity(64, 8, 0); err == nil {
		t.Fatal("unbounded group count accepted")
	}
	if _, err := capacity(64, 0, 17); err == nil {
		t.Fatal("unsupported shard count accepted")
	}
}

func TestTasksCoverAllChecksWithPrivateWebAssets(t *testing.T) {
	for _, cpus := range []int{2, 16, 64} {
		b, _ := capacity(cpus, 0, 0)
		directory := t.TempDir()
		parallel, isolated := tasks(b, directory, "selected-go", "selected-pnpm", 1)
		all := append(slices.Clone(parallel), isolated...)
		names := make([]string, 0, len(all))
		for _, task := range all {
			names = append(names, task.name)
			if !slices.Contains(task.env, "GO=selected-go") || !slices.Contains(task.env, "TEST_COUNT=1") {
				t.Fatalf("toolchain or uncached verification lost for %s", task.name)
			}
			if task.name == "web" {
				want := "TEND_WEB_BUILD_OUT_DIR=" + filepath.Join(directory, "web-app")
				if !slices.Contains(task.env, want) {
					t.Fatal("web writes outside its private output directory")
				}
			}
		}
		slices.Sort(names)
		if !slices.Equal(names, []string{"api", "app", "e2e", "mainthread", "other", "quality", "web"}) {
			t.Fatal("checks omitted or repeated", names)
		}
		if b.exclusiveE2E && (len(isolated) != 1 || isolated[0].name != "e2e") {
			t.Fatal("transport suite lost its exclusive phase")
		}
	}
	got := environment([]string{"GO=old", "AUTH=preserved", "MAKEFLAGS=old"}, map[string]string{"GO": "new", "MAKEFLAGS": ""})
	if slices.Contains(got, "GO=old") || !slices.Contains(got, "GO=new") || !slices.Contains(got, "AUTH=preserved") {
		t.Fatal(got)
	}
}

func TestSchedulerBoundsConcurrencyAggregatesFailuresAndWaitsForIsolation(t *testing.T) {
	parallel := []task{{name: "a"}, {name: "b"}, {name: "c"}, {name: "d"}}
	isolated := []task{{name: "e2e"}}
	started, release := make(chan string, 5), make(chan struct{}, 5)
	var mu sync.Mutex
	active, peak := 0, 0
	var seen []string
	boom := errors.New("failed check")
	done := make(chan error, 1)
	go func() {
		done <- schedule(context.Background(), 2, parallel, isolated, func(_ context.Context, task task) error {
			mu.Lock()
			active++
			peak = max(peak, active)
			seen = append(seen, task.name)
			if task.name == "e2e" && (active != 1 || len(seen) != 5) {
				t.Error("exclusive test overlapped the parallel phase")
			}
			mu.Unlock()
			started <- task.name
			<-release
			mu.Lock()
			active--
			mu.Unlock()
			if task.name == "a" || task.name == "c" {
				return boom
			}
			return nil
		})
	}()
	for range 2 {
		<-started
	}
	select {
	case unexpected := <-started:
		t.Fatal("group budget exceeded", unexpected)
	default:
	}
	for range 5 {
		release <- struct{}{}
	}
	err := <-done
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "a:") || !strings.Contains(err.Error(), "c:") {
		t.Fatal("failures lost", err)
	}
	if peak != 2 || len(seen) != 5 || active != 0 {
		t.Fatal(peak, seen, active)
	}
}

func TestSchedulerWaitsForCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{}, 2)
	done := make(chan error, 1)
	go func() {
		done <- schedule(ctx, 2, []task{{name: "a"}, {name: "b"}}, nil, func(ctx context.Context, _ task) error {
			started <- struct{}{}
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	<-started
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
