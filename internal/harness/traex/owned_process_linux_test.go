//go:build linux

package traex

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/codexenv"
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
	for _, changed := range []string{"run", "thread", "pid generation", "reconciled scope", "malformed record", "missing record"} {
		t.Run(changed, func(t *testing.T) {
			ws, spec, cmd, record := ownedProcessFixture(t)
			switch changed {
			case "run":
				record.RunID = "another-run"
			case "thread":
				record.ThreadID = "another-thread"
			case "pid generation":
				record.Process.StartTicks += "1"
			case "reconciled scope":
				record.ReconciledCloseScope = "unsupported"
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

func TestOwnedRecoveryDoesNotInferGroupExitFromInactiveDetachedMember(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	ws, spec, root, record := ownedProcessFixture(t)
	leader := exec.CommandContext(ctx, "sleep", "60")
	procx.Contain(leader)
	if err := leader.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { procx.KillGroup(leader); _ = leader.Wait() })
	member := exec.CommandContext(ctx, "sh", "-c", "exit 0")
	member.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: leader.Process.Pid}
	if err := member.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = member.Wait() })
	var identity ownedProcessIdentity
	for {
		current, group, active, err := readOwnedProcess(member.Process.Pid)
		if err != nil || group != leader.Process.Pid {
			t.Fatal("detached member lost its original group", current, group, err)
		}
		if !active {
			identity = current
			identity.Group = group
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	record.Detached = []ownedProcessIdentity{identity}
	if err := saveOwnedWorkerProcess(spec, record); err != nil {
		t.Fatal(err)
	}
	procx.KillGroup(root)
	_ = root.Wait()
	procx.KillGroup(leader)
	_ = leader.Wait()
	// The unreaped member is inactive but still has group != PID. The kernel
	// group exists: a PID snapshot alone cannot prove that no child was born
	// between listing and inspection. External boundary proof remains usable.
	if err := ws.StopOwnedWorker(ctx, spec); !errors.Is(err, harness.ErrOwnedWorkerUnconfirmed) {
		t.Fatal("snapshot of inactive member was promoted to group closure", err)
	}
	saved, err := ws.ownedWorkerProcess(spec)
	if err != nil || saved.closed() {
		t.Fatal("unconfirmed kernel group became a successful close", saved, err)
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
		if err != nil || !sameOwnedProcessIdentity(current, process) || !active {
			t.Fatal("recovery signalled an independently running session", process, err)
		}
	}
}

func TestOwnedRecoveryReconcilesEnvironmentOnlyAfterConfirmedClose(t *testing.T) {
	_, spec, _, _ := ownedProcessFixture(t)
	svc := &codexenv.Service{Root: t.TempDir(), Validate: func(context.Context, codexenv.Profile, string) error { return nil }}
	profile := codexenv.Profile{ID: "original", Home: t.TempDir(), UserHome: t.TempDir(), Enabled: true}
	created, err := svc.Execute(t.Context(), codexenv.Request{Action: "create", ID: profile.ID, RequestID: "create", Profile: &profile})
	if err != nil {
		t.Fatal(err)
	}
	binding := &codexenv.Binding{Directory: svc.Root, ID: profile.ID}
	if _, err = codexenv.BindExecution(t.Context(), spec.StateDir, "run:"+spec.RunID, binding, false); err != nil {
		t.Fatal(err)
	}
	ref, err := binding.Pin(t.Context(), "run:"+spec.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err = ref.Record(t.Context(), "unknown"); err != nil {
		t.Fatal(err)
	}
	restarted := NewWorkers(WorkerOptions{Session: Options{Bin: "must-not-launch"}})
	changed := spec
	changed.Thread = "wrong-thread"
	if err = restarted.StopOwnedWorker(t.Context(), changed); !errors.Is(err, harness.ErrOwnedWorkerUnconfirmed) {
		t.Fatal(err)
	}
	status, err := svc.Read(profile.ID)
	if err != nil || status.Adoptions["run:"+spec.RunID].State != "unknown" {
		t.Fatal("unconfirmed stop cleared uncertainty", status, err)
	}
	if err = restarted.StopOwnedWorker(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	status, err = svc.Read(profile.ID)
	if err != nil || status.Adoptions["run:"+spec.RunID].State != "retained" {
		t.Fatal("confirmed close left execution permanently active", status, err)
	}
	if err = restarted.StopOwnedWorker(t.Context(), spec); err != nil {
		t.Fatal("close reconciliation is not idempotent", err)
	}
	if _, err = svc.Execute(t.Context(), codexenv.Request{Action: "delete", ID: profile.ID, RequestID: "delete", ExpectedRevision: created.Status.Revision}); err != nil {
		t.Fatal("closed execution blocked deletion", err)
	}
}

func TestUsageWriterObservationRequiresOriginalProcessExit(t *testing.T) {
	_, _, cmd, record := ownedProcessFixture(t)
	raw, _ := json.Marshal(record.Process)
	th := &thread{}
	if err := th.ConfirmUsageWriterStopped(t.Context(), string(raw)); err == nil {
		t.Fatal("live writer declared stopped")
	}
	procx.KillGroup(cmd)
	_ = cmd.Wait()
	deadline := time.Now().Add(time.Second)
	for {
		err := th.ConfirmUsageWriterStopped(t.Context(), string(raw))
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	if err := th.ConfirmUsageWriterStopped(t.Context(), `{"pid":1}`); err == nil {
		t.Fatal("incomplete identity declared stopped")
	}
}
