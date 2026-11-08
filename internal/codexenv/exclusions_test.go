//go:build linux

package codexenv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSkillExclusionsShareSixHourLimitAcrossConcurrentLaunches(t *testing.T) {
	user := t.TempDir()
	now := time.Unix(1700000000, 0)
	var scans atomic.Int32
	scan := func(context.Context, string) ([]string, error) {
		scans.Add(1)
		return []string{"/personal/z/SKILL.md", "/personal/a/SKILL.md", "/personal/a/SKILL.md"}, nil
	}
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			got, err := refreshExclusions(t.Context(), user, now, scan)
			if err != nil || !slices.Equal(got.Paths, []string{"/personal/a/SKILL.md", "/personal/z/SKILL.md"}) {
				t.Errorf("shared inventory: %+v %v", got, err)
			}
		})
	}
	wg.Wait()
	if scans.Load() != 1 {
		t.Fatal("concurrent launch rescanned personal skills", scans.Load())
	}
	for _, at := range []time.Time{now.Add(5 * time.Hour), now.Add(-time.Hour)} {
		if _, err := refreshExclusions(t.Context(), user, at, scan); err != nil {
			t.Fatal(err)
		}
	}
	if scans.Load() != 1 {
		t.Fatal("cache did not survive a new caller or backwards clock")
	}
	if _, err := refreshExclusions(t.Context(), user, now.Add(6*time.Hour), scan); err != nil || scans.Load() != 2 {
		t.Fatal("six-hour refresh missing", scans.Load(), err)
	}
}

func TestSkillExclusionsThrottleFailureAndKeepLastGoodInventory(t *testing.T) {
	user := t.TempDir()
	now := time.Unix(1700000000, 0)
	good := func(context.Context, string) ([]string, error) { return []string{"/personal/known/SKILL.md"}, nil }
	first, err := refreshExclusions(t.Context(), user, now, good)
	if err != nil {
		t.Fatal(err)
	}
	failed := func(context.Context, string) ([]string, error) { return nil, errors.New("controlled scan failure") }
	got, err := refreshExclusions(t.Context(), user, now.Add(6*time.Hour), failed)
	if err != nil || got.LastError == "" || !got.RefreshedAt.Equal(first.RefreshedAt) || !slices.Equal(got.Paths, first.Paths) {
		t.Fatal("refresh failure lost prior inventory", got, err)
	}
	never := func(context.Context, string) ([]string, error) {
		t.Error("retried scan inside six-hour window")
		return nil, nil
	}
	if _, err = refreshExclusions(t.Context(), user, now.Add(7*time.Hour), never); err != nil {
		t.Fatal(err)
	}
	missing := t.TempDir()
	if _, err = refreshExclusions(t.Context(), missing, now, failed); err == nil {
		t.Fatal("initial failed inventory was treated as empty success")
	}
	if _, err = refreshExclusions(t.Context(), missing, now.Add(time.Minute), never); err == nil {
		t.Fatal("initial failure lost its throttle")
	}
}

func TestPersonalSkillInventoryFollowsLinksWithoutScanningProjects(t *testing.T) {
	user, outside := t.TempDir(), t.TempDir()
	root := filepath.Join(user, ".agents", "skills")
	real := skill(t, outside, "linked", "fixture")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"one": real, "two": real, "cycle": root, "broken": filepath.Join(outside, "missing")} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	skill(t, filepath.Join(user, "project", ".agents", "skills"), "project", "must remain")
	got, err := scanPersonalSkills(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	got = slices.Compact(got)
	if !slices.Equal(got, []string{filepath.Join(real, "SKILL.md")}) {
		t.Fatal("wrong discovery boundary", got)
	}
}

func TestSkillExclusionCancelledScanRemainsThrottledAfterRestart(t *testing.T) {
	user := t.TempDir()
	now := time.Unix(1700000000, 0)
	ctx, cancel := context.WithCancel(t.Context())
	_, err := refreshExclusions(ctx, user, now, func(context.Context, string) ([]string, error) {
		cancel()
		return nil, ctx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	_, err = refreshExclusions(t.Context(), user, now.Add(time.Minute), func(context.Context, string) ([]string, error) {
		t.Error("cancelled scan lost its persisted six-hour admission")
		return nil, nil
	})
	if err == nil {
		t.Fatal("incomplete first inventory was treated as valid")
	}
}
