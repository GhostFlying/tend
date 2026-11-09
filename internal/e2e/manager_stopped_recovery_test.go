package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

type stoppedManagerNoNative struct {
	pin   protocol.RunExecutionPin
	opens atomic.Int64
}

func (n *stoppedManagerNoNative) Refresh(context.Context) error { return nil }
func (n *stoppedManagerNoNative) Observe(context.Context) ([]protocol.AgentExecutor, error) {
	return []protocol.AgentExecutor{{Pin: n.pin, Available: true}}, nil
}
func (n *stoppedManagerNoNative) Resolve(context.Context, protocol.RunExecutionPin) (runner.ResolvedExecution, error) {
	n.opens.Add(1)
	return runner.ResolvedExecution{}, errors.New("recovery cannot start a worker")
}
func (n *stoppedManagerNoNative) ResolveManager(context.Context, protocol.RunExecutionPin) (runner.ManagerExecution, error) {
	n.opens.Add(1)
	return runner.ManagerExecution{}, errors.New("completed close cannot reopen native execution")
}

type stoppedManagerRecoveryHTTP struct {
	t              *testing.T
	s              *server
	as             protocol.Assignment
	close, stopped protocol.Envelope
	business       string
	mu             sync.Mutex
	seen           []protocol.Envelope
}

func (n *stoppedManagerRecoveryHTTP) RoundTrip(q *http.Request) (*http.Response, error) {
	raw, err := io.ReadAll(q.Body)
	_ = q.Body.Close()
	if err != nil {
		return nil, err
	}
	q.Body = io.NopCloser(bytes.NewReader(raw))
	var env protocol.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, err
	}
	if env.Operation == protocol.OpManagerNativeStopped || env.Operation == protocol.OpManagerNativeReply {
		n.mu.Lock()
		n.seen = append(n.seen, env)
		n.mu.Unlock()
		want := n.close
		if env.Operation == protocol.OpManagerNativeStopped {
			want = n.stopped
		}
		if !reflect.DeepEqual(env, want) {
			n.t.Error("runner changed original factual envelope", env)
		}
	} else if env.Operation == protocol.OpManagerNativeClaim {
		n.t.Error("closed manager reopened native command admission")
	}
	response, err := http.DefaultTransport.RoundTrip(q)
	if err == nil && response.StatusCode == http.StatusOK && env.Operation == protocol.OpManagerNativeStopped {
		if n.s.count(`SELECT COUNT(*) FROM runs r JOIN leases l USING(run_id) WHERE r.run_id=? AND r.observed_state='failed' AND l.status='released'`, n.as.RunID) != 1 {
			n.t.Error("physical stop did not release the original server run")
		}
		if n.s.count(`SELECT COUNT(*) FROM native_turn_receipts_pending WHERE request_id=?`, n.business) != 1 {
			n.t.Error("physical stop fabricated a business completion")
		}
	}
	return response, err
}

