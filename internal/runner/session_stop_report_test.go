package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

func sessionStopReportFixture(t *testing.T) (*Runner, protocol.Assignment, protocol.Envelope, string) {
	t.Helper()
	r, as, workers, _, path := implementationReconcileFixture(t)
	as.SessionStopReports = true
	payload, err := json.Marshal(workers.result.Result)
	if err != nil {
		t.Fatal(err)
	}
	final := protocol.Envelope{RequestID: "original-result", Operation: protocol.OpRunsSubmitArtifact, Target: runTarget(as), Payload: payload}
	if err := r.cfg.Journal.Finish(t.Context(), as.RunID, journal.PhaseSubmitting, journal.Request{RunID: as.RunID, Envelope: final}); err != nil {
		t.Fatal(err)
	}
	if err := r.cfg.Journal.ConfirmStopped(t.Context(), as.RunID); err != nil {
		t.Fatal(err)
	}
	return r, as, final, path
}

func TestSessionStopReportRequiresProtocol3AndExplicitAssignment(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		version  int64
		enabled  bool
		expected int64
	}{{"protocol2", 2, true, 0}, {"not-negotiated", 3, false, 0}, {"protocol3-negotiated", 3, true, 1}} {
		t.Run(scenario.name, func(t *testing.T) {
			r, as, final, _ := sessionStopReportFixture(t)
			as.Protocol, as.SessionStopReports = scenario.version, scenario.enabled
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				var env protocol.Envelope
				if err := json.NewDecoder(q.Body).Decode(&env); err != nil || env.Operation != protocol.OpRunsRecordSessionStop {
					t.Error("unexpected optional operation", env, err)
				}
				calls.Add(1)
				_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded}})
			}))
			defer server.Close()
			r.c.base, r.c.http = server.URL, server.Client()
			if err := r.reportSessionStop(t.Context(), as); err != nil || calls.Load() != scenario.expected {
				t.Fatal("optional RPC was not explicitly negotiated", err, calls.Load())
			}
			recorded, err := r.cfg.Journal.SessionStopReport(t.Context(), as.RunID)
			if err != nil || (recorded != nil) != (scenario.expected != 0) {
				t.Fatal("unexpected local metadata request", recorded, err)
			}
			request, ok, err := r.cfg.Journal.RequestForRun(t.Context(), as.RunID)
			if err != nil || !ok || !reflect.DeepEqual(request.Envelope, final) {
				t.Fatal("optional metadata replaced the original result", request, err)
			}
		})
	}
}

func TestOptionalSessionStopReceiptFailurePreservesBusinessCompletion(t *testing.T) {
	for _, scenario := range []string{"unavailable", "lost-reply", "unresponsive"} {
		t.Run(scenario, func(t *testing.T) {
			r, as, final, path := sessionStopReportFixture(t)
			var metadataCalls, finalCalls atomic.Int64
			var metadata protocol.Envelope
			var mu sync.Mutex
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				var env protocol.Envelope
				if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
					t.Error(err)
					return
				}
				if env.Operation == protocol.OpRunsRecordSessionStop {
					metadataCalls.Add(1)
					mu.Lock()
					metadata = env
					mu.Unlock()
					switch scenario {
					case "unresponsive":
						<-q.Context().Done()
					case "lost-reply":
						_, _ = w.Write([]byte(`{"operation":`))
					default:
						w.WriteHeader(http.StatusServiceUnavailable)
					}
					return
				}
				if !reflect.DeepEqual(env, final) {
					t.Error("metadata failure changed the original business result", env)
				}
				finalCalls.Add(1)
				_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded}})
			}))
			defer server.Close()
			r.c.base, r.c.http, r.c.timeout = server.URL, server.Client(), time.Minute
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			if err := r.report(ctx, as); err != nil {
				t.Fatal("optional receipt blocked real completion", err)
			}
			if metadataCalls.Load() != 1 || finalCalls.Load() != 1 {
				t.Fatal("optional metadata or business work was replayed", metadataCalls.Load(), finalCalls.Load())
			}
			reopenOutcomeJournal(t, r, path)
			saved, err := r.cfg.Journal.HistoricalRun(t.Context(), as.RunID)
			if err != nil || saved.Phase != journal.PhaseDone || !saved.StoppedConfirmed {
				t.Fatal("completed result did not release local ownership", saved, err)
			}
			retained, err := r.cfg.Journal.SessionStopReport(t.Context(), as.RunID)
			mu.Lock()
			matches := retained != nil && reflect.DeepEqual(*retained, metadata)
			mu.Unlock()
			if err != nil || !matches {
				t.Fatal("uncertain metadata request identity was discarded", retained, err)
			}
			pending, err := r.cfg.Journal.Requests(t.Context())
			if err != nil || len(pending) != 0 {
				t.Fatal("optional optimization held business pending operations", pending, err)
			}
			runtime, err := r.runtimeObservation(t.Context())
			if err != nil || runtime.PendingOperations != 0 {
				t.Fatal("optional optimization blocks ordinary maintenance", runtime, err)
			}
		})
	}
}

