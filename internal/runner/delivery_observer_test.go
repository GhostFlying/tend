package runner

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/reviewwatch"
	"github.com/GhostFlying/tend/internal/testkit"
)

type observationFunc func(context.Context, reviewwatch.Ref) (reviewwatch.Observation, error)

func (f observationFunc) Observe(ctx context.Context, ref reviewwatch.Ref) (reviewwatch.Observation, error) {
	return f(ctx, ref)
}

func TestFault_DeliveryObservationRetryKeepsIdentityWithoutBlockingMaintenance(t *testing.T) {
	r := controlRunner(t)
	r.cfg.IDs = testkit.NewIDGen(testkit.NewClock(), 7)
	r.cfg.Log = slog.New(slog.DiscardHandler)
	r.c.log = r.cfg.Log
	r.cfg.RetryDelay = time.Millisecond
	r.cfg.DeliveryProviders = []string{"github"}
	calls := 0
	r.cfg.DeliveryObserver = observationFunc(func(_ context.Context, ref reviewwatch.Ref) (reviewwatch.Observation, error) {
		calls++
		if ref.Repository != "example/project" || ref.Number != 7 {
			t.Fatal(ref)
		}
		return reviewwatch.Observation{State: "open", HeadSHA: "head"}, nil
	})
	var reports []protocol.Envelope
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var env protocol.Envelope
		if err := json.NewDecoder(req.Body).Decode(&env); err != nil {
			t.Error(err)
			return
		}
		if req.Header.Get(headerRun) != "" {
			t.Error("observer used a run credential")
		}
		switch env.Operation {
		case protocol.OpDeliveriesPoll:
			raw, _ := json.Marshal(protocol.DeliveryPollResult{Assignments: []protocol.DeliveryAssignment{{WorkItemID: "item", Delivery: protocol.WorkItemDelivery{DeliveryID: "delivery", Provider: "github", Repository: "example/project", Number: 7}, Generation: 3, Nonce: "query"}}})
			json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: raw}})
		case protocol.OpDeliveriesReport:
			reports = append(reports, env)
			if len(reports) == 1 {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				conn.Close()
				return
			}
			raw, _ := json.Marshal(protocol.DeliveryReportResult{Accepted: true})
			json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: raw}})
		default:
			t.Errorf("unexpected operation %s", env.Operation)
		}
	}))
	defer server.Close()
	r.c.base = server.URL
	r.c.http = server.Client()
	if err := r.observeDelivery(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(reports) != 2 || reports[0].RequestID != reports[1].RequestID || string(reports[0].Payload) != string(reports[1].Payload) {
		t.Fatalf("retry lost identity: calls=%d reports=%+v", calls, reports)
	}
	requests, err := r.cfg.Journal.Requests(t.Context())
	if err != nil || len(requests) != 0 {
		t.Fatalf("observer entered execution pending queue: %+v %v", requests, err)
	}
	rt, err := r.runtimeObservation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(rt.Capabilities, protocol.CapabilityDeliveryGitHub) || slices.Contains(rt.Blockers, "pending_operation") {
		t.Fatalf("capability or barrier wrong: %+v", rt)
	}
}

func TestDeliveryObserverOlderServerStopsLoopOnce(t *testing.T) {
	r := controlRunner(t)
	r.cfg.IDs = testkit.NewIDGen(testkit.NewClock(), 1)
	r.cfg.Log = slog.New(slog.DiscardHandler)
	r.c.log = r.cfg.Log
	count := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count++
		json.NewEncoder(w).Encode(rpcResponse{Error: protocol.Errorf(protocol.CodeInvalidRequest, "unknown operation")})
	}))
	defer server.Close()
	r.c.base = server.URL
	r.c.http = server.Client()
	done := make(chan struct{})
	go func() { r.deliveryObservations(t.Context()); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("old server caused recurring retry loop")
	}
	if count != 1 {
		t.Fatalf("requests=%d", count)
	}
}

func TestDeliveryObserverCancellationDoesNotReportFalseFailure(t *testing.T) {
	r := controlRunner(t)
	r.cfg.IDs = testkit.NewIDGen(testkit.NewClock(), 1)
	r.cfg.Log = slog.New(slog.DiscardHandler)
	r.c.log = r.cfg.Log
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r.cfg.DeliveryObserver = observationFunc(func(context.Context, reviewwatch.Ref) (reviewwatch.Observation, error) {
		cancel()
		return reviewwatch.Observation{}, context.Canceled
	})
	count := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count++
		raw, _ := json.Marshal(protocol.DeliveryPollResult{Assignments: []protocol.DeliveryAssignment{{}}})
		json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: raw}})
	}))
	defer server.Close()
	r.c.base = server.URL
	r.c.http = server.Client()
	if err := r.observeDelivery(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("cancel was reported as provider failure")
	}
}
