package runner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

type observedExecutions struct{ calls atomic.Int64 }

func (*observedExecutions) Refresh(context.Context) error { return nil }
func (e *observedExecutions) Observe(context.Context) ([]protocol.AgentExecutor, error) {
	e.calls.Add(1)
	return []protocol.AgentExecutor{{Reason: "adapter_not_loaded"}}, nil
}
func (*observedExecutions) Resolve(context.Context, protocol.RunExecutionPin) (ResolvedExecution, error) {
	panic("unexpected execution")
}
func TestAgentExecutionObservationsRequireMutualHelloAcknowledgment(t *testing.T) {
	for _, configured := range []bool{false, true} {
		for _, ack := range []bool{false, true} {
			t.Run(map[bool]string{true: "configured", false: "legacy"}[configured]+"/"+map[bool]string{true: "ack", false: "old_server"}[ack], func(t *testing.T) {
				r := controlRunner(t)
				r.cfg.IDs = testkit.NewIDGen(testkit.NewClock(), 747)
				executions := &observedExecutions{}
				if configured {
					r.cfg.Executions = executions
				}
				var hello protocol.HelloPayload
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
					var env protocol.Envelope
					if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
						t.Error(err)
					}
					if env.Operation != protocol.OpRunnerHello {
						t.Errorf("unexpected operation %s", env.Operation)
					}
					if err := json.Unmarshal(env.Payload, &hello); err != nil {
						t.Error(err)
					}
					// Old servers acknowledge no execution capability and return their old shape.
					raw, _ := json.Marshal(protocol.HelloResult{AgentExecution: ack})
					_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: raw}})
				}))
				defer server.Close()
				r.c.base = server.URL
				r.c.http = server.Client()
				if err := r.freshHello(t.Context()); err != nil {
					t.Fatal(err)
				}
				if slices.Contains(hello.Runtime.Capabilities, protocol.CapabilityAgentExecution) != configured || len(hello.Runtime.AgentExecutors) != 0 || executions.calls.Load() != 0 {
					t.Fatal("observations sent before ack", hello, executions.calls.Load())
				}
				runtime, err := r.runtimeObservation(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				expected := configured && ack
				if (len(runtime.AgentExecutors) > 0) != expected || (executions.calls.Load() > 0) != expected {
					t.Fatal("observations escaped negotiation", runtime, executions.calls.Load())
				}
				raw, err := json.Marshal(runtime)
				if err != nil {
					t.Fatal(err)
				}
				var fields map[string]any
				_ = json.Unmarshal(raw, &fields)
				if !expected && fields["agent_executors"] != nil {
					t.Fatal("old strict decoder would receive a new field", string(raw))
				}
			})
		}
	}
}

type retryingExecutions struct {
	calls     atomic.Int64
	recovered chan struct{}
}

func (e *retryingExecutions) Refresh(context.Context) error {
	if e.calls.Add(1) == 1 {
		return errors.New("temporary native setup failure")
	}
	select {
	case <-e.recovered:
	default:
		close(e.recovered)
	}
	return nil
}
func (e *retryingExecutions) Observe(context.Context) ([]protocol.AgentExecutor, error) {
	return []protocol.AgentExecutor{{Available: e.calls.Load() > 1}}, nil
}
func (*retryingExecutions) Resolve(context.Context, protocol.RunExecutionPin) (ResolvedExecution, error) {
	panic("unexpected execution")
}

func TestReadinessFailureRetriesThroughExistingReconciler(t *testing.T) {
	r := controlRunner(t)
	r.cfg.IDs = testkit.NewIDGen(testkit.NewClock(), 748)
	r.cfg.RetryDelay = time.Millisecond
	r.cfg.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	executions := &retryingExecutions{recovered: make(chan struct{})}
	r.cfg.Executions = executions
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var env protocol.Envelope
		if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
			t.Error(err)
		}
		if env.Operation != protocol.OpRunnerHello {
			t.Errorf("unexpected operation %s", env.Operation)
		}
		result, _ := json.Marshal(protocol.HelloResult{AgentExecution: true})
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: result}})
	}))
	defer server.Close()
	r.c.base = server.URL
	r.c.http = server.Client()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	done := make(chan struct{})
	go func() { defer close(done); r.reconciler(ctx) }()
	defer func() { cancel(); <-done }()
	r.requestReconcile()
	select {
	case <-executions.recovered:
	case <-ctx.Done():
		t.Fatal("readiness did not retry without another reload or reconciliation signal", executions.calls.Load())
	}
	runtime, err := r.runtimeObservation(t.Context())
	if err != nil || len(runtime.AgentExecutors) != 1 || !runtime.AgentExecutors[0].Available {
		t.Fatal(runtime, err)
	}
}
