package runner

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

type retainedStrictFinalizationProbe struct {
	*retainedStrictWorkerProbe
	finalizing atomic.Int64
	completed  atomic.Int64
}

func (*retainedStrictFinalizationProbe) StrictRetainedResume() {}
func (p *retainedStrictFinalizationProbe) ReleaseFinalizing(context.Context, string, string, int64, bool, func(string) error) error {
	p.finalizing.Add(1)
	return nil
}
func (p *retainedStrictFinalizationProbe) ReleaseCompleted(context.Context, string, string, int64, bool) error {
	p.completed.Add(1)
	return nil
}

type retainedStrictErrorWorker struct{ events chan harness.WorkerEvent }

func (w retainedStrictErrorWorker) Events() <-chan harness.WorkerEvent { return w.events }
func (retainedStrictErrorWorker) Wait() (harness.WorkerResult, error) {
	return harness.WorkerResult{}, errors.New("original native attachment observation failed")
}

// The server accepts an original request under its original ID even when its
// business result is still pending. Transport success is not terminal proof.
type retainedStrictRPCProbe struct {
	mu       sync.Mutex
	requests []protocol.Envelope
}

func (p *retainedStrictRPCProbe) snapshot() []protocol.Envelope {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]protocol.Envelope(nil), p.requests...)
}

func retainedStrictRegressionServer(t *testing.T, r *Runner, verdict *protocol.RunVerdict, releaseStatus protocol.OpStatus) *retainedStrictRPCProbe {
	t.Helper()
	probe := &retainedStrictRPCProbe{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var env protocol.Envelope
		if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		probe.mu.Lock()
		probe.requests = append(probe.requests, env)
		probe.mu.Unlock()
		status := protocol.OpSucceeded
		var result any = map[string]any{}
		switch env.Operation {
		case protocol.OpRunnerHello:
			if verdict == nil {
				t.Error("unexpected hello")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			result = protocol.HelloResult{LeaseTTLMillis: 60000, Runs: []protocol.RunVerdict{*verdict}}
		case protocol.OpRunsRelease:
			status = releaseStatus
			if status == protocol.OpAccepted {
				result = map[string]any{"business_outcome": "pending_reconciliation"}
			}
		case protocol.OpRunnerEvents, protocol.OpRunsSubmitArtifact:
		default:
			t.Errorf("unexpected strict continuation RPC: %s", env.Operation)
		}
		raw, err := json.Marshal(result)
		if err != nil {
			t.Error(err)
			return
		}
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{
			RequestID: env.RequestID, Status: status, Result: raw}})
	}))
	r.c.base, r.c.http = server.URL, server.Client()
	t.Cleanup(server.Close)
	return probe
}

