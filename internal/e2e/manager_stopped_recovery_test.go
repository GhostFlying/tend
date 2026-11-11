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
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

type stoppedManagerNoNative struct {
	pin    protocol.RunExecutionPin
	opens  atomic.Int64
	normal bool
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
	if n.normal {
		return runner.ManagerExecution{Profile: n.pin.Profile, Open: func(ctx context.Context, _ string) (harness.Session, error) {
			n.opens.Add(1)
			return testkit.NewHarness().Open(ctx)
		}}, nil
	}
	n.opens.Add(1)
	return runner.ManagerExecution{}, errors.New("completed close cannot reopen native execution")
}

type stoppedManagerRecoveryHTTP struct {
	t               *testing.T
	s               *server
	as              protocol.Assignment
	close, stopped  protocol.Envelope
	business        string
	settled         bool
	normal          bool
	stoppedAccepted bool
	claims          int
	mu              sync.Mutex
	seen            []protocol.Envelope
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
		if n.normal {
			n.mu.Lock()
			want := &n.close
			if env.Operation == protocol.OpManagerNativeStopped {
				want = &n.stopped
			}
			if want.RequestID == "" {
				*want = env
			}
			if !sameClosedFactsEnvelope(env, *want) {
				n.t.Error("normal recovery changed original envelope", env)
			}
			gotHash, hashErr := protocol.PayloadHash(&env)
			wantHash, wantErr := protocol.PayloadHash(want)
			if hashErr != nil || wantErr != nil || gotHash != wantHash {
				n.t.Error("normal recovery changed original payload hash", hashErr, wantErr)
			}
			if env.Operation == protocol.OpManagerNativeStopped {
				var p protocol.ManagerStoppedPayload
				if err := json.Unmarshal(env.Payload, &p); err != nil || p.LastSequence != 3 {
					n.t.Error("physical stop changed original event cursor", p, err)
				}
			}
			pending := env.Operation == protocol.OpManagerNativeReply && !n.stoppedAccepted
			n.mu.Unlock()
			if pending {
				// A completed native operation has not yet been accepted by the
				// server. Never synthesize success; real App handles both facts
				// after the runner's independently confirmed physical stop.
				body, _ := json.Marshal(map[string]any{"error": protocol.Errorf(protocol.CodeUnknownOutcome, "native close must cover all applied events")})
				return &http.Response{StatusCode: http.StatusConflict, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), Request: q}, nil
			}
		} else {
			want := n.close
			if env.Operation == protocol.OpManagerNativeStopped {
				want = n.stopped
			}
			if !sameClosedFactsEnvelope(env, want) {
				n.t.Error("runner changed original factual envelope", env)
			}
		}
	} else if env.Operation == protocol.OpManagerNativeClaim {
		var claim protocol.ManagerNativeClaimPayload
		if json.Unmarshal(env.Payload, &claim) == nil && claim.AcknowledgeOnly && len(claim.AcknowledgedIDs) > 0 {
			return http.DefaultTransport.RoundTrip(q)
		}
		if !n.normal {
			n.t.Error("closed manager reopened native command admission")
		}
		n.mu.Lock()
		n.claims++
		n.mu.Unlock()
	}
	response, err := http.DefaultTransport.RoundTrip(q)
	if err == nil && response.StatusCode == http.StatusOK && env.Operation == protocol.OpManagerNativeStopped {
		n.mu.Lock()
		n.stoppedAccepted = true
		n.mu.Unlock()
		if n.s.count(`SELECT COUNT(*) FROM runs r JOIN leases l USING(run_id) WHERE r.run_id=? AND r.observed_state='failed' AND l.status='released'`, n.as.RunID) != 1 {
			n.t.Error("physical stop did not release the original server run")
		}
		wantPending := 1
		if n.settled {
			wantPending = 0
		}
		if n.s.count(`SELECT COUNT(*) FROM native_turn_receipts_pending WHERE request_id=?`, n.business) != wantPending {
			n.t.Error("physical stop fabricated a business completion")
		}
	}
	return response, err
}

