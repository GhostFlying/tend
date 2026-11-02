package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

type retainedResolver struct {
	observedExecutions
	pins []protocol.RunExecutionPin
}

func (e *retainedResolver) ContinuationBackend() string { return "git" }

func (e *retainedResolver) SetRetained(pins []protocol.RunExecutionPin) { e.pins = slices.Clone(pins) }
func (e *retainedResolver) ObserveRetained(context.Context) ([]protocol.AgentExecutor, error) {
	out := []protocol.AgentExecutor{}
	for _, pin := range e.pins {
		out = append(out, protocol.AgentExecutor{Pin: pin, Available: true})
	}
	return out, nil
}

func TestContinuationObservationWaitsForHelloAcknowledgment(t *testing.T) {
	for _, ack := range []bool{false, true} {
		t.Run(map[bool]string{false: "old-server", true: "supported"}[ack], func(t *testing.T) {
			r := controlRunner(t)
			r.cfg.HostID = "host"
			r.cfg.IDs = testkit.NewIDGen(testkit.NewClock(), 936)
			executions := &retainedResolver{}
			r.cfg.Executions = executions
			pin := protocol.RunExecutionPin{Identity: protocol.AgentIdentity{ExecutionEnvironmentID: "host", LocalID: "cedar"}, AgentRevision: 1, Runtime: protocol.AgentRuntimeReference{RuntimeID: "native", Revision: 1}, InventoryRevision: 1, SnapshotID: "retained", PublicSHA256: "sha256:" + strings.Repeat("a", 64), Profile: protocol.ExecutionProfile{Model: "model"}, Worker: protocol.WorkerAppServer, BackendID: "git"}
			var hello protocol.HelloPayload
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				var env protocol.Envelope
				if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
					t.Error(err)
				}
				if err := json.Unmarshal(env.Payload, &hello); err != nil {
					t.Error(err)
				}
				result := protocol.HelloResult{AgentExecution: true, AgentContinuation: ack}
				if ack {
					result.RetainedExecutions = []protocol.RunExecutionPin{pin}
				}
				raw, _ := json.Marshal(result)
				_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: raw}})
			}))
			defer server.Close()
			r.c.base = server.URL
			r.c.http = server.Client()
			if err := r.freshHello(t.Context()); err != nil {
				t.Fatal(err)
			}
			if hello.Runtime.ContinuationBackend != "" || len(hello.Runtime.AgentContinuations) != 0 || !slices.Contains(hello.Runtime.Capabilities, protocol.CapabilityAgentContinuation) {
				t.Fatal("retained observations escaped the handshake", hello)
			}
			observation, err := r.runtimeObservation(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if (observation.ContinuationBackend == "git") != ack || (len(observation.AgentContinuations) == 1) != ack || (len(executions.pins) == 1) != ack {
				t.Fatal("acknowledgment did not control observations", observation)
			}
			if ack {
				select {
				case <-r.reconcile:
				default:
					t.Fatal("new retention did not wake reconciler")
				}
				r.acceptRetained([]protocol.RunExecutionPin{pin})
				select {
				case <-r.reconcile:
					t.Fatal("unchanged heartbeat retriggered reconciliation")
				default:
				}
				r.acceptRetained(nil)
				if len(executions.pins) != 0 {
					t.Fatal("withdrawn retention remained")
				}
			}
		})
	}
}

type orderedRetainedResolver struct {
	observedExecutions
	mu      sync.Mutex
	pins    []protocol.RunExecutionPin
	entered chan struct{}
	release chan struct{}
}

func (e *orderedRetainedResolver) ContinuationBackend() string { return "git" }

func (e *orderedRetainedResolver) SetRetained(pins []protocol.RunExecutionPin) {
	if len(pins) > 0 && pins[0].SnapshotID == "old" {
		close(e.entered)
		<-e.release
	}
	e.mu.Lock()
	e.pins = slices.Clone(pins)
	e.mu.Unlock()
}
func (e *orderedRetainedResolver) ObserveRetained(context.Context) ([]protocol.AgentExecutor, error) {
	return nil, nil
}

func TestConcurrentContinuationRepliesKeepResolverAndAcknowledgmentTogether(t *testing.T) {
	r := controlRunner(t)
	r.cfg.HostID = "host"
	r.agentContinuationSupported = true
	resolver := &orderedRetainedResolver{entered: make(chan struct{}), release: make(chan struct{})}
	r.cfg.Executions = resolver
	old := protocol.RunExecutionPin{Identity: protocol.AgentIdentity{ExecutionEnvironmentID: "host", LocalID: "cedar"}, AgentRevision: 1, Runtime: protocol.AgentRuntimeReference{RuntimeID: "native", Revision: 1}, InventoryRevision: 1, SnapshotID: "old", PublicSHA256: "sha256:" + strings.Repeat("a", 64), Profile: protocol.ExecutionProfile{Model: "model"}, Worker: protocol.WorkerAppServer, BackendID: "git"}
	next := old
	next.SnapshotID = "next"
	first := make(chan struct{})
	go func() { r.acceptRetained([]protocol.RunExecutionPin{old}); close(first) }()
	<-resolver.entered
	second := make(chan struct{})
	go func() { r.acceptRetained([]protocol.RunExecutionPin{next}); close(second) }()
	// A second reply can arrive while a setter is in progress. With serialized
	// publication it waits; otherwise it completes and the old setter overwrites it.
	select {
	case <-second:
	case <-time.After(20 * time.Millisecond):
	}
	close(resolver.release)
	<-first
	<-second
	r.controlMu.Lock()
	cached := slices.Clone(r.retainedExecutions)
	r.controlMu.Unlock()
	resolver.mu.Lock()
	actual := slices.Clone(resolver.pins)
	resolver.mu.Unlock()
	if !slices.Equal(cached, actual) || len(actual) != 1 || actual[0] != next {
		t.Fatal("acknowledged and installed retention sets differ", cached, actual)
	}
}
