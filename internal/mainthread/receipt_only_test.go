package mainthread

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func receiptOnlySelection() *hostedconfig.Selection {
	return &hostedconfig.Selection{
		RunnerManaged: true, RunnerRunID: "receipt-reader-run", ReceiptOnly: true,
		Purpose:      agentconfig.IssueManager,
		Definition:   agentconfig.AgentDefinition{Model: "original-catalog-model", Effort: "high"},
		AppliedModel: "original-native-model", AppliedProvider: "original-provider", AppliedEffort: "high",
	}
}

type receiptOnlySession struct {
	harness.Session
	thread          harness.Thread
	resumeErr       error
	starts, resumes int
	config          harness.ThreadConfig
}

func (s *receiptOnlySession) StartThread(context.Context, harness.ThreadConfig) (harness.Thread, error) {
	s.starts++
	return nil, errors.New("receipt reader must not start a thread")
}

func (s *receiptOnlySession) ResumeThread(_ context.Context, id string, cfg harness.ThreadConfig) (harness.Thread, error) {
	s.resumes++
	s.config = cfg
	if s.resumeErr != nil {
		return nil, s.resumeErr
	}
	if s.thread == nil || id != s.thread.ID() {
		return nil, harness.ErrRejected
	}
	return s.thread, nil
}

func TestReceiptOnlyRequiresOriginalThreadAndAppliedModel(t *testing.T) {
	for _, missing := range []string{"thread", "model"} {
		t.Run(missing, func(t *testing.T) {
			e := newEnv(t, testkit.NewHarness())
			e.stop()
			e.m.named = receiptOnlySelection()
			e.m.cfg.MaintenanceEnabled = true
			if missing == "model" {
				e.m.named.AppliedModel = ""
				if err := e.app.SetMainThread(t.Context(), e.m.bindingInstance(), "original-thread"); err != nil {
					t.Fatal(err)
				}
			}
			session := &receiptOnlySession{}
			if _, err := e.m.openThread(t.Context(), session); err == nil {
				t.Fatal("incomplete original receipt identity was admitted")
			}
			if session.starts != 0 || session.resumes != 0 {
				t.Fatal("incomplete original identity reached native lifecycle", session.starts, session.resumes)
			}
		})
	}
}

func TestReceiptOnlyResumesOriginalWithoutToolsetHandoff(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		t.Run(map[bool]string{false: "old_toolset", true: "rejected_resume"}[rejected], func(t *testing.T) {
			e := newEnv(t, testkit.NewHarness())
			e.stop()
			e.m.named = receiptOnlySelection()
			e.m.cfg.MaintenanceEnabled = true
			if _, err := e.app.BindMainThread(t.Context(), e.m.bindingInstance(), "original-thread", "older-toolset", "retained-original.jsonl"); err != nil {
				t.Fatal(err)
			}
			original := &receiptThread{Thread: &historyReference{id: "original-thread", path: "retained-original.jsonl"}}
			session := &receiptOnlySession{thread: original}
			if rejected {
				session.resumeErr = harness.ErrRejected
			}
			thread, err := e.m.openThread(t.Context(), session)
			if rejected {
				if !errors.Is(err, harness.ErrRejected) {
					t.Fatal("original rejection was replaced by recovery or handoff", err)
				}
			} else if err != nil || thread != original {
				t.Fatal("receipt read did not preserve its original thread", thread, err)
			}
			if session.starts != 0 || session.resumes != 1 || len(e.h.Starts()) != 0 {
				t.Fatal("receipt attachment entered a new native execution", session.starts, session.resumes, e.h.Starts())
			}
			if session.config.Model != "original-native-model" || session.config.Config["model_provider"] != "original-provider" || session.config.Config["model_reasoning_effort"] != "high" {
				t.Fatal("receipt resume changed frozen model selection", session.config)
			}
			binding, found, err := e.app.MainThreadBinding(t.Context(), e.m.bindingInstance())
			if err != nil || !found || binding.ThreadID != "original-thread" || binding.ToolsetHash != "older-toolset" || e.count(`SELECT COUNT(*) FROM main_thread_handoffs`) != 0 {
				t.Fatal("receipt attachment replaced the original binding", binding, found, err)
			}
		})
	}
}

func TestReceiptOnlyRejectsFreshDeliveriesAndToolCalls(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	e.m.named = receiptOnlySelection()
	for _, op := range []string{app.OpHandleMessage, app.OpRecoverAgent, "conversation.compact", "conversation.handoff"} {
		result := e.m.Deliver(t.Context(), app.Delivery{Operation: op, Payload: []byte(`{}`)})
		if result.Outcome != app.Retry {
			t.Fatalf("receipt reader admitted %s: %+v", op, result)
		}
	}
	if result := e.m.onToolCall(t.Context(), harness.ToolCall{Tool: "repos_list", Arguments: []byte(`{}`)}); result.Success {
		t.Fatal("receipt reader executed a fresh native tool callback", result)
	}
	for _, op := range []string{"conversation.compact", "conversation.handoff"} {
		if result := e.m.Reconcile(t.Context(), app.Delivery{Operation: op, Payload: []byte(`{}`)}); result.Outcome != app.Unknown {
			t.Fatal("receipt reader resumed native maintenance", op, result)
		}
	}
}

func TestReceiptOnlySkipsOtherwiseDueIdleSummary(t *testing.T) {
	f := newHandoffFixture(t)
	seedIdleConversation(t, f)
	f.e.m.named = receiptOnlySelection()
	if _, err := f.e.app.BindMainThread(t.Context(), f.e.m.bindingInstance(), f.source.ID(), "older-toolset", rolloutPath(f.source)); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(time.Hour)
	if err := f.e.m.idleSummary(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, forks, _ := f.session.counts(); forks != 0 {
		t.Fatal("receipt attachment launched a due maintenance model", forks)
	}
}

func TestReceiptOnlyClearsPendingOnlyForOriginalTerminalHistory(t *testing.T) {
	for _, status := range []string{"missing", "inProgress", "completed", "interrupted", "failed"} {
		t.Run(status, func(t *testing.T) {
			e := newEnv(t, testkit.NewHarness())
			e.stop()
			message := e.say("original input")
			request := e.ids.New(protocol.PrefixRequest)
			const thread, turn = "original-thread", "original-turn"
			if err := e.app.PutMainTurn(t.Context(), store.MainTurn{RequestID: request, MessageID: message, ThreadID: thread, TurnID: turn, Status: store.TurnStarted}); err != nil {
				t.Fatal(err)
			}
			if err := e.app.SettleOpenTurns(t.Context(), thread); err != nil {
				t.Fatal(err)
			}
			e.m.named = receiptOnlySelection()
			reader := &receiptThread{Thread: &historyReference{id: thread}, found: status != "missing", receipt: harness.TurnReceipt{ID: turn, Status: status, Raw: []byte(`{"fixture":"retained-native-history"}`)}}
			if err := e.m.reconcileNativeReceipts(t.Context(), reader); err != nil {
				t.Fatal(err)
			}
			pending, err := e.app.NativeReceiptPending(t.Context(), request)
			terminal := status == "completed" || status == "interrupted" || status == "failed"
			if err != nil || pending == terminal || reader.known != turn || len(e.h.Starts()) != 0 {
				t.Fatal("receipt pending did not reflect exact terminal native history", pending, reader.known, err)
			}
		})
	}
}
