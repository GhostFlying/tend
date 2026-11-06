package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/coordination"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

type receiptBridgeResolver struct {
	closeRecoveryResolver
	native             *receiptBridgeSession
	business, receipts atomic.Int64
	pin                protocol.RunExecutionPin
}

func (r *receiptBridgeResolver) ResolveManager(context.Context, protocol.RunExecutionPin) (ManagerExecution, error) {
	r.business.Add(1)
	return ManagerExecution{}, errors.New("new business readiness is unavailable")
}
func (r *receiptBridgeResolver) ResolveManagerReceipt(_ context.Context, pin protocol.RunExecutionPin) (ManagerExecution, error) {
	r.receipts.Add(1)
	if pin != r.pin {
		return ManagerExecution{}, errors.New("original receipt pin changed")
	}
	return ManagerExecution{Profile: pin.Profile, Open: func(context.Context, string) (harness.Session, error) { return r.native, nil }}, nil
}

type receiptBridgeSession struct {
	harness.Session
	thread                  *receiptBridgeThread
	models, starts, resumes int
	done                    chan struct{}
	once                    sync.Once
}

func (s *receiptBridgeSession) Models(context.Context) ([]harness.Model, error) {
	s.models++
	return nil, errors.New("current model catalog is unavailable")
}
func (s *receiptBridgeSession) StartThread(context.Context, harness.ThreadConfig) (harness.Thread, error) {
	s.starts++
	return nil, errors.New("new thread is forbidden")
}
func (s *receiptBridgeSession) ResumeThread(_ context.Context, id string, cfg harness.ThreadConfig) (harness.Thread, error) {
	s.resumes++
	if id != "original-thread" {
		return nil, harness.ErrRejected
	}
	s.thread.config = cfg
	return s.thread, nil
}
func (s *receiptBridgeSession) Done() <-chan struct{} { return s.done }
func (s *receiptBridgeSession) Close() error {
	s.once.Do(func() { close(s.thread.events); close(s.done) })
	return nil
}

type receiptBridgeThread struct {
	harness.Thread
	events chan harness.Event
	config harness.ThreadConfig
	reads  int
}

func (*receiptBridgeThread) ID() string                     { return "original-thread" }
func (t *receiptBridgeThread) Events() <-chan harness.Event { return t.events }
func (t *receiptBridgeThread) ModelDefaults() harness.ModelSelection {
	provider, _ := t.config.Config["model_provider"].(string)
	effort, _ := t.config.Config["model_reasoning_effort"].(string)
	return harness.ModelSelection{Model: t.config.Model, Provider: provider, Effort: effort}
}
func (t *receiptBridgeThread) PermissionDefaults() harness.PermissionSettings {
	return harness.PermissionSettings{Sandbox: t.config.Sandbox, ApprovalPolicy: t.config.ApprovalPolicy}
}
func (t *receiptBridgeThread) FindTurn(_ context.Context, identity, turn string) (harness.TurnReceipt, bool, error) {
	if identity != "original-repair" || turn != "original-turn" {
		return harness.TurnReceipt{}, false, errors.New("original native request/turn changed")
	}
	t.reads++
	return harness.TurnReceipt{ID: turn, Status: "completed", Raw: json.RawMessage(`{"id":"original-turn","status":"completed"}`)}, true, nil
}