func recoverStoppedManagerHTTP(t *testing.T, s *server, j *journal.Journal, as protocol.Assignment, runtime *protocol.RunnerRuntime, closeEnvelope, stopped protocol.Envelope, business, turn string, options closedManagerRecoveryOptions) {
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
	if options.settled {
		var original store.MainTurn
		if err := s.store.Read(ctx, func(tx *store.ReadTx) error {
			var err error
			original, err = store.GetMainTurn(ctx, tx.Q(), business)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		original.NativeReconciled = true
		if err := s.app.PutMainTurn(ctx, original); err != nil {
			t.Fatal(err)
		}
	}
	if !options.normal {
		revision := s.task(as.TaskID).ControlRevision
		raw := mustClosedFactsJSON(t, map[string]any{})
		op, err := s.app.Execute(ctx, owner, &protocol.Envelope{RequestID: s.ids.New(protocol.PrefixRequest), Operation: "tasks.cancel", Target: protocol.Target{Type: "task", ID: as.TaskID}, ExpectedControlRevision: &revision, Payload: raw}, app.CancelTask{})
		if err != nil || op.Status != protocol.OpSucceeded {
			t.Fatal(op, err)
		}
	}
	if _, err := j.NextProcessGeneration(ctx); err != nil {
		t.Fatal(err)
	}
	if options.bounded {
		data, err := j.BoundedDataIdentity(ctx)
		if err != nil {
			t.Fatal(err)
		}
		authority := protocol.BoundedMaintenanceAuthorization{RequestID: "original-bounded-recovery", Role: "runner", DataIdentity: &data, Target: protocol.UpgradeBinding{ProcessInstanceID: "replacement-facts-only"}}
		if err := j.QuarantineBoundedRecovery(ctx, authority); err != nil {
			t.Fatal(err)
		}
	}
	transport := &stoppedManagerRecoveryHTTP{t: t, s: s, as: as, close: closeEnvelope, stopped: stopped, business: business, settled: options.settled, normal: options.normal}
	executions := &stoppedManagerNoNative{pin: *as.Execution, normal: options.normal}
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
	if options.normal && run.Err() != nil {
		t.Fatal("normal deadline required external cancellation", run.Err())
	}
	stop()
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		t.Fatal(runErr)
	}
	wantOpens := int64(0)
	if options.normal {
		wantOpens = 1
	}
	if executions.opens.Load() != wantOpens {
		t.Fatal("physical receipt recovery changed native execution count", executions.opens.Load())
	}
	transport.mu.Lock()
	seen := append([]protocol.Envelope(nil), transport.seen...)
	transport.mu.Unlock()
	if options.normal {
		if len(seen) < 3 || seen[0].Operation != protocol.OpManagerNativeReply || !transport.stoppedAccepted || transport.claims != 1 || !sameClosedFactsEnvelope(seen[len(seen)-1], transport.close) {
			t.Fatal("normal deadline did not preserve its original close across actual physical stop", seen)
		}
		closeEnvelope = transport.close
		if t.Context().Err() != nil {
			t.Fatal("normal recovery required external cancellation")
		}
	} else if len(seen) != 2 || !sameClosedFactsEnvelope(seen[0], stopped) || !sameClosedFactsEnvelope(seen[1], closeEnvelope) {
		t.Fatal("original close blocked physical stop or changed its identity", seen)
	}
	if s.count(`SELECT COUNT(*) FROM manager_native_requests WHERE request_id=? AND run_id=? AND status='completed' AND json_extract(result_json,'$.last_sequence')=3 AND COALESCE(json_extract(result_json,'$.error'),'')=''`, closeResultID(t, closeEnvelope), as.RunID) != 1 || s.count(`SELECT COUNT(*) FROM manager_native_events WHERE run_id=? AND applied=0`, as.RunID) != 0 {
		t.Fatal("terminal run rejected its original close or lost original event application")
	}
	wantPending := 1
	if options.settled {
		wantPending = 0
	}
	if s.count(`SELECT COUNT(*) FROM native_turn_receipts_pending WHERE request_id=?`, business) != wantPending || s.count(`SELECT COUNT(*) FROM runs`) != 1 {
		t.Fatal("transport recovery completed unknown business or created another run")
	}
	if options.settled {
		return
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

func sameClosedFactsEnvelope(a, b protocol.Envelope) bool {
	first, err := protocol.PayloadHash(&a)
	second, otherErr := protocol.PayloadHash(&b)
	if err != nil || otherErr != nil || first != second {
		return false
	}
	left, right := string(a.Payload), string(b.Payload)
	if left != right && !store.SemanticJSONEqual(left, right) {
		return false
	}
	a.Payload, b.Payload = nil, nil
	return reflect.DeepEqual(a, b)
}

func closeResultID(t *testing.T, envelope protocol.Envelope) string {
	t.Helper()
	var reply protocol.ManagerNativeReply
	if err := json.Unmarshal(envelope.Payload, &reply); err != nil {
		t.Fatal(err)
	}
	return reply.ID
}
