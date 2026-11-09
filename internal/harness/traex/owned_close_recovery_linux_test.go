//go:build linux

package traex

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/procx"
)

func TestOwnedCloseRetainsGroupExitUntilObservedDetachedProcessEnds(t *testing.T) {
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
	// The fixture supplies a completed descendant observation, with a live
	// process outside the original group, at the actual session close boundary.
	opts.onBeforeCloseProcess = func() error {
		record.Detached = []ownedProcessIdentity{identity}
		if err := saveOwnedWorkerProcess(spec, *record); err != nil {
			return err
		}
		return errDetachedNativeActive
	}
	s, err := startProcess(ctx, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	record.ThreadID = spec.Thread
	if err := s.Close(); !errors.Is(err, errDetachedNativeActive) {
		t.Fatalf("live detached work lost its uncertainty: %v", err)
	}
	saved, err := ws.ownedWorkerProcess(spec)
	if err != nil || !saved.CloseObserved || len(saved.Detached) != 1 {
		t.Fatalf("original group exit was lost: %+v error=%v", saved, err)
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
		t.Fatal("completed detached work could not reconcile original close", err)
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