func TestManagerReceiptNativeUsesRestrictedResolverAndDriver(t *testing.T) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var phaseMu sync.Mutex
	lastPhase := "initializing"
	var requestTrace []string
	bufferedReplies := 0
	mark := func(phase string, env *protocol.Envelope) {
		phaseMu.Lock()
		lastPhase = phase
		var requestID, operation string
		if env != nil {
			requestID, operation = env.RequestID, env.Operation
			if phase == "http-request" {
				requestTrace = append(requestTrace, requestID+":"+operation)
			}
		}
		if phase == "reply-buffered" {
			bufferedReplies++
		}
		count := bufferedReplies
		phaseMu.Unlock()
		t.Logf("phase=%s elapsed=%s request_id=%q operation=%q buffered_replies=%d", phase, time.Since(started), requestID, operation, count)
	}
	diagnostic := func() string {
		phaseMu.Lock()
		defer phaseMu.Unlock()
		return fmt.Sprintf("last_phase=%s elapsed=%s buffered_replies=%d requests=%v", lastPhase, time.Since(started), bufferedReplies, requestTrace)
	}
	clock := testkit.NewClock()
	mark("journal-open", nil)
	j, err := journal.Open(ctx, filepath.Join(t.TempDir(), "journal.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	mark("journal-ready", nil)
	pin := protocol.RunExecutionPin{Profile: protocol.ExecutionProfile{Model: "original-catalog-alias", Effort: "low"}}
	as := protocol.Assignment{Protocol: 2, RunID: "receipt-reader", LeaseID: "reader-lease", Role: protocol.RoleIssueManager, Execution: &pin, Manager: &protocol.IssueManagerAssignment{RequireReceiptReader: true, ReceiptOnly: true, ConversationID: "original-conversation"}}
	if err := j.AcceptAssignment(ctx, as, ""); err != nil {
		t.Fatal(err)
	}
	mark("assignment-saved", nil)
	native := &receiptBridgeSession{thread: &receiptBridgeThread{events: make(chan harness.Event)}, done: make(chan struct{})}
	resolver := &receiptBridgeResolver{native: native, pin: pin}
	resume, _ := json.Marshal(coordination.Request{ThreadID: "original-thread", Config: harness.ThreadConfig{Model: "original-applied-model", Sandbox: "read-only", ApprovalPolicy: "never", Config: map[string]any{"model_provider": "original-provider", "model_reasoning_effort": "high"}}})
	find, _ := json.Marshal(coordination.Request{ThreadID: "original-thread", MessageID: "original-repair", TurnID: "original-turn"})
	commands := []protocol.ManagerNativeRequest{{ID: "resume-original", Method: "resume", Payload: resume}, {ID: "find-original", Method: "find", Payload: find}, {ID: "forbidden-business", Method: "turn", Payload: find}, {ID: "close-reader", Method: "close", Payload: json.RawMessage(`{}`)}}
	var claims atomic.Int64
	replies := make(chan protocol.ManagerNativeReply, len(commands))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var env protocol.Envelope
		if err := json.NewDecoder(req.Body).Decode(&env); err != nil {
			t.Error(err)
			return
		}
		mark("http-request", &env)
		var result any = map[string]any{}
		if env.Operation == protocol.OpManagerNativeClaim {
			index := claims.Add(1) - 1
			if index >= int64(len(commands)) {
				t.Error("reader did not finish after its close")
				return
			}
			result = commands[index]
		}
		if env.Operation == protocol.OpManagerNativeReply {
			var reply protocol.ManagerNativeReply
			if err := json.Unmarshal(env.Payload, &reply); err != nil {
				t.Error(err)
			}
			replies <- reply
			mark("reply-buffered", &env)
		}
		raw, _ := json.Marshal(result)
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: raw}})
	}))
	defer server.Close()
	r := New(Config{Journal: j, Executions: resolver, Now: clock.Now, IDs: testkit.NewIDGen(clock, 1), ServerURL: server.URL, HTTPClient: server.Client(), RequestTimeout: time.Second, Log: slog.New(slog.DiscardHandler)})
	mark("manager-native", nil)
	if err := r.managerNative(ctx, ctx, as); err != nil {
		t.Fatalf("actual runner could not read the original native receipt: %v; %s", err, diagnostic())
	}
	mark("manager-native-completed", nil)
	if resolver.receipts.Load() != 1 || resolver.business.Load() != 0 || native.models != 0 || native.starts != 0 || native.resumes != 1 || native.thread.reads != 1 || native.thread.ModelDefaults() != (harness.ModelSelection{Model: "original-applied-model", Provider: "original-provider", Effort: "high"}) {
		t.Fatal("runner receipt-only mode was lost before native history", resolver.receipts.Load(), resolver.business.Load(), native.models, native.starts, native.resumes, native.thread.reads)
	}
	if len(replies) != len(commands) {
		t.Fatal("native request receipts were not durably reported", len(replies))
	}
	for range len(commands) {
		reply := <-replies
		if reply.ID == "forbidden-business" {
			if !reply.Rejected || reply.Error == "" {
				t.Fatal("receipt driver admitted a fresh native turn", reply)
			}
		} else if reply.Error != "" {
			t.Fatal("receipt-only native operation failed", reply)
		}
		if reply.ID == "find-original" {
			var history coordination.FindResult
			if json.Unmarshal(reply.Result, &history) != nil || !history.Found || history.Receipt.Status != "completed" {
				t.Fatal("actual runner dropped the original native terminal", reply)
			}
		}
	}
}

