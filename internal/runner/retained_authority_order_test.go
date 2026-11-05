package runner

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

func TestRetainedAuthorityRejectedRunDoesNotAbortHostStartup(t *testing.T) {
	r, as, _ := retainedStrictFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := r.cfg.Journal.SetPhase(ctx, as.RunID, journal.PhaseReleasing); err != nil {
		t.Fatal(err)
	}
	before, requests := retainedStrictSnapshot(t, r.cfg.Journal, as.RunID)
	other := as
	other.RunID, other.TaskID, other.LeaseID = "other-run", "other-task", "other-lease"
	if err := r.cfg.Journal.AcceptAssignmentFromWorker(ctx, other, "", protocol.WorkerOrca); err != nil {
		t.Fatal(err)
	}
	otherRequest := protocol.Envelope{RequestID: "other-final-request", Operation: protocol.OpRunsRelease,
		Target: protocol.Target{Type: "run", ID: other.RunID}, Payload: json.RawMessage(`{"reason":"other completed worker"}`)}
	if err := r.cfg.Journal.Finish(ctx, other.RunID, journal.PhaseReleasing, journal.Request{RunID: other.RunID, Envelope: otherRequest}); err != nil {
		t.Fatal(err)
	}
	if err := r.cfg.Journal.ConfirmStopped(ctx, other.RunID); err != nil {
		t.Fatal(err)
	}
	claims := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var env protocol.Envelope
		if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
			t.Error(err)
			return
		}
		var result any = map[string]any{}
		switch env.Operation {
		case protocol.OpRunnerHello:
			result = protocol.HelloResult{LeaseTTLMillis: 60000, Runs: []protocol.RunVerdict{
				{RunID: as.RunID, Verdict: protocol.VerdictContinueRetained, Desired: protocol.DesiredRun,
					RetainedResume: &protocol.RetainedRunResume{RequestID: "attach-without-original-result", Mode: "attach"}},
				{RunID: other.RunID, Verdict: protocol.VerdictContinue, Desired: protocol.DesiredRun},
			}}
		case protocol.OpRunnerClaim:
			once.Do(func() { close(claims) })
			<-q.Context().Done()
			return
		case protocol.OpRunnerHeartbeat:
			result = protocol.HeartbeatResult{LeaseTTLMillis: 60000}
		case protocol.OpRunsRelease:
			if !reflect.DeepEqual(env, otherRequest) {
				t.Error("rejected run sent business or other request changed", env)
			}
		default:
			t.Errorf("unexpected startup operation %s", env.Operation)
		}
		raw, _ := json.Marshal(result)
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: raw}})
	}))
	defer server.Close()
	r.c.base, r.c.http = server.URL, server.Client()
	r.cfg.RetryDelay, r.cfg.HeartbeatInterval = time.Millisecond, time.Hour
	r.cfg.Workers = &retainedStrictWorkerProbe{}
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	select {
	case <-claims:
	case err := <-done:
		t.Fatal("one rejected strict journal aborted the whole host", err)
	case <-ctx.Done():
		t.Fatal("host never reached its ordinary claim loop")
	}
	for {
		otherSaved, err := r.cfg.Journal.HistoricalRun(ctx, other.RunID)
		if err == nil && otherSaved.Phase == journal.PhaseDone {
			break
		}
		select {
		case err := <-done:
			t.Fatal("host exited before applying another run's verdict", err)
		case <-ctx.Done():
			t.Fatal("other run was blocked by rejected strict journal")
		case <-time.After(time.Millisecond):
		}
	}
	after, err := r.cfg.Journal.Run(ctx, as.RunID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("rejected journal identity/fence changed", after, err)
	}
	pending, err := r.cfg.Journal.Requests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var original []journal.Request
	for _, p := range pending {
		if p.RunID == as.RunID {
			original = append(original, p)
		}
	}
	if !reflect.DeepEqual(original, requests) {
		t.Fatal("rejection discarded the original pending request", original)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("host did not remain alive until explicitly canceled", err)
	}
}

type retainedAuthorityBlockingWorkspace struct {
	*retainedStrictWorkspaceProbe
	entered chan struct{}
	release chan struct{}
}

