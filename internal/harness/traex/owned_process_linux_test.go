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

func ownedProcessFixture(t *testing.T, detached ...bool) (*Workers, harness.WorkerSpec, *exec.Cmd, ownedWorkerProcess) {
	t.Helper()
	spec := harness.WorkerSpec{RunID: "original-run", StateDir: t.TempDir(), Dir: t.TempDir(), Thread: "original-thread"}
	ws := NewWorkers(WorkerOptions{})
	opts := Options{}
	record, err := ws.recordOwnedWorkerProcess(spec, &opts)
	if err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(t.TempDir(), "child")
	command := `sleep 60 & echo $! > "$1"; wait`
	if len(detached) != 0 && detached[0] {
		// Publish readiness from inside the new session. The parent's $!
		// becomes available before the asynchronous child executes setsid.
		command = `setsid sh -c 'echo $$ > "$1"; exec sleep 60' fixture "$1" & wait`
	}
	cmd := exec.CommandContext(t.Context(), "sh", "-c", command, "fixture", child)
	procx.Contain(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		procx.KillGroup(cmd)
		_ = cmd.Wait()
	})
	if err := opts.onStartProcess(cmd); err != nil {
		t.Fatal(err)
	}
	record.ThreadID = spec.Thread
	if err := saveOwnedWorkerProcess(spec, *record); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		if body, err := os.ReadFile(child); err == nil && strings.TrimSpace(string(body)) != "" {
			pid, err := strconv.Atoi(strings.TrimSpace(string(body)))
			if err != nil {
				t.Fatal(err)
			}
			_, group, active, err := readOwnedProcess(pid)
			wantGroup := cmd.Process.Pid
			if len(detached) != 0 && detached[0] {
				wantGroup = pid
				identity, _, _, readErr := readOwnedProcess(pid)
				if readErr != nil {
					t.Fatal(readErr)
				}
				t.Cleanup(func() {
					current, _, active, err := readOwnedProcess(pid)
					if err == nil && active && current == identity {
						process, err := os.FindProcess(pid)
						if err == nil {
							_ = process.Kill()
							_ = process.Release()
						}
					}
				})
			}
			if err != nil || group != wantGroup || !active {
				t.Fatalf("child was not contained: group=%d active=%t error=%v", group, active, err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("owned child did not start")
		}
		time.Sleep(time.Millisecond)
	}
	return ws, spec, cmd, *record
}

func TestOwnedAppServerRecoveryAfterClientRestartStopsExactProcessGroup(t *testing.T) {
	_, spec, _, original := ownedProcessFixture(t)
	// The new client has no exec.Cmd, Wait handle or in-memory session map.
	restarted := NewWorkers(WorkerOptions{Session: Options{Bin: "must-not-launch"}})
	if err := restarted.StopOwnedWorker(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	if err := confirmOwnedProcessStopped(original.Process); err != nil {
		t.Fatal(err)
	}
	if err := restarted.StopOwnedWorker(t.Context(), spec); err != nil {
		t.Fatal("repeated stop lost original evidence", err)
	}
	if _, err := os.Stat(filepath.Join(spec.StateDir, ownedProcessFile)); err != nil {
		t.Fatal("recovery deleted original ownership evidence", err)
	}
}

func TestOwnedAppServerRecoveryRejectsChangedIdentityWithoutSignalling(t *testing.T) {
	for _, changed := range []string{"run", "thread", "pid generation", "malformed record", "missing record"} {
		t.Run(changed, func(t *testing.T) {
			ws, spec, cmd, record := ownedProcessFixture(t)
			switch changed {
			case "run":
				record.RunID = "another-run"
			case "thread":
				record.ThreadID = "another-thread"
			case "pid generation":
				record.Process.StartTicks += "1"
			}
			if err := saveOwnedWorkerProcess(spec, record); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(spec.StateDir, ownedProcessFile)
			switch changed {
			case "malformed record":
				if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing record":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			if err := ws.StopOwnedWorker(t.Context(), spec); err == nil {
				t.Fatal("changed identity was accepted")
			}
			if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
				t.Fatal("identity mismatch stopped an unrelated process", err)
			}
			_, _, active, err := readOwnedProcess(cmd.Process.Pid)
			if err != nil || !active {
				t.Fatal("identity mismatch changed the process", err)
			}
		})
	}
}

func TestOwnedAppServerCannotOverwriteUnsettledProcessWithAnotherLaunch(t *testing.T) {
	ws, spec, _, _ := ownedProcessFixture(t)
	launches := 0
	ws.start = func(context.Context, Options) (*Session, error) {
		launches++
		return nil, errors.New("unexpected replacement launch")
	}
	if _, err := ws.Start(t.Context(), spec); !errors.Is(err, harness.ErrRetainedResumePending) || launches != 0 {
		t.Fatalf("unsettled original allowed another native launch: launches=%d error=%v", launches, err)
	}
}

func TestOwnedAppServerMissingLeaderDoesNotKillUnverifiedDescendants(t *testing.T) {
	ws, spec, cmd, _ := ownedProcessFixture(t)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if err := ws.StopOwnedWorker(ctx, spec); !errors.Is(err, harness.ErrOwnedWorkerUnconfirmed) {
		t.Fatalf("unverified descendant group was accepted or wait was unbounded: %v", err)
	}
	if active, err := ownedGroupActive(cmd.Process.Pid); err != nil || !active {
		t.Fatal("missing leader caused an unverified group signal", active, err)
	}
}

func TestOwnedAppServerDetachedNativeExecutionStaysUnknownAndIsolated(t *testing.T) {
	ws, spec, cmd, original := ownedProcessFixture(t, true)
	detached, err := ownedDetachedDescendants(original.Process)
	if err != nil || len(detached) == 0 {
		t.Fatal("setsid child was not observed outside the app-server group", detached, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := ws.StopOwnedWorker(ctx, spec); !errors.Is(err, harness.ErrOwnedWorkerUnconfirmed) || ctx.Err() != nil {
		t.Fatalf("detached execution became a close receipt or blocked indefinitely: %v", err)
	}
	record, err := ws.ownedWorkerProcess(spec)
	if err != nil || record.CloseObserved || len(record.Detached) == 0 {
		t.Fatal("detached execution was not retained as unresolved native ownership", record, err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if err := NewWorkers(WorkerOptions{}).StopOwnedWorker(ctx, spec); !errors.Is(err, harness.ErrOwnedWorkerUnconfirmed) {
		t.Fatalf("leader exit erased detached native execution: %v", err)
	}
	for _, process := range detached {
		current, _, active, err := readOwnedProcess(process.PID)
		if err != nil || current != process || !active {
			t.Fatal("recovery signalled an independently running session", process, err)
		}
	}
}
