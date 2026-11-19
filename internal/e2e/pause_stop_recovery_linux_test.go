//go:build linux

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/traex"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner"
	"github.com/GhostFlying/tend/internal/runner/gitws"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
	"github.com/GhostFlying/tend/internal/views"
	"golang.org/x/sys/unix"
)

type pauseRecoveryWorkers struct {
	*traex.Workers
	present atomic.Int64
}

func (w *pauseRecoveryWorkers) ObserveOwnedWorkerStop(ctx context.Context, spec harness.WorkerSpec) error {
	err := w.Workers.ObserveOwnedWorkerStop(ctx, spec)
	if errors.Is(err, harness.ErrOwnedWorkerStillPresent) {
		w.present.Add(1)
	}
	return err
}

func TestFault_PauseAutomaticallySettlesOriginalStopAfterLeaseExpiry(t *testing.T) {
	// Reap the fixture's detached child ourselves. The host's init process
	// may leave an orphaned zombie group, which does not prove closure.
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 0, 0, 0, 0) })
	for _, restart := range []bool{false, true} {
		name := "same runner"
		if restart {
			name = "runner restart"
		}
		t.Run(name, func(t *testing.T) { testPauseStopRecovery(t, restart) })
	}
}

func testPauseStopRecovery(t *testing.T, restart bool) {
	clock := testkit.NewClock()
	ttl := time.Minute
	srv := newServer(t, ttl, clock.Now)
	ctx := t.Context()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	logPath, pidPath := filepath.Join(home, "native.log"), filepath.Join(home, "detached.pid")
	workers := &pauseRecoveryWorkers{Workers: traex.NewWorkers(traex.WorkerOptions{Session: traex.Options{Bin: bin, Stderr: os.Stderr, Env: []string{
		acceptanceRecoveryHelper + "=" + logPath, pauseRecoveryChildPID + "=" + pidPath,
	}}})}

	var mu sync.Mutex
	var envelopes []protocol.Envelope
	var holdReplay, failNextEventUpload, eventUploadFailed, holdBeforeReceipt atomic.Bool
	var receiptHoldHellos atomic.Int64
	firstUpload := make(chan struct{})
	releaseUpload := make(chan struct{})
	holdReplay.Store(restart)
	net := &network{before: func(req *http.Request) error {
		if req.Body == nil {
			return nil
		}
		raw, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return err
		}
		req.Body = io.NopCloser(bytes.NewReader(raw))
		var env protocol.Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			return err
		}
		if env.Operation == protocol.OpRunnerHello && holdBeforeReceipt.Load() {
			receiptHoldHellos.Add(1)
		}
		if env.Operation == protocol.OpRunnerEvents && failNextEventUpload.CompareAndSwap(true, false) {
			close(firstUpload)
			if !restart {
				select {
				case <-releaseUpload:
				case <-req.Context().Done():
					return req.Context().Err()
				}
			}
			eventUploadFailed.Store(true)
			return errors.New("injected disconnect while flushing original Run events")
		}
		if env.Operation == protocol.OpRunsYieldPause {
			mu.Lock()
			if len(envelopes) > 0 && holdReplay.Load() {
				mu.Unlock()
				<-req.Context().Done()
				return req.Context().Err()
			}
			envelopes = append(envelopes, env)
			mu.Unlock()
		}
		return nil
	}}
	net.loseNext(protocol.OpRunsYieldPause, 1)

	var activeJournal *journal.Journal
	var cancel context.CancelFunc
	var done chan error
	stop := func() {
		if cancel != nil {
			cancel()
			if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
				t.Error(err)
			}
			cancel = nil
		}
	}
	var pid int
	t.Cleanup(func() {
		stop()
		var status unix.WaitStatus
		if pid > 1 {
			if reaped, _ := unix.Wait4(pid, &status, unix.WNOHANG, nil); reaped == 0 {
				_ = unix.Kill(pid, unix.SIGKILL)
				_, _ = unix.Wait4(pid, &status, 0, nil)
			}
		}
	})
	start := func() {
		j, err := journal.Open(ctx, filepath.Join(home, "runner.db"), clock.Now)
		if err != nil {
			t.Fatal(err)
		}
		activeJournal = j
		var runnerCtx context.Context
		runnerCtx, cancel = context.WithCancel(ctx)
		done = make(chan error, 1)
		r := runner.New(runner.Config{
			ServerURL: srv.url, HostID: "linux-1", HostToken: hostToken, Version: "test",
			HTTPClient: &http.Client{Transport: net}, Journal: j, Workers: workers,
			Workspaces: gitws.New(gitws.Config{Home: home, Log: quiet(), Now: clock.Now}),
			Now:        clock.Now, IDs: protocol.NewULIDGen(clock.Now, nil), Log: runnerTestLog(t),
			HeartbeatInterval: 20 * time.Millisecond, RetryDelay: 25 * time.Millisecond, EventFlush: 20 * time.Millisecond,
		})
		finished := done
		go func() { finished <- errors.Join(r.Run(runnerCtx), j.Close()) }()
	}
	start()
	eventually(t, "runner registers the pause handoff capability", func() bool {
		return srv.count(`SELECT COUNT(*) FROM host_runtime WHERE runtime_json LIKE '%run-pause-handoff-v1%'`) == 1
	})
	issue, taskID := createPauseRecoveryTask(t, srv)
	var original journal.Run
	eventually(t, "the original app-server turn starts a detached child", func() bool {
		raw, err := os.ReadFile(pidPath)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(string(raw))
		if err != nil || pid <= 1 || srv.runState(protocol.RoleImplement) != protocol.RunRunning {
			return false
		}
		runs, err := activeJournal.Runs(ctx)
		if err != nil || len(runs) != 1 || runs[0].Thread == "" {
			return false
		}
		original = runs[0]
		return true
	}, func() {
		log, logErr := os.ReadFile(logPath)
		runs, runErr := activeJournal.Runs(ctx)
		t.Logf("startup diagnostics task state=%s runs=%d (%v) native=%s (%v)", srv.task(taskID).Lifecycle, len(runs), runErr, log, logErr)
	})

	as := original.Assignment
	readIssue := func() protocol.IssueView {
		t.Helper()
		var v protocol.IssueView
		if err := srv.store.Read(ctx, func(tx *store.ReadTx) error {
			var err error
			v, err = views.CanonicalIssue(ctx, tx.Q(), issue.IssueID, 0)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return v
	}
	issue = readIssue().Issue
	pause, _ := json.Marshal(protocol.SetIssueStatusPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Status: protocol.IssueBacklog})
	pauseEnv := &protocol.Envelope{RequestID: srv.ids.New(protocol.PrefixRequest), Operation: "issues.set_status", Target: protocol.Target{Type: "issue", ID: issue.IssueID}, Payload: pause, ExpectedControlRevision: &issue.Revision}
	if op, err := srv.app.Execute(ctx, owner, pauseEnv, app.CanonicalOwnerIssueCommand{Action: "set_status"}); err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal(op, err)
	}
	eventually(t, "failed Close leaves the same attempted pause pending", func() bool {
		run, err := activeJournal.Run(ctx, as.RunID)
		progress := readIssue().Pause
		return err == nil && run.Phase == journal.PhasePausing && !run.StoppedConfirmed && progress != nil && progress.Pending == 1
	})
	clock.Advance(ttl + time.Second)
	eventually(t, "the original lease lapses while the stop remains pending", func() bool {
		return srv.runState(protocol.RoleImplement) == protocol.RunUnknown && workerQuestionLease(t, srv, as.RunID).Status == store.LeaseLapsed
	})
	if restart {
		stop()
		start()
	}
	clock.Advance(time.Second)
	eventually(t, "automatic observation still sees the original process set", func() bool {
		clock.Advance(25 * time.Millisecond)
		return workers.present.Load() > 0
	})
	if progress := readIssue().Pause; progress == nil || progress.Pending != 1 || srv.runState(protocol.RoleImplement) != protocol.RunUnknown {
		t.Fatal("an active detached child settled or reclassified the original stop", progress)
	}
	mu.Lock()
	if len(envelopes) != 0 {
		t.Error("sent physical stop while the original child was alive", envelopes)
	}
	mu.Unlock()
	setControl := func(mode protocol.ControlMode) {
		t.Helper()
		task := srv.task(taskID)
		raw, err := json.Marshal(protocol.SetControlModePayload{Mode: mode})
		if err != nil {
			t.Fatal(err)
		}
		env := &protocol.Envelope{RequestID: srv.ids.New(protocol.PrefixRequest), Operation: "control.set_mode", Target: protocol.Target{Type: "task", ID: taskID}, Payload: raw, ExpectedControlRevision: &task.ControlRevision}
		if op, err := srv.app.Execute(ctx, owner, env, app.SetControlMode{}); err != nil || op.Status != protocol.OpSucceeded {
			t.Fatal(op, err)
		}
	}
	if !restart {
		setControl(protocol.ControlHumanControl)
		eventually(t, "pause reconciliation sees the owner's hold", func() bool {
			clock.Advance(25 * time.Millisecond)
			return workerQuestionLease(t, srv, as.RunID).Status == store.LeaseActive
		})
		observed := workers.present.Load()
		// Keep the hold beyond the prior retry and permit more Hello cycles.
		// During the hold none of them may run the stop observer.
		heldUntil := time.Now().Add(500 * time.Millisecond)
		eventually(t, "owner control stays in force across the pause retry", func() bool {
			clock.Advance(25 * time.Millisecond)
			return time.Now().After(heldUntil)
		})
		if workers.present.Load() != observed {
			t.Fatal("pause observer ran during owner control")
		}
		setControl(protocol.ControlManaged)
	}
	if err := activeJournal.AppendEvent(ctx, as.RunID, protocol.RunEvent{Kind: "worker.observed", Body: json.RawMessage(`{"source":"pause-recovery-test"}`)}); err != nil {
		t.Fatal(err)
	}
	failNextEventUpload.Store(true)
	var status unix.WaitStatus
	if reaped, err := unix.Wait4(pid, &status, unix.WNOHANG, nil); err != nil || reaped != 0 {
		t.Fatalf("original detached child was not reparented alive: %d %v", reaped, err)
	}
	if err := unix.Kill(pid, unix.SIGTERM); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the real detached child exits and is reaped", func() bool {
		reaped, err := unix.Wait4(pid, &status, unix.WNOHANG, nil)
		return err == nil && reaped == pid
	})
	if !restart {
		eventually(t, "the original event upload reaches the transport after physical close", func() bool {
			clock.Advance(25 * time.Millisecond)
			select {
			case <-firstUpload:
				return true
			default:
				return false
			}
		})
		setControl(protocol.ControlHumanControl)
		holdBeforeReceipt.Store(true)
		close(releaseUpload)
		eventually(t, "the releasing Run reconciles through repeated Hello during takeover", func() bool {
			clock.Advance(25 * time.Millisecond)
			progress := readIssue().Pause
			if progress == nil || progress.Pending != 1 || progress.Stopped != 0 ||
				workerQuestionLease(t, srv, as.RunID).Status == store.LeaseReleased {
				t.Fatal("the first pause receipt settled during owner control", progress)
			}
			return eventUploadFailed.Load() && receiptHoldHellos.Load() >= 2
		})
		mu.Lock()
		sent := len(envelopes)
		mu.Unlock()
		if sent != 0 {
			t.Fatalf("runner submitted the first pause receipt during takeover: %d requests", sent)
		}
		holdBeforeReceipt.Store(false)
		setControl(protocol.ControlManaged)
	}
	eventually(t, "the first event upload fails after physical closure", func() bool {
		clock.Advance(25 * time.Millisecond)
		return eventUploadFailed.Load()
	})
	if restart {
		eventually(t, "server commits the pause before the interrupted runner acknowledges it", func() bool {
			clock.Advance(25 * time.Millisecond)
			run, err := activeJournal.Run(ctx, as.RunID)
			progress := readIssue().Pause
			return err == nil && run.Phase == journal.PhaseReleasing && progress != nil && progress.Pending == 0
		})
		stop()
		holdReplay.Store(false)
		start()
	}
	eventually(t, "pause settles and the original lost receipt is acknowledged", func() bool {
		clock.Advance(25 * time.Millisecond)
		run, err := activeJournal.HistoricalRun(ctx, as.RunID)
		progress := readIssue().Pause
		return err == nil && run.Phase == journal.PhaseDone && progress != nil && progress.Pending == 0 && progress.Stopped == 1
	})
	final, err := activeJournal.HistoricalRun(ctx, as.RunID)
	if err != nil || !final.StoppedConfirmed || final.Attempts != original.Attempts || !reflect.DeepEqual(final.Assignment, as) ||
		workerQuestionLease(t, srv, as.RunID).LeaseID != as.LeaseID || srv.runState(protocol.RoleImplement) != protocol.RunFinished {
		t.Fatalf("changed the original Run, lease or attempt: %+v, %v", final, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(envelopes) != 2 || !reflect.DeepEqual(envelopes[0], envelopes[1]) || envelopes[0].Target.ID != as.RunID || envelopes[0].RequestID == "" {
		t.Fatal("lost response must replay the original receipt", envelopes)
	}
	var yielded protocol.YieldPausePayload
	if err := json.Unmarshal(envelopes[0].Payload, &yielded); err != nil || yielded.RequestID != pauseEnv.RequestID || yielded.IssueID != issue.IssueID || yielded.ThreadID != original.Thread {
		t.Fatal("pause receipt changed the original identity", yielded, err)
	}
	if srv.count(`SELECT COUNT(*) FROM events WHERE object_id=? AND kind='run.pause_yielded'`, as.RunID) != 1 ||
		srv.count(`SELECT COUNT(*) FROM runs WHERE task_id=?`, taskID) != 1 {
		t.Fatal("pause issued a second transition or started a successor")
	}
	log, err := os.ReadFile(logPath)
	if err != nil || bytes.Count(log, []byte("turn/start\n")) != 1 || bytes.Count(log, []byte("thread/start\n")) != 1 {
		t.Fatalf("stop reconciliation entered native work: %s, %v", log, err)
	}
}

func createPauseRecoveryTask(t *testing.T, srv *server) (protocol.Issue, string) {
	t.Helper()
	created := srv.exec(owner, "issues.create", protocol.Target{Type: "issue"}, protocol.CreateIssuePayload{
		Plan: protocol.IssuePlan{Title: "Pause owned app-server", Project: "default", Intent: "Retain the original run during pause"},
	}, app.CanonicalOwnerIssueCommand{Action: "create"})
	var issue protocol.Issue
	if err := json.Unmarshal(created.Result, &issue); err != nil {
		t.Fatal(err)
	}
	id := srv.ids.New(protocol.PrefixTask)
	raw, err := json.Marshal(protocol.CreateIssueTaskPayload{
		TaskID: id, ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Start an owned app-server",
		Spec: protocol.OwnerTaskSpec{Goal: "Keep the original turn active until pause", Acceptance: []string{"The original turn remains active before pause"}, Deliverables: []string{domain.DeliveryReport}, Workspace: protocol.OwnerTaskWorkspace{Mode: domain.WorkspaceTemporary}},
	})
	if err != nil {
		t.Fatal(err)
	}
	env := &protocol.Envelope{RequestID: srv.ids.New(protocol.PrefixRequest), Operation: "issues.create_task",
		Target: protocol.Target{Type: "issue", ID: issue.IssueID}, ExpectedControlRevision: &issue.Revision, Payload: raw}
	if op, err := srv.app.Execute(t.Context(), owner, env, app.CanonicalOwnerTaskCreate{}); err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal(op, err)
	}
	return issue, id
}
