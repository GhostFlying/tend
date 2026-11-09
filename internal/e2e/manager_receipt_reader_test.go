package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/contextbuilder"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/coordination"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/mainthread"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/tools"
)

type receiptHistorySession struct {
	thread  *receiptHistoryThread
	done    chan struct{}
	once    sync.Once
	resumes atomic.Int64
}

func (s *receiptHistorySession) StartThread(context.Context, harness.ThreadConfig) (harness.Thread, error) {
	return nil, errors.New("receipt reader cannot create a thread")
}
func (s *receiptHistorySession) Models(context.Context) ([]harness.Model, error) {
	return nil, errors.New("current catalog must not be consulted for original receipts")
}
func (s *receiptHistorySession) ResumeThread(_ context.Context, id string, cfg harness.ThreadConfig) (harness.Thread, error) {
	if id != "original-native" || cfg.Model != "frozen-model" {
		return nil, errors.New("receipt reader changed original thread/model")
	}
	s.resumes.Add(1)
	return s.thread, nil
}
func (s *receiptHistorySession) Done() <-chan struct{} { return s.done }
func (s *receiptHistorySession) Close() error {
	s.once.Do(func() { close(s.thread.events); close(s.done) })
	return nil
}

type receiptHistoryThread struct {
	harness.Thread
	events                 chan harness.Event
	business, status, turn string
	reads                  atomic.Int64
}

func (t *receiptHistoryThread) ID() string                   { return "original-native" }
func (t *receiptHistoryThread) Events() <-chan harness.Event { return t.events }
func (t *receiptHistoryThread) PermissionDefaults() harness.PermissionSettings {
	return harness.PermissionSettings{Sandbox: "read-only", ApprovalPolicy: "never"}
}
func (t *receiptHistoryThread) ModelDefaults() harness.ModelSelection {
	return harness.ModelSelection{Model: "frozen-model"}
}
func (t *receiptHistoryThread) FindTurn(_ context.Context, identity, turn string) (harness.TurnReceipt, bool, error) {
	if identity != t.business || turn != t.turn {
		return harness.TurnReceipt{}, false, errors.New("receipt reader changed original repair execution")
	}
	t.reads.Add(1)
	return harness.TurnReceipt{ID: turn, Status: t.status, Items: []harness.Event{}, Raw: json.RawMessage(fmt.Sprintf(`{"id":%q,"status":%q,"items":[]}`, t.turn, t.status))}, true, nil
}

