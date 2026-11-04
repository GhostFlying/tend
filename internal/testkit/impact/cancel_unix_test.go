//go:build !windows

package impact

import (
	"context"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestImpactOrchestratorHelper(t *testing.T) {
	if len(os.Args) < 4 || os.Args[len(os.Args)-3] != "--" {
		return
	}
	mode, record := os.Args[len(os.Args)-2], os.Args[len(os.Args)-1]
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if mode == "leaf" {
		if err := os.WriteFile(record, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			t.Fatal(err)
		}
		<-ctx.Done()
		return
	}
	next := "inner"
	if mode == "inner" {
		next = "leaf"
	}
	call := CommandGraceful
	if mode == "inner" {
		call = Command
	}
	err := call(ctx, "", io.Discard, nil, os.Args[0], "-test.run=^TestImpactOrchestratorHelper$", "--", next, record)
	if ctx.Err() == nil {
		t.Fatalf("unexpected early child exit: %v", err)
	}
}

func TestSelectedRunCancellationCleansRaceValidationChild(t *testing.T) {
	tools, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	runner := filepath.Join(t.TempDir(), "racecheck")
	fixtureCommand(t, tools, "go", "build", "-o", runner, "../racecheck")
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEND_IMPACT_REAL_GO", realGo)
	dir := fixtureRepo(t)
	fixtureWrite(t, dir, "Makefile", "go-check:\n\t@touch .local/block-env\n")
	wrapper := filepath.Join(dir, ".local/go-wrapper")
	fixtureWrite(t, dir, ".local/go-wrapper", "#!/bin/sh\nif [ \"$1\" = env ] && [ -e .local/block-env ]; then echo $$ > .local/child.pid; exec sleep 300; fi\nexec \"$TEND_IMPACT_REAL_GO\" \"$@\"\n")
	if err := os.Chmod(wrapper, 0o700); err != nil {
		t.Fatal(err)
	}
	before, err := Capture(t.Context(), dir, wrapper)
	if err != nil {
		t.Fatal(err)
	}
	plan := Select(before, indexFor(before), "")
	plan.Go[before.Module] = []string{"TestA"}
	path := filepath.Join(dir, ".local/plan.json")
	if err := WriteJSON(path, plan); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := Run(ctx, RunOptions{Dir: dir, Go: wrapper, RaceRunner: runner, PlanPath: path, Jobs: 1, Procs: 1, Packages: 1}, plan)
		done <- err
	}()
	var pid int
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(filepath.Join(dir, ".local/child.pid")); err == nil {
			if candidate, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && candidate > 0 {
				pid = candidate
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("race validation child never became ready")
	}
	defer syscall.Kill(pid, syscall.SIGKILL)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled selected execution reported success")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("selected execution did not cancel")
	}
	if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
		t.Fatalf("separate race validation child survived cancellation: %v", err)
	}
}

func TestNestedOrchestratorCancellationCleansChildGroups(t *testing.T) {
	record := t.TempDir() + "/leaf"
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- CommandGraceful(ctx, "", io.Discard, nil, os.Args[0], "-test.run=^TestImpactOrchestratorHelper$", "--", "outer", record)
	}()
	deadline := time.Now().Add(15 * time.Second)
	var pid int
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(record); err == nil {
			// WriteFile creates the marker before writing its PID. Observing
			// that empty intermediate file is not a readiness notification.
			if candidate, err := strconv.Atoi(string(b)); err == nil && candidate > 0 {
				pid = candidate
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("nested leaf never became ready")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancellation reported success")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("orchestrator did not stop")
	}
	if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
		t.Fatalf("leaf process survived cancellation: %v", err)
	}
}