func (w retainedAuthorityBlockingWorkspace) Finish(ctx context.Context, _ protocol.Assignment) error {
	w.finishes.Add(1)
	close(w.entered)
	select {
	case <-w.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func retainedAuthorityStopFixture(t *testing.T) (*Runner, protocol.Assignment, string, protocol.RetainedRunResume, *active) {
	t.Helper()
	r, as, path := retainedStrictFixture(t)
	ctx := t.Context()
	if err := r.cfg.Journal.SetFenced(ctx, as.RunID, false); err != nil {
		t.Fatal(err)
	}
	authority := protocol.RetainedRunResume{RequestID: "original-attach", Mode: "attach"}
	if err := r.cfg.Journal.SetRetainedResume(ctx, as.RunID, authority); err != nil {
		t.Fatal(err)
	}
	result, _ := json.Marshal(harness.WorkerResult{Summary: "original strictly observed result"})
	if err := r.cfg.Journal.WorkerDone(ctx, as.RunID, result); err != nil {
		t.Fatal(err)
	}
	if err := r.cfg.Journal.SetPhase(ctx, as.RunID, journal.PhaseReleasing); err != nil {
		t.Fatal(err)
	}
	if err := r.cfg.Journal.ConfirmStopped(ctx, as.RunID); err != nil {
		t.Fatal(err)
	}
	if err := r.cfg.Journal.AuthorizeRetainedStop(ctx, as.RunID, "original-host-stop"); err != nil {
		t.Fatal(err)
	}
	retainedStrictOriginalEvent(t, r, as.RunID)
	runCtx, cancel := context.WithCancelCause(ctx)
	t.Cleanup(func() { cancel(nil) })
	a := &active{as: as, ctx: runCtx, cancel: cancel, desired: protocol.DesiredRun, resume: make(chan struct{}, 1)}
	r.active[as.RunID] = a
	return r, as, path, authority, a
}

func TestRetainedAuthorityContinueCannotRevokeStopDuringCleanup(t *testing.T) {
	r, as, path, authority, _ := retainedAuthorityStopFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	spaces := retainedAuthorityBlockingWorkspace{retainedStrictWorkspaceProbe: &retainedStrictWorkspaceProbe{}, entered: make(chan struct{}), release: make(chan struct{})}
	r.cfg.Workspaces = spaces
	report := make(chan error, 1)
	go func() { report <- r.report(ctx, as) }()
	select {
	case <-spaces.entered:
	case <-ctx.Done():
		t.Fatal("Stop cleanup did not acquire its settlement gate")
	}
	wire := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var env protocol.Envelope
		_ = json.NewDecoder(q.Body).Decode(&env)
		if env.Operation != protocol.OpRunnerHello {
			t.Errorf("unexpected operation %s", env.Operation)
		}
		close(wire)
		raw, _ := json.Marshal(protocol.HelloResult{LeaseTTLMillis: 60000, Runs: []protocol.RunVerdict{{RunID: as.RunID, Verdict: protocol.VerdictContinueRetained, Desired: protocol.DesiredRun, RetainedResume: &authority}}})
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: raw}})
	}))
	defer server.Close()
	r.c.base, r.c.http = server.URL, server.Client()
	hello := make(chan error, 1)
	go func() { hello <- r.freshHello(ctx) }()
	select {
	case <-wire:
	case <-ctx.Done():
		t.Fatal("concurrent continuation did not reach the wire")
	}
	// Keep actual cleanup paused while the concurrent Hello attempts to write
	// its authority. It must wait at the same gate, before clearing Stop.
	revoked := false
	deadline := time.NewTimer(100 * time.Millisecond)
wait:
	for {
		proof, err := r.cfg.Journal.RetainedStop(ctx, as.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if proof == nil {
			revoked = true
			break
		}
		select {
		case <-deadline.C:
			break wait
		case <-time.After(time.Millisecond):
		}
	}
	deadline.Stop()
	close(spaces.release)
	if err := <-report; err != nil {
		t.Fatal("authorized cleanup failed", err)
	}
	if err := <-hello; err != nil {
		t.Fatal("concurrent completed journal aborted Hello", err)
	}
	if revoked {
		t.Error("new authority cleared Stop outside the settlement gate")
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var raw string
	if err := db.QueryRowContext(ctx, `SELECT authority_json FROM retained_run_resumes WHERE run_id=?`, as.RunID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, "stop_history") || !strings.Contains(raw, "original-business-request") || !strings.Contains(raw, "original-native-event") {
		t.Fatal("late continuation discarded the independently stopped pending history", raw)
	}
	saved, err := r.cfg.Journal.HistoricalRun(ctx, as.RunID)
	if err != nil || saved.Phase != journal.PhaseDone || saved.Thread != "original-thread" || spaces.finishes.Load() != 1 {
		t.Fatal("cleanup did not settle its original journal exactly once", saved, err)
	}
}

func TestRetainedAuthorityRevocationWinsBeforeCleanup(t *testing.T) {
	for _, newAuthority := range []bool{false, true} {
		t.Run(map[bool]string{false: "continue", true: "new-authority"}[newAuthority], func(t *testing.T) {
			r, as, _, _, _ := retainedAuthorityStopFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			before, requests := retainedStrictSnapshot(t, r.cfg.Journal, as.RunID)
			events, err := r.cfg.Journal.PendingEvents(ctx, as.RunID, 100)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(rpcResponse{Error: protocol.Errorf(protocol.CodeForbidden, "original credential ended")})
			}))
			defer server.Close()
			r.c.base, r.c.http = server.URL, server.Client()
			entered, release := make(chan struct{}), make(chan struct{})
			control := make(chan error, 1)
			go func() {
				control <- r.authorizeRetainedControl(ctx, as.RunID, true, func() error {
					close(entered)
					<-release
					if newAuthority {
						return r.cfg.Journal.SetRetainedResume(ctx, as.RunID, protocol.RetainedRunResume{RequestID: "new-attach", Mode: "attach"})
					}
					return r.cfg.Journal.ClearRetainedStop(ctx, as.RunID)
				})
			}()
			<-entered
			report := make(chan error, 1)
			go func() { report <- r.report(ctx, as) }()
			// The reporting goroutine observes the still-valid old Stop while
			// the real control callback holds its gate. Revoke it before letting
			// the pending cleanup acquire that gate.
			time.Sleep(50 * time.Millisecond)
			close(release)
			if err := <-control; err != nil {
				t.Fatal(err)
			}
			if err := <-report; !isCompletionPending(err) {
				t.Fatal("revoked Stop still authorized cleanup", err)
			}
			after, pending := retainedStrictSnapshot(t, r.cfg.Journal, as.RunID)
			remaining, err := r.cfg.Journal.PendingEvents(ctx, as.RunID, 100)
			spaces := r.cfg.Workspaces.(*retainedStrictWorkspaceProbe)
			if err != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(requests, pending) || !reflect.DeepEqual(events, remaining) || spaces.finishes.Load() != 0 {
				t.Fatal("revocation lost original facts or cleanup authority", after.Phase, spaces.finishes.Load(), err)
			}
		})
	}
}