// Exercise App admission, a real runner transport and BackgroundPool against a
// synthetic native history reader. The fixture never asserts a live terminal.
func TestFault_ProvedCloseReaderReconcilesOnlyRealTerminalHistory(t *testing.T) {
	for _, status := range []string{"completed", "inProgress"} {
		t.Run(status, func(t *testing.T) {
			f := lapsedManagerReplayCommitsOriginalClose(t, true, true)
			s, old := f.server, f.assignment
			ctx := t.Context()
			proof, err := s.app.ManagerRunReconciliation(ctx, old.TaskID, old.RunID)
			if err != nil {
				t.Fatal(err)
			}
			payload := protocol.ReconcileManagerRunPayload{Mode: "read_receipts", RunID: old.RunID, LeaseID: proof.LeaseID, CloseRequestID: proof.CloseRequestID, CloseResultSHA256: proof.CloseResultSHA256, StoppedRequestID: proof.StoppedRequestID, StoppedPayloadSHA256: proof.StoppedPayloadSHA256, ExpectedAssignmentEpoch: proof.AssignmentEpoch, Reason: "Read the exact original native history without repeating its work"}
			body, _ := json.Marshal(payload)
			op, err := s.app.Execute(ctx, owner, &protocol.Envelope{RequestID: s.ids.New(protocol.PrefixRequest), Operation: protocol.OpReconcileManagerRun, Target: protocol.Target{Type: "task", ID: old.TaskID}, ExpectedControlRevision: &proof.TaskRevision, Payload: body}, app.ReconcileManagerRun{})
			if err != nil || op.Status != protocol.OpSucceeded {
				t.Fatal(op, err)
			}
			claimed := s.exec(protocol.Principal{Kind: protocol.PrincipalRunner, Subject: "linux-1"}, protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{Runtime: f.runtime, WorkspaceProtocol: 1}, app.Claim{})
			var result struct{ Assignment *protocol.Assignment }
			if err := json.Unmarshal(claimed.Result, &result); err != nil || result.Assignment == nil {
				t.Fatal(claimed, err)
			}
			reader := result.Assignment
			if reader.TaskID != old.TaskID || reader.RunID == old.RunID || reader.Manager == nil || !reader.Manager.ReceiptOnly || !reader.Manager.RequireReceiptReader {
				t.Fatal("new transport is not the original responsibility's read-only reader", reader)
			}
			driveManagerReceiptHistory(t, s, old, reader, f.business, f.instance, "original-turn", status)
			if s.count(`SELECT COUNT(*) FROM manager_native_events WHERE run_id=?`, old.RunID) != 19 || s.count(`SELECT COUNT(*) FROM conversation_agent_messages WHERE thread_id='original-native' AND turn_id='original-turn'`) != 0 {
				t.Fatal("receipt reading changed the old cursor or published repair output")
			}
			if err := s.store.Read(ctx, func(tx *store.ReadTx) error {
				var raw string
				if err := tx.Q().QueryRowContext(ctx, `SELECT result_json FROM manager_native_requests WHERE run_id=? AND method='close'`, old.RunID).Scan(&raw); err != nil {
					return err
				}
				var close protocol.ManagerNativeReply
				if err := json.Unmarshal([]byte(raw), &close); err != nil {
					return err
				}
				if close.ID != proof.CloseRequestID || close.LastSequence != 19 || close.Error != "" {
					t.Fatal("reader changed the original frozen close", close)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func driveManagerReceiptHistory(t *testing.T, s *server, old, reader *protocol.Assignment, business, instance, turn, status string) {
	t.Helper()
	ctx := t.Context()
	native := &receiptHistorySession{thread: &receiptHistoryThread{events: make(chan harness.Event), business: business, status: status, turn: turn}, done: make(chan struct{})}
	run, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	var sequence atomic.Int64
	var forbidden atomic.Int64
	principal := protocol.Principal{Kind: protocol.PrincipalRun, Subject: reader.RunID, Role: reader.Role}
	report := func(op string, p any) error {
		raw, _ := json.Marshal(p)
		res, err := s.app.Execute(run, principal, &protocol.Envelope{RequestID: s.ids.New(protocol.PrefixRequest), Operation: op, Target: protocol.Target{Type: "run", ID: reader.RunID}, Payload: raw}, app.ManagerNativeCommand{OperationName: op})
		if err != nil {
			return err
		}
		if res.Error != nil {
			return res.Error
		}
		return nil
	}
	driver := &coordination.Driver{Session: native, Profile: reader.Execution.Profile, RequireReceiptReader: reader.Manager.RequireReceiptReader, Emit: func(_ context.Context, e coordination.Event) error {
		raw, _ := json.Marshal(e)
		return report(protocol.OpManagerNativeEvent, protocol.ManagerNativeEvent{Sequence: sequence.Add(1), Body: raw})
	}}
	wg.Go(func() {
		for run.Err() == nil {
			raw, _ := json.Marshal(map[string]any{})
			op, err := s.app.Execute(run, principal, &protocol.Envelope{RequestID: s.ids.New(protocol.PrefixRequest), Operation: protocol.OpManagerNativeClaim, Target: protocol.Target{Type: "run", ID: reader.RunID}, Payload: raw}, app.ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim})
			if err != nil || op.Error != nil {
				if run.Err() == nil {
					t.Error("native reader claim", op, err)
				}
				return
			}
			var request protocol.ManagerNativeRequest
			if err := json.Unmarshal(op.Result, &request); err != nil {
				t.Error(err)
				return
			}
			if request.ID == "" {
				select {
				case <-run.Done():
					return
				case <-time.After(5 * time.Millisecond):
				}
				continue
			}
			if request.Method != "resume" && request.Method != "find" && request.Method != "usage" && request.Method != "close" {
				forbidden.Add(1)
			}
			value, handleErr := driver.Handle(run, request.Method, request.Payload)
			reply := protocol.ManagerNativeReply{ID: request.ID, LastSequence: sequence.Load()}
			if handleErr != nil {
				reply.Error = handleErr.Error()
				reply.Rejected = errors.Is(handleErr, harness.ErrRejected)
			} else {
				reply.Result, _ = json.Marshal(value)
			}
			if err := report(protocol.OpManagerNativeReply, reply); err != nil {
				if run.Err() == nil {
					t.Error(err)
				}
				return
			}
			if request.Method == "close" {
				return
			}
		}
	})
	cb := contextbuilder.New(s.app, s.store, s.ids, contextbuilder.Config{})
	pool := &mainthread.BackgroundPool{App: s.app, New: func(id string) (*mainthread.Manager, error) {
		if id != old.Manager.ConversationID {
			return nil, errors.New("unrelated receipt reader")
		}
		return mainthread.New(s.app, cb, tools.New(s.app, s.store, cb, log), mainthread.Config{Instance: instance, ConversationID: id, Log: log, RestartDelay: time.Millisecond, OpenNamed: func(c context.Context, selection hostedconfig.Selection) (harness.Session, error) {
			if !selection.ReceiptOnly || selection.RequireReceiptReader != reader.Manager.RequireReceiptReader || selection.RunnerRunID != reader.RunID {
				return nil, errors.New("wrong receipt-only transport")
			}
			return mainthread.RunnerSession(c, s.app, reader.RunID), nil
		}}), nil
	}}
	wg.Go(func() { pool.Run(run) })
	defer func() {
		cancel()
		wg.Wait()
		if t.Failed() {
			t.Logf("reader resumes=%d reads=%d forbidden=%d logs:\n%s", native.resumes.Load(), native.thread.reads.Load(), forbidden.Load(), logs.String())
		}
	}()
	eventually(t, "original history was read", func() bool { return native.thread.reads.Load() > 0 })
	if status == "completed" {
		eventually(t, "actual terminal receipt clears original pending", func() bool {
			return s.count(`SELECT COUNT(*) FROM native_turn_receipts_pending WHERE request_id=?`, business) == 0
		})
	} else if s.count(`SELECT COUNT(*) FROM native_turn_receipts_pending WHERE request_id=?`, business) != 1 {
		t.Fatal("nonterminal history was fabricated into a business terminal")
	}
	cancel()
	wg.Wait()
	if forbidden.Load() != 0 || native.resumes.Load() != 1 {
		t.Fatal("receipt reader started new work or changed original thread", forbidden.Load(), native.resumes.Load())
	}
}
