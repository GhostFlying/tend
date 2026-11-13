//go:build linux

package traex

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/codexenv"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

func startupEnvironment(t *testing.T) (*codexenv.Service, codexenv.Profile, *codexenv.Binding) {
	t.Helper()
	service := &codexenv.Service{Root: t.TempDir(), Validate: func(context.Context, codexenv.Profile, string) error { return nil }}
	profile := codexenv.Profile{ID: "worker", Home: t.TempDir(), UserHome: t.TempDir(), Enabled: true}
	result, err := service.Execute(t.Context(), codexenv.Request{Action: "create", ID: profile.ID, RequestID: "create", Profile: &profile})
	if err != nil {
		t.Fatal(err)
	}
	return service, result.Status.Profile, &codexenv.Binding{Directory: service.Root, ID: profile.ID}
}

func TestEnvironmentPreflightPreservesKnownNotStartedProof(t *testing.T) {
	service, profile, binding := startupEnvironment(t)
	profile.Enabled = false
	status, _ := service.Read(profile.ID)
	if _, err := service.Execute(t.Context(), codexenv.Request{Action: "update", ID: profile.ID, RequestID: "disable", ExpectedRevision: status.Revision, Profile: &profile}); err != nil {
		t.Fatal(err)
	}
	spec := harness.WorkerSpec{RunID: "never-started", StateDir: t.TempDir(), Dir: t.TempDir()}
	workers := NewWorkers(WorkerOptions{Session: Options{Bin: "must-not-launch", Environment: binding}})
	if _, err := workers.Start(t.Context(), spec); !errors.Is(err, harness.ErrWorkerNotStarted) {
		t.Fatal("preflight lost positive no-launch proof", err)
	}
	if _, err := os.Stat(filepath.Join(spec.StateDir, ownedProcessFile)); !os.IsNotExist(err) {
		t.Fatal("preflight created native ownership", err)
	}
}

func TestEnvironmentRunningRecordFailurePersistsOwnedClose(t *testing.T) {
	_, profile, binding := startupEnvironment(t)
	t.Setenv("HOME", profile.UserHome)
	bin := t.TempDir()
	native := filepath.Join(bin, "native")
	if err := os.WriteFile(native, []byte("#!/bin/sh\nwhile IFS= read -r fixture_line; do :; done\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	spec := harness.WorkerSpec{RunID: "startup-cancelled", StateDir: t.TempDir(), Dir: t.TempDir()}
	if _, err := codexenv.BindExecution(t.Context(), spec.StateDir, "run:"+spec.RunID, binding, false); err != nil {
		t.Fatal(err)
	}
	ref, err := binding.Pin(t.Context(), "run:"+spec.RunID)
	if err != nil {
		t.Fatal(err)
	}
	workers := NewWorkers(WorkerOptions{})
	opts := Options{Bin: native, Dir: spec.Dir, environmentReference: ref}
	if _, err = workers.recordOwnedWorkerProcess(spec, &opts); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	recordStart := opts.onStartProcess
	opts.onStartProcess = func(cmd *exec.Cmd) error {
		if err := recordStart(cmd); err != nil {
			return err
		}
		cancel()
		return nil
	}
	if _, err = startProcess(ctx, opts, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	record, err := workers.ownedWorkerProcess(spec)
	if err != nil || !record.CloseObserved {
		t.Fatal("startup state failure discarded owned close", record, err)
	}
	if err = NewWorkers(WorkerOptions{Session: Options{Bin: "must-not-launch"}}).StopOwnedWorker(t.Context(), spec); err != nil {
		t.Fatal("known closure cannot reconcile", err)
	}
}

func TestEnvironmentLaunchLockHonorsStartupCancellation(t *testing.T) {
	_, profile, binding := startupEnvironment(t)
	t.Setenv("HOME", profile.UserHome)
	ref, err := binding.Pin(t.Context(), "run:locked-startup")
	if err != nil {
		t.Fatal(err)
	}
	_, release, err := ref.Command(t.Context(), "/bin/true", nil, t.TempDir(), os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		s, err := startProcess(ctx, Options{Bin: "/bin/true", Dir: t.TempDir(), environmentReference: ref}, nil)
		if s != nil {
			_ = s.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, harness.ErrWorkerNotStarted) {
			t.Fatal("startup cancellation lost known no-launch proof", err)
		}
	case <-time.After(2 * time.Second):
		release()
		<-done
		t.Fatal("startup lock ignored the caller deadline")
	}
}

func TestContinuationMissingStrictEnvironmentDoesNotUseHistoryFallback(t *testing.T) {
	for _, kind := range []string{"question", "pause"} {
		t.Run(kind, func(t *testing.T) {
			_, _, binding := startupEnvironment(t)
			root := t.TempDir()
			spec := harness.WorkerSpec{RunID: "current", Dir: t.TempDir(), StateDir: filepath.Join(root, "current"), Thread: "strict-thread", PredecessorStateDir: filepath.Join(root, "missing"),
				SessionContinuation:      &protocol.SessionContinuation{RunID: "history", ThreadID: "history-thread"},
				ExternalWaitContinuation: &protocol.ExternalWait{RunID: "wait", ThreadID: "wait-thread"}}
			if kind == "question" {
				spec.QuestionContinuation = &protocol.QuestionContinuation{RunID: "strict", ThreadID: spec.Thread}
			} else {
				spec.PauseContinuation = &protocol.PauseContinuation{RunID: "strict", ThreadID: spec.Thread}
			}
			workers := NewWorkers(WorkerOptions{Session: Options{Environment: binding}})
			starts := 0
			workers.start = func(context.Context, Options) (*Session, error) {
				starts++
				return nil, harness.ErrWorkerNotStarted
			}
			_, err := workers.Start(t.Context(), spec)
			if !errors.Is(err, os.ErrNotExist) || !errors.Is(err, harness.ErrWorkerNotStarted) || starts != 0 {
				t.Fatalf("unrelated history replaced strict predecessor: starts=%d err=%v", starts, err)
			}
			if _, err := os.Stat(spec.StateDir); !os.IsNotExist(err) {
				t.Fatalf("missing original acquired another environment: %v", err)
			}
		})
	}
}