func recoverStoppedManagerHTTP(t *testing.T, s *server, j *journal.Journal, as protocol.Assignment, runtime *protocol.RunnerRuntime, closeEnvelope, stopped protocol.Envelope, business, turn string) {
	t.Helper()
	ctx := t.Context()
	selection, named, err := s.app.HostedRuntimeSelection(ctx, as.Manager.ConversationID)
	if err != nil || !named {
		t.Fatal(selection, named, err)
	}
	selection.RunnerRunID, selection.ReceiptOnly = "", false
	selection.AppliedModel, selection.AppliedProvider, selection.AppliedEffort = "", "", ""
	binding, err := json.Marshal(selection)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(binding)
	if err := s.app.SetMainThread(ctx, "stopped-recovery/named/"+hex.EncodeToString(sum[:]), "original-native"); err != nil {
		t.Fatal(err)
	}
	if s.count(`SELECT COUNT(*) FROM manager_native_events WHERE run_id=? AND applied=0`, as.RunID) < 2 {
		t.Fatal("fixture lacks original unapplied repair events")
	}
	revision := s.task(as.TaskID).ControlRevision
	raw := mustClosedFactsJSON(t, map[string]any{})
	op, err := s.app.Execute(ctx, owner, &protocol.Envelope{RequestID: s.ids.New(protocol.PrefixRequest), Operation: "tasks.cancel", Target: protocol.Target{Type: "task", ID: as.TaskID}, ExpectedControlRevision: &revision, Payload: raw}, app.CancelTask{})
	if err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal(op, err)
	}
	if _, err := j.NextProcessGeneration(ctx); err != nil {
		t.Fatal(err)
	}
	transport := &stoppedManagerRecoveryHTTP{t: t, s: s, as: as, close: closeEnvelope, stopped: stopped, business: business}
	executions := &stoppedManagerNoNative{pin: *as.Execution}
	// Maintenance only prevents unrelated claims. The real Hello still admits
	// recovery of the exact stopped manager through the normal executor path.
	r := runner.New(runner.Config{ServerURL: s.url, HostID: "linux-1", HostToken: hostToken, Journal: j,
		HTTPClient: &http.Client{Transport: transport}, IDs: s.ids, Now: s.now, Log: runnerTestLog(t), Executions: executions,
		InitialMaintenance: true, UpgradeID: "receipt-recovery", RetryDelay: time.Millisecond,
		HeartbeatInterval: 20 * time.Millisecond, RequestTimeout: 3 * time.Second})
	run, cancel := context.WithCancel(ctx)
	var runErr error
	done := make(chan struct{})
	go func() { runErr = r.Run(run); close(done) }()
	stop := sync.OnceFunc(func() { cancel(); <-done })
	defer stop()
	eventually(t, "original stop and close recover through App HTTP", func() bool {
		select {
		case <-done:
			t.Fatal("recovery runner exited before original receipts settled", runErr)
		default:
		}
		_, err := j.Run(ctx, as.RunID)
		return errors.Is(err, journal.ErrNotFound)
	})
	stop()
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		t.Fatal(runErr)
	}
	if executions.opens.Load() != 0 {
		t.Fatal("physical receipt recovery attempted another native execution")
	}
	transport.mu.Lock()
	seen := append([]protocol.Envelope(nil), transport.seen...)
	transport.mu.Unlock()
	if len(seen) != 2 || !reflect.DeepEqual(seen[0], stopped) || !reflect.DeepEqual(seen[1], closeEnvelope) {
		t.Fatal("original close blocked physical stop or changed its identity", seen)
	}
	if s.count(`SELECT COUNT(*) FROM manager_native_requests WHERE request_id=? AND run_id=? AND status='completed' AND json_extract(result_json,'$.last_sequence')=3 AND COALESCE(json_extract(result_json,'$.error'),'')=''`, closeResultID(t, closeEnvelope), as.RunID) != 1 || s.count(`SELECT COUNT(*) FROM manager_native_events WHERE run_id=? AND applied=0`, as.RunID) != 0 {
		t.Fatal("terminal run rejected its original close or lost original event application")
	}
	if s.count(`SELECT COUNT(*) FROM native_turn_receipts_pending WHERE request_id=?`, business) != 1 || s.count(`SELECT COUNT(*) FROM runs`) != 1 {
		t.Fatal("transport recovery completed unknown business or created another run")
	}
	// Claim only the original manager Task's automatically derived receipt-only
	// action. Its fake native history can read the original turn, never run it.
	runtime.InstanceID, runtime.Generation, runtime.Sequence = "original-receipt-reader", 3, 1
	s.exec(protocol.Principal{Kind: protocol.PrincipalRunner, Subject: "linux-1"}, protocol.OpRunnerHello, protocol.Target{Type: "host", ID: "linux-1"}, protocol.HelloPayload{Runtime: runtime, WorkspaceProtocol: 1}, app.Hello{})
	claimed := s.exec(protocol.Principal{Kind: protocol.PrincipalRunner, Subject: "linux-1"}, protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{Runtime: runtime, WorkspaceProtocol: 1}, app.Claim{})
	var next struct{ Assignment *protocol.Assignment }
	if err := json.Unmarshal(claimed.Result, &next); err != nil || next.Assignment == nil || next.Assignment.TaskID != as.TaskID || next.Assignment.RunID == as.RunID || next.Assignment.Manager == nil || !next.Assignment.Manager.ReceiptOnly {
		t.Fatal("original receipt-only transport was not derived after physical release", claimed, err)
	}
	driveManagerReceiptHistory(t, s, &as, next.Assignment, business, "stopped-recovery", turn, "completed")
	if s.count(`SELECT COUNT(*) FROM tasks`) != 1 || s.count(`SELECT COUNT(*) FROM main_turns`) != 2 || s.count(`SELECT COUNT(*) FROM manager_native_events WHERE run_id=?`, as.RunID) != 3 {
		t.Fatal("receipt recovery rebuilt business work or changed original event history")
	}
}

func closeResultID(t *testing.T, envelope protocol.Envelope) string {
	t.Helper()
	var reply protocol.ManagerNativeReply
	if err := json.Unmarshal(envelope.Payload, &reply); err != nil {
		t.Fatal(err)
	}
	return reply.ID
}
