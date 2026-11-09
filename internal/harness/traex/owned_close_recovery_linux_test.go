//go:build linux

package traex

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/procx"
)

func TestOwnedCloseLateDetachedForkRemainsUnconfirmed(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	readPID := func(name string) int {
		for {
			body, err := os.ReadFile(filepath.Join(dir, name))
			if err == nil {
				if pid, err := strconv.Atoi(strings.TrimSpace(string(body))); err == nil {
					return pid
				}
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(time.Millisecond):
			}
		}
	}
	// The owned app-server creates a real detached shell. Only after its close
	// snapshot and exit does that shell fork another live process and exit.
	ws := NewWorkers(WorkerOptions{Session: Options{Bin: "must-not-launch"}})
	spec := harness.WorkerSpec{RunID: "original-run", Dir: t.TempDir(), StateDir: t.TempDir(), Thread: "original-thread"}
	opts := Options{Bin: bin, Dir: spec.Dir, Env: []string{envAcceptanceCloseServer + "=1", "TEND_TRAEX_TEST_LATE_DETACHED_DIR=" + dir}}
	record, err := ws.recordOwnedWorkerProcess(spec, &opts)
	if err != nil {
		t.Fatal(err)
	}
	s, err := startProcess(ctx, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	record.ThreadID = spec.Thread
	parent := readPID("parent")
	t.Cleanup(func() { _ = syscall.Kill(-parent, syscall.SIGKILL) })
	if err := s.Close(); err == nil {
		t.Fatal("observed detached work lost its close uncertainty")
	}
	saved, err := ws.ownedWorkerProcess(spec)
	if err != nil || len(saved.Detached) == 0 {
		t.Fatal("close did not retain the actual detached snapshot", saved, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "launch"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	grandchild := readPID("grandchild")
	for confirmDetachedProcessesStopped(saved.Detached) != nil {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	if _, _, active, err := readOwnedProcess(grandchild); err != nil || !active {
		t.Fatal("late detached descendant is not active", active, err)
	}
	if err := ws.StopOwnedWorker(ctx, spec); !errors.Is(err, harness.ErrOwnedWorkerUnconfirmed) {
		t.Fatalf("recorded PIDs exited but later descendant %d is active; close=%v", grandchild, err)
	}
	saved, err = ws.ownedWorkerProcess(spec)
	if err != nil || saved.CloseObserved {
		t.Fatal("detached PID exit manufactured complete closure", saved, err)
	}
}

func TestOwnedCloseDetachedExitReconcilesKnownProcessSet(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.CommandContext(ctx, "sleep", "60")
	procx.Contain(child)
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { procx.KillGroup(child); _ = child.Wait() })
	identity, _, active, err := readOwnedProcess(child.Process.Pid)
	if err != nil || !active {
		t.Fatalf("detached fixture did not start: active=%t error=%v", active, err)
	}
	ws := NewWorkers(WorkerOptions{})
	spec := harness.WorkerSpec{RunID: "original-run", Dir: t.TempDir(), StateDir: t.TempDir(), Thread: "original-thread"}
	opts := Options{Bin: bin, Dir: spec.Dir, Env: []string{envAcceptanceCloseServer + "=1"}}
	record, err := ws.recordOwnedWorkerProcess(spec, &opts)
	if err != nil {
		t.Fatal(err)
	}
	// The fixture supplies a descendant observation, with a live process
	// outside the original group, at the actual session close boundary.
	detachedErr := errors.New("observed detached native execution")
	opts.onBeforeCloseProcess = func() error {
		record.Detached = []ownedProcessIdentity{identity}
		if err := saveOwnedWorkerProcess(spec, *record); err != nil {
			return err
		}
		return detachedErr
	}
	s, err := startProcess(ctx, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	record.ThreadID = spec.Thread
	if err := s.Close(); !errors.Is(err, detachedErr) {
		t.Fatalf("live detached work lost its uncertainty: %v", err)
	}
	saved, err := ws.ownedWorkerProcess(spec)
	if err != nil || saved.CloseObserved || len(saved.Detached) != 1 {
		t.Fatalf("detached observation lost its uncertainty: %+v error=%v", saved, err)
	}
	if err := confirmOwnedProcessStopped(saved.Process); err != nil {
		t.Fatal("original group did not actually exit", err)
	}
	if err := ws.StopOwnedWorker(ctx, spec); !errors.Is(err, harness.ErrOwnedWorkerUnconfirmed) {
		t.Fatalf("live detached process became completed execution: %v", err)
	}
	current, _, active, err := readOwnedProcess(identity.PID)
	if err != nil || current != identity || !active {
		t.Fatal("reconciliation changed the detached fixture", current, active, err)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	if err := ws.StopOwnedWorker(ctx, spec); err != nil {
		t.Fatal("original known process set could not reconcile after exit", err)
	}
	saved, err = ws.ownedWorkerProcess(spec)
	if err != nil || saved.CloseObserved || saved.ReconciledCloseScope != knownOwnedProcessSet || saved.Process != record.Process || len(saved.Detached) != 1 || saved.Detached[0] != identity {
		t.Fatalf("reconciliation changed the original close or identities: %+v error=%v", saved, err)
	}
	if err := NewWorkers(WorkerOptions{Session: Options{Bin: "must-not-launch"}}).StopOwnedWorker(ctx, spec); err != nil {
		t.Fatal("original process reconciliation did not survive client restart", err)
	}
}

func TestOwnedCloseDoesNotReplaceFailedObservationWithGroupExit(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ws := NewWorkers(WorkerOptions{})
	spec := harness.WorkerSpec{RunID: "original-run", Dir: t.TempDir(), StateDir: t.TempDir(), Thread: "original-thread"}
	opts := Options{Bin: bin, Dir: spec.Dir, Env: []string{envAcceptanceCloseServer + "=1"}}
	record, err := ws.recordOwnedWorkerProcess(spec, &opts)
	if err != nil {
		t.Fatal(err)
	}
	observationErr := errors.New("original process observation failed")
	opts.onBeforeCloseProcess = func() error { return observationErr }
	s, err := startProcess(ctx, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	record.ThreadID = spec.Thread
	if err := saveOwnedWorkerProcess(spec, *record); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); !errors.Is(err, observationErr) {
		t.Fatalf("original observation error was lost: %v", err)
	}
	saved, err := ws.ownedWorkerProcess(spec)
	if err != nil || saved.CloseObserved {
		t.Fatalf("incomplete observation became a close fact: %+v error=%v", saved, err)
	}
}