func TestSessionStopLostReplyRetryKeepsOriginalRequest(t *testing.T) {
	r, as, _, path := sessionStopReportFixture(t)
	var requests []protocol.Envelope
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var env protocol.Envelope
		if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		requests = append(requests, env)
		first := len(requests) == 1
		mu.Unlock()
		if first {
			_, _ = w.Write([]byte(`{"operation":`))
			return
		}
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded}})
	}))
	defer server.Close()
	r.c.base, r.c.http = server.URL, server.Client()
	if err := r.reportSessionStop(t.Context(), as); err == nil {
		t.Fatal("lost reply was reported as success")
	}
	reopenOutcomeJournal(t, r, path)
	if err := r.reportSessionStop(t.Context(), as); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 || !reflect.DeepEqual(requests[0], requests[1]) {
		t.Fatal("metadata retry changed immutable request identity", requests)
	}
}

func TestStrictStopOvertakesUnavailableOptionalSessionReceipt(t *testing.T) {
	r, as, _, authority, _ := retainedAuthorityStopFixture(t)
	as.SessionStopReports = true
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := r.cfg.Journal.ClearRetainedStop(ctx, as.RunID); err != nil {
		t.Fatal(err)
	}
	r.cfg.RetryDelay = 10 * time.Millisecond
	var stopped atomic.Bool
	var metadataCalls atomic.Int64
	first := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var env protocol.Envelope
		if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
			t.Error(err)
			return
		}
		switch env.Operation {
		case protocol.OpRunsRecordSessionStop:
			metadataCalls.Add(1)
			once.Do(func() { close(first) })
			<-q.Context().Done()
			return
		case protocol.OpRunnerHello:
			verdict := protocol.RunVerdict{RunID: as.RunID, Verdict: protocol.VerdictContinueRetained, Desired: protocol.DesiredRun, RetainedResume: &authority}
			if stopped.Load() {
				verdict = protocol.RunVerdict{RunID: as.RunID, Verdict: protocol.VerdictStop, Desired: protocol.DesiredStopped}
			}
			raw, _ := json.Marshal(protocol.HelloResult{LeaseTTLMillis: 60000, Runs: []protocol.RunVerdict{verdict}})
			_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: raw}})
		default:
			t.Error("Stop must preserve outstanding business facts instead of replaying them", env)
		}
	}))
	defer server.Close()
	r.c.base, r.c.http, r.c.timeout = server.URL, server.Client(), time.Minute
	done := make(chan struct{})
	go func() { defer close(done); r.reconciler(ctx) }()
	defer func() { cancel(); <-done; r.wg.Wait() }()
	reported := make(chan error, 1)
	go func() { reported <- r.report(ctx, as) }()
	defer func() {
		cancel()
		if err := <-reported; err != nil {
			t.Error("authoritative Stop failed to settle local ownership", err)
		}
	}()
	select {
	case <-first:
	case <-ctx.Done():
		t.Fatal("negotiated metadata report was never attempted")
	}
	stopped.Store(true)
	r.requestReconcile()
	for {
		saved, err := r.cfg.Journal.HistoricalRun(ctx, as.RunID)
		if err == nil && saved.Phase == journal.PhaseDone {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("optional session report blocked authoritative Stop")
		case <-time.After(time.Millisecond):
		}
	}
	if metadataCalls.Load() != 1 {
		t.Fatal("optional report was retried during Stop", metadataCalls.Load())
	}
	if pending, err := r.cfg.Journal.SessionStopReport(ctx, as.RunID); err != nil || pending == nil {
		t.Fatal("Stop discarded unknown metadata identity", pending, err)
	}
}

func TestSessionStopMetadataCannotReplacePhysicalStopProof(t *testing.T) {
	r, as, _, _, _ := implementationReconcileFixture(t)
	as.SessionStopReports = true
	if err := r.reportSessionStop(t.Context(), as); !isCompletionPending(err) {
		t.Fatal("session metadata bypassed unknown physical closure", err)
	}
	if env, err := r.cfg.Journal.SessionStopReport(t.Context(), as.RunID); err != nil || env != nil {
		t.Fatal("created optional closed-session fact without stop proof", env, err)
	}
}
