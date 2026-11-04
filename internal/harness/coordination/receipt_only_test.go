package coordination

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

type receiptNativeSession struct {
	models, starts, resumes int
	thread                  *receiptNativeThread
	done                    chan struct{}
	once                    sync.Once
}

func (s *receiptNativeSession) Models(context.Context) ([]harness.Model, error) {
	s.models++
	return nil, errors.New("current model catalog is unavailable")
}
func (s *receiptNativeSession) StartThread(context.Context, harness.ThreadConfig) (harness.Thread, error) {
	s.starts++
	return nil, errors.New("new thread is forbidden")
}
func (s *receiptNativeSession) ResumeThread(_ context.Context, id string, cfg harness.ThreadConfig) (harness.Thread, error) {
	s.resumes++
	if id != "original-thread" {
		return nil, harness.ErrRejected
	}
	s.thread.config = cfg
	return s.thread, nil
}
func (s *receiptNativeSession) Done() <-chan struct{} { return s.done }
func (s *receiptNativeSession) Close() error {
	s.once.Do(func() { close(s.thread.events); close(s.done) })
	return nil
}

type receiptNativeThread struct {
	config harness.ThreadConfig
	events chan harness.Event
	reads  int
}

func (*receiptNativeThread) ID() string { return "original-thread" }
func (*receiptNativeThread) InjectDeveloper(context.Context, ...string) error {
	return errors.New("receipt reader attempted injection")
}
func (*receiptNativeThread) StartTurn(context.Context, harness.TurnInput) (string, error) {
	return "", errors.New("receipt reader attempted a new turn")
}
func (*receiptNativeThread) Interrupt(context.Context, string) error {
	return errors.New("receipt reader attempted an interrupt")
}
func (t *receiptNativeThread) Events() <-chan harness.Event { return t.events }
func (t *receiptNativeThread) ModelDefaults() harness.ModelSelection {
	provider, _ := t.config.Config["model_provider"].(string)
	effort, _ := t.config.Config["model_reasoning_effort"].(string)
	return harness.ModelSelection{Model: t.config.Model, Provider: provider, Effort: effort}
}
func (t *receiptNativeThread) PermissionDefaults() harness.PermissionSettings {
	return harness.PermissionSettings{Sandbox: t.config.Sandbox, ApprovalPolicy: t.config.ApprovalPolicy}
}
func (t *receiptNativeThread) FindTurn(_ context.Context, identity, turn string) (harness.TurnReceipt, bool, error) {
	if identity != "original-repair" || turn != "original-turn" {
		return harness.TurnReceipt{}, false, errors.New("original repair identity changed")
	}
	t.reads++
	return harness.TurnReceipt{ID: turn, Status: "completed", Raw: json.RawMessage(`{"id":"original-turn","status":"completed"}`)}, true, nil
}

func TestReceiptOnlyDriverPreservesAppliedConfigurationWithoutCatalog(t *testing.T) {
	native := &receiptNativeSession{thread: &receiptNativeThread{events: make(chan harness.Event)}, done: make(chan struct{})}
	d := Driver{ReceiptOnly: true, Session: native, Profile: protocol.ExecutionProfile{Model: "original-catalog-alias", Effort: "low"}}
	defer d.Handle(t.Context(), "close", json.RawMessage(`{}`))
	raw, _ := json.Marshal(Request{ThreadID: "original-thread", Config: harness.ThreadConfig{Model: "original-applied-model", Sandbox: "read-only", ApprovalPolicy: "never", Config: map[string]any{"model_provider": "original-provider", "model_reasoning_effort": "high"}}})
	if _, err := d.Handle(t.Context(), "resume", raw); err != nil {
		t.Fatal("receipt-only resume required current model readiness", err)
	}
	got := native.thread.ModelDefaults()
	if got != (harness.ModelSelection{Model: "original-applied-model", Provider: "original-provider", Effort: "high"}) || native.models != 0 || native.starts != 0 || native.resumes != 1 {
		t.Fatal("receipt resume changed original configuration", got, native.models, native.starts, native.resumes)
	}
	if result := native.thread.config.OnToolCall(t.Context(), harness.ToolCall{Tool: "must_not_execute"}); result.Success || result.Text == "" {
		t.Fatal("receipt driver executed a native tool callback", result)
	}
	read, _ := json.Marshal(Request{ThreadID: "original-thread", MessageID: "original-repair", TurnID: "original-turn"})
	result, err := d.Handle(t.Context(), "find", read)
	if err != nil {
		t.Fatal(err)
	}
	if receipt := result.(FindResult); !receipt.Found || receipt.Receipt.Status != "completed" || native.thread.reads != 1 {
		t.Fatal("original native terminal was not read", receipt)
	}
	for _, method := range []string{"models", "start", "fork", "turn", "inject", "steer", "compact", "interrupt", "tool_result", "release"} {
		if _, err := d.Handle(t.Context(), method, raw); !errors.Is(err, harness.ErrRejected) {
			t.Fatal("receipt driver admitted a native business operation", method, err)
		}
	}
	if native.models != 0 || native.starts != 0 {
		t.Fatal("rejected receipt operation reached native execution", native.models, native.starts)
	}
}

func TestReceiptOnlyDriverRejectsMissingOriginalResumeConfiguration(t *testing.T) {
	for _, missing := range []string{"thread", "model"} {
		t.Run(missing, func(t *testing.T) {
			native := &receiptNativeSession{thread: &receiptNativeThread{events: make(chan harness.Event)}, done: make(chan struct{})}
			d := Driver{ReceiptOnly: true, Session: native}
			defer d.Handle(t.Context(), "close", json.RawMessage(`{}`))
			request := Request{ThreadID: "original-thread", Config: harness.ThreadConfig{Model: "original-model", Sandbox: "read-only", ApprovalPolicy: "never"}}
			if missing == "thread" {
				request.ThreadID = ""
			} else {
				request.Config.Model = ""
			}
			raw, _ := json.Marshal(request)
			if _, err := d.Handle(t.Context(), "resume", raw); !errors.Is(err, harness.ErrRejected) || native.models != 0 || native.resumes != 0 || native.starts != 0 {
				t.Fatal("missing original configuration reached native lifecycle", err, native.models, native.resumes, native.starts)
			}
		})
	}
}