func retainedStrictOriginalEvent(t *testing.T, r *Runner, id string) []protocol.RunEvent {
	t.Helper()
	if err := r.cfg.Journal.AppendEvent(t.Context(), id, protocol.RunEvent{
		NativeID: "original-native-event", Kind: "turn.observed", Body: json.RawMessage(`{"status":"unknown"}`),
	}); err != nil {
		t.Fatal(err)
	}
	events, err := r.cfg.Journal.PendingEvents(t.Context(), id, 100)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func retainedStrictRegressionSnapshot(t *testing.T, j *journal.Journal, id string) (journal.Run, []journal.Request) {
	t.Helper()
	run, err := j.Run(t.Context(), id)
	if errors.Is(err, journal.ErrNotFound) {
		run, err = j.HistoricalRun(t.Context(), id)
	}
	if err != nil {
		t.Fatal(err)
	}
	requests, err := j.Requests(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return run, requests
}

func TestRetainedStrictRegressionHelloCannotSettleUnvalidatedRelease(t *testing.T) {
	r, as, _ := retainedStrictFixture(t)
	if err := r.cfg.Journal.SetPhase(t.Context(), as.RunID, journal.PhaseReleasing); err != nil {
		t.Fatal(err)
	}
	before, requests := retainedStrictSnapshot(t, r.cfg.Journal, as.RunID)
	events := retainedStrictOriginalEvent(t, r, as.RunID)
	workers := &retainedStrictFinalizationProbe{retainedStrictWorkerProbe: &retainedStrictWorkerProbe{}}
	r.cfg.Workers = workers
	authority := protocol.RetainedRunResume{RequestID: "independent-continuation", Mode: "attach"}
	rpc := retainedStrictRegressionServer(t, r, &protocol.RunVerdict{RunID: as.RunID,
		Verdict: protocol.VerdictContinueRetained, Desired: protocol.DesiredRun, RetainedResume: &authority}, protocol.OpAccepted)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	// Rejecting this verdict or retaining it are both safe; neither may run a
	// finalizer before the original native execution has been validated.
	_ = r.freshHello(ctx)
	r.wg.Wait()
	saved, pending := retainedStrictRegressionSnapshot(t, r.cfg.Journal, as.RunID)
	remaining, err := r.cfg.Journal.PendingEvents(t.Context(), as.RunID, 100)
	if err != nil {
		t.Fatal(err)
	}
	sent := rpc.snapshot()
	spaces := r.cfg.Workspaces.(*retainedStrictWorkspaceProbe)
	if !reflect.DeepEqual(saved, before) || !reflect.DeepEqual(pending, requests) ||
		!reflect.DeepEqual(remaining, events) || len(sent) != 1 || sent[0].Operation != protocol.OpRunnerHello ||
		workers.starts.Load() != 0 || workers.finalizing.Load() != 0 || workers.completed.Load() != 0 ||
		workers.releases.Load() != 0 || spaces.finishes.Load() != 0 {
		t.Fatalf("unvalidated release was resumed or settled: phase=%s fence=%t stopped=%t requests=%d events=%d RPCs=%d finalizing=%d completed=%d cleanup=%d",
			saved.Phase, saved.Fenced, saved.StoppedConfirmed, len(pending), len(remaining), len(sent),
			workers.finalizing.Load(), workers.completed.Load(), spaces.finishes.Load())
	}
}

func TestRetainedStrictRegressionAcceptedReleaseRemainsJournaled(t *testing.T) {
	r, as, _ := retainedStrictFixture(t)
	authority := protocol.RetainedRunResume{RequestID: "independent-continuation", Mode: "attach"}
	if err := r.cfg.Journal.SetRetainedResume(t.Context(), as.RunID, authority); err != nil {
		t.Fatal(err)
	}
	if err := r.cfg.Journal.SetPhase(t.Context(), as.RunID, journal.PhaseReleasing); err != nil {
		t.Fatal(err)
	}
	if err := r.cfg.Journal.ConfirmStopped(t.Context(), as.RunID); err != nil {
		t.Fatal(err)
	}
	before, requests := retainedStrictSnapshot(t, r.cfg.Journal, as.RunID)
	rpc := retainedStrictRegressionServer(t, r, nil, protocol.OpAccepted)
	_ = r.report(t.Context(), as)
	saved, pending := retainedStrictRegressionSnapshot(t, r.cfg.Journal, as.RunID)
	sent := rpc.snapshot()
	spaces := r.cfg.Workspaces.(*retainedStrictWorkspaceProbe)
	if !reflect.DeepEqual(saved, before) || !reflect.DeepEqual(pending, requests) || len(sent) != 1 ||
		!reflect.DeepEqual(sent[0], requests[0].Envelope) || spaces.finishes.Load() != 0 {
		t.Fatalf("Accepted/pending_reconciliation was treated as terminal: phase=%s requests=%d RPCs=%d cleanup=%d",
			saved.Phase, len(pending), len(sent), spaces.finishes.Load())
	}
}

func TestRetainedStrictRegressionOrdinaryAttachmentErrorsStayPending(t *testing.T) {
	for _, stage := range []string{"start", "wait"} {
		t.Run(stage, func(t *testing.T) {
			r, as, _ := retainedStrictFixture(t)
			authority := protocol.RetainedRunResume{RequestID: "independent-continuation", Mode: "attach"}
			if err := r.cfg.Journal.SetRetainedResume(t.Context(), as.RunID, authority); err != nil {
				t.Fatal(err)
			}
			workers := &retainedStrictFinalizationProbe{retainedStrictWorkerProbe: &retainedStrictWorkerProbe{}}
			workers.start = func(_ context.Context, spec harness.WorkerSpec) (harness.Worker, error) {
				if spec.RetainedResume == nil || *spec.RetainedResume != authority || spec.Thread != "original-thread" {
					t.Error("original strict identity was lost")
				}
				if stage == "start" {
					return nil, errors.New("original bridge socket could not be opened")
				}
				events := make(chan harness.WorkerEvent)
				close(events)
				return retainedStrictErrorWorker{events: events}, nil
			}
			r.cfg.Workers = workers
			before, requests := retainedStrictSnapshot(t, r.cfg.Journal, as.RunID)
			events := retainedStrictOriginalEvent(t, r, as.RunID)
			err := r.work(t.Context(), t.Context(), as, &active{as: as, desired: protocol.DesiredRun, resume: make(chan struct{}, 1)})
			if !isCompletionPending(err) {
				t.Errorf("ordinary strict %s error became a business failure: %v", stage, err)
			}
			saved, pending := retainedStrictSnapshot(t, r.cfg.Journal, as.RunID)
			remaining, err := r.cfg.Journal.PendingEvents(t.Context(), as.RunID, 100)
			if err != nil {
				t.Fatal(err)
			}
			before.Attempts++
			if !reflect.DeepEqual(saved, before) || !reflect.DeepEqual(pending, requests) ||
				!reflect.DeepEqual(remaining, events) || workers.starts.Load() != 1 ||
				workers.finalizing.Load() != 0 || workers.completed.Load() != 0 || workers.releases.Load() != 0 {
				t.Fatalf("ordinary strict %s error destroyed original facts: run=%+v requests=%d events=%d", stage, saved, len(pending), len(remaining))
			}
		})
	}
}

func TestRetainedStrictRegressionExecuteDoesNotFinalizeAttachmentError(t *testing.T) {
	for _, strict := range []bool{true, false} {
		name := "ordinary-worker"
		if strict {
			name = "strict-attachment"
		}
		t.Run(name, func(t *testing.T) {
			r, as, _ := retainedStrictFixture(t)
			if err := r.cfg.Journal.SetFenced(t.Context(), as.RunID, false); err != nil {
				t.Fatal(err)
			}
			if strict {
				if err := r.cfg.Journal.SetRetainedResume(t.Context(), as.RunID,
					protocol.RetainedRunResume{RequestID: "independent-continuation", Mode: "attach"}); err != nil {
					t.Fatal(err)
				}
			}
			workers := &retainedStrictFinalizationProbe{retainedStrictWorkerProbe: &retainedStrictWorkerProbe{
				start: func(context.Context, harness.WorkerSpec) (harness.Worker, error) {
					return nil, errors.New("original bridge socket could not be opened")
				},
			}}
			r.cfg.Workers = workers
			before, requests := retainedStrictSnapshot(t, r.cfg.Journal, as.RunID)
			events := retainedStrictOriginalEvent(t, r, as.RunID)
			rpc := retainedStrictRegressionServer(t, r, nil, protocol.OpSucceeded)
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			if err := r.execute(ctx, ctx, as.RunID, &active{as: as, desired: protocol.DesiredRun, resume: make(chan struct{}, 1)}); err != nil {
				t.Fatal(err)
			}
			if strict {
				saved, pending := retainedStrictRegressionSnapshot(t, r.cfg.Journal, as.RunID)
				remaining, err := r.cfg.Journal.PendingEvents(t.Context(), as.RunID, 100)
				if err != nil {
					t.Fatal(err)
				}
				before.Attempts++
				if !reflect.DeepEqual(saved, before) || !reflect.DeepEqual(pending, requests) ||
					!reflect.DeepEqual(remaining, events) || len(rpc.snapshot()) != 0 ||
					workers.finalizing.Load() != 0 || workers.completed.Load() != 0 || workers.releases.Load() != 0 {
					t.Fatalf("strict observation error reached terminal finalization: phase=%s stopped=%t requests=%d events=%d RPCs=%d finalizing=%d",
						saved.Phase, saved.StoppedConfirmed, len(pending), len(remaining), len(rpc.snapshot()), workers.finalizing.Load())
				}
			} else {
				saved, err := r.cfg.Journal.HistoricalRun(t.Context(), as.RunID)
				if err != nil || saved.Phase != journal.PhaseDone || workers.finalizing.Load() != 1 || len(rpc.snapshot()) == 0 {
					t.Fatalf("ordinary worker failure lost its existing finalization: phase=%s finalizing=%d err=%v", saved.Phase, workers.finalizing.Load(), err)
				}
			}
		})
	}
}

func TestRetainedStrictRegressionHelloResumesOriginalValidatedResult(t *testing.T) {
	r, as, _ := retainedStrictFixture(t)
	authority := protocol.RetainedRunResume{RequestID: "independent-continuation", Mode: "attach"}
	if err := r.cfg.Journal.SetRetainedResume(t.Context(), as.RunID, authority); err != nil {
		t.Fatal(err)
	}
	if err := r.cfg.Journal.DeleteRequest(t.Context(), "original-business-request"); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(harness.WorkerResult{Summary: "original strict result"})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.cfg.Journal.WorkerDone(t.Context(), as.RunID, body); err != nil {
		t.Fatal(err)
	}
	if err := r.finish(t.Context(), as, journal.PhaseSubmitting, protocol.OpRunsSubmitArtifact, protocol.ArtifactPayload{}); err != nil {
		t.Fatal(err)
	}
	original, found, err := r.cfg.Journal.RequestForRun(t.Context(), as.RunID)
	if err != nil || !found {
		t.Fatal("original result request missing", err)
	}
	workers := &retainedStrictFinalizationProbe{retainedStrictWorkerProbe: &retainedStrictWorkerProbe{}}
	r.cfg.Workers = workers
	rpc := retainedStrictRegressionServer(t, r, &protocol.RunVerdict{RunID: as.RunID,
		Verdict: protocol.VerdictContinueRetained, Desired: protocol.DesiredRun, RetainedResume: &authority}, protocol.OpSucceeded)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := r.freshHello(ctx); err != nil {
		t.Fatal(err)
	}
	r.wg.Wait()
	saved, err := r.cfg.Journal.HistoricalRun(t.Context(), as.RunID)
	sent := rpc.snapshot()
	if err != nil || saved.Phase != journal.PhaseDone || workers.starts.Load() != 0 ||
		workers.completed.Load() != 1 || len(sent) != 2 || !reflect.DeepEqual(sent[1], original.Envelope) {
		t.Fatalf("validated strict result could not finish under original identity: phase=%s starts=%d completed=%d RPCs=%d err=%v",
			saved.Phase, workers.starts.Load(), workers.completed.Load(), len(sent), err)
	}
}