func TestManagerReceiptResolverNeverFallsBackToBusinessReadiness(t *testing.T) {
	resolver := &closeRecoveryResolver{}
	r := New(Config{Executions: resolver})
	as := protocol.Assignment{Execution: &protocol.RunExecutionPin{}, Manager: &protocol.IssueManagerAssignment{RequireReceiptReader: true, ReceiptOnly: true}}
	if _, err := r.resolveManagerExecution(t.Context(), as); err == nil {
		t.Fatal("missing receipt resolver fell back to ordinary business resolution")
	}
	as.Manager.ReceiptOnly = false
	as.Manager.RequireReceiptReader = false
	if _, err := r.resolveManagerExecution(t.Context(), as); err != nil {
		t.Fatal("ordinary manager resolver changed", err)
	}
}

type receiptWithoutBusinessResolver struct{ ExecutionResolver }

func (receiptWithoutBusinessResolver) ResolveManagerReceipt(context.Context, protocol.RunExecutionPin) (ManagerExecution, error) {
	return ManagerExecution{}, harness.ErrRejected
}

func TestManagerReceiptCapabilityRequiresBothResolversAndCurrentProtocol(t *testing.T) {
	ordinary := &closeRecoveryResolver{}
	for _, tc := range []struct {
		name     string
		resolver ExecutionResolver
		protocol int64
		manager  bool
		reader   bool
	}{
		{"both", &receiptBridgeResolver{}, 2, true, true},
		{"ordinary", ordinary, 2, true, false},
		{"receipt_without_business", receiptWithoutBusinessResolver{ExecutionResolver: ordinary}, 2, false, false},
		{"previous_protocol", &receiptBridgeResolver{}, 1, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := controlRunner(t)
			r.cfg.Executions = tc.resolver
			r.runnerProtocol = tc.protocol
			view, err := r.runtimeObservation(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if slices.Contains(view.Capabilities, protocol.CapabilityManagerReceiptReader) != tc.reader || slices.Contains(view.Capabilities, protocol.CapabilityIssueManager) != tc.manager {
				t.Fatal("runtime advertised a receipt contract it cannot enforce", view.Capabilities)
			}
		})
	}
}

// Historical receipt-only assignments predate strict reader negotiation.
func TestLegacyReceiptOnlyUsesExistingManagerResolver(t *testing.T) {
	r := New(Config{Executions: &closeRecoveryResolver{}})
	var as protocol.Assignment
	if err := json.Unmarshal([]byte(`{"execution":{},"manager":{"receipt_only":true,"issue_id":"issue","conversation_id":"conversation","assignment_epoch":1,"category_id":"manager"}}`), &as); err != nil {
		t.Fatal(err)
	}
	if as.Manager.RequireReceiptReader {
		t.Fatal("historical serialized assignment enabled strict reader")
	}
	if _, err := r.resolveManagerExecution(t.Context(), as); err != nil {
		t.Fatal("legacy receipt-only assignment lost its existing resolver contract", err)
	}
}
