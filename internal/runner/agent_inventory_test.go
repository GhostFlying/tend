package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestAgentInventoryHelloCapabilityRequiresConfigurationAndAcknowledgment(t *testing.T) {
	for _, configured := range []bool{false, true} {
		for _, ack := range []bool{false, true} {
			t.Run(map[bool]string{true: "configured", false: "legacy"}[configured]+"/"+map[bool]string{true: "ack", false: "old_server"}[ack], func(t *testing.T) {
				r := controlRunner(t)
				r.cfg.IDs = testkit.NewIDGen(testkit.NewClock(), 73)
				if configured {
					r.cfg.AgentInventory = func(context.Context) (protocol.LocalAgentConfiguration, error) {
						return protocol.LocalAgentConfiguration{}, nil
					}
				}
				var hello protocol.HelloPayload
				var extra atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
					var env protocol.Envelope
					if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
						t.Error(err)
					}
					if env.Operation == protocol.OpRunnerHello {
						if err := json.Unmarshal(env.Payload, &hello); err != nil {
							t.Error(err)
						}
						raw, _ := json.Marshal(protocol.HelloResult{AgentInventory: ack})
						_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: raw}})
					} else {
						extra.Add(1)
						raw, _ := json.Marshal(protocol.AgentInventoryStatus{RequestState: "not_requested"})
						_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: raw}})
					}
				}))
				defer server.Close()
				r.c.base = server.URL
				r.c.http = server.Client()
				if err := r.freshHello(t.Context()); err != nil {
					t.Fatal(err)
				}
				if slices.Contains(hello.Runtime.Capabilities, protocol.CapabilityAgentInventory) != configured {
					t.Fatal("legacy capability changed")
				}
				err := r.RegisterAgentInventory(t.Context())
				if configured && !ack {
					if err == nil {
						t.Fatal("old server allowed registration")
					}
				} else if err != nil {
					t.Fatal(err)
				}
				if (!configured || !ack) && extra.Load() != 0 {
					t.Fatal("inventory operation sent before mutual acknowledgment")
				}
			})
		}
	}
}

func TestAgentInventoryAuthorizationRejectionDoesNotEraseUnknown(t *testing.T) {
	r := controlRunner(t)
	r.cfg.IDs = testkit.NewIDGen(testkit.NewClock(), 75)
	r.cfg.AgentInventory = func(context.Context) (protocol.LocalAgentConfiguration, error) {
		return protocol.LocalAgentConfiguration{}, nil
	}
	runtime, err := r.runtimeObservation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	r.confirmedRuntime = *runtime
	r.agentInventorySupported = true
	env, err := r.newEnvelope(protocol.OpRunnerInventoryRegister, protocol.Target{Type: "host", ID: r.cfg.HostID}, protocol.RegisterAgentInventoryPayload{Runtime: runtime, LocalRevision: 1, SnapshotID: "snapshot-one", PublicSHA256: "original"})
	if err != nil {
		t.Fatal(err)
	}
	revision := int64(4)
	env.ExpectedControlRevision = &revision
	if err := r.cfg.Journal.PutRequest(t.Context(), journal.Request{Envelope: *env}); err != nil {
		t.Fatal(err)
	}
	hash, _ := protocol.PayloadHash(env)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var request protocol.Envelope
		json.NewDecoder(q.Body).Decode(&request)
		if request.Operation == protocol.OpRunnerInventoryStatus {
			raw, _ := json.Marshal(protocol.AgentInventoryStatus{RequestState: "not_recorded"})
			json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: raw}})
		} else {
			// The process may have been fenced since the status read. This is not
			// evidence that the original attempt never committed.
			json.NewEncoder(w).Encode(rpcResponse{Error: protocol.Errorf(protocol.CodeConflictPayload, "runner instance has been superseded")})
		}
	}))
	defer server.Close()
	r.c.base = server.URL
	r.c.http = server.Client()
	if err := r.RegisterAgentInventory(t.Context()); err == nil {
		t.Fatal("authorization failure ignored")
	}
	requests, err := r.cfg.Journal.Requests(t.Context())
	if err != nil || len(requests) != 1 {
		t.Fatalf("unknown erased: %+v %v", requests, err)
	}
	retainedHash, _ := protocol.PayloadHash(&requests[0].Envelope)
	if retainedHash != hash || requests[0].Envelope.RequestID != env.RequestID {
		t.Fatal("unknown envelope changed")
	}
}
