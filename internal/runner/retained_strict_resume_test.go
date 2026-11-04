package runner

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

type retainedStrictWorkerProbe struct {
	starts   atomic.Int64
	releases atomic.Int64
	start    func(context.Context, harness.WorkerSpec) (harness.Worker, error)
}

func (p *retainedStrictWorkerProbe) Start(ctx context.Context, spec harness.WorkerSpec) (harness.Worker, error) {
	p.starts.Add(1)
	if p.start != nil {
		return p.start(ctx, spec)
	}
	return nil, harness.ErrRetainedResumePending
}

func (p *retainedStrictWorkerProbe) Release(context.Context, string, string) error {
	p.releases.Add(1)
	return nil
}

type retainedStrictWorkers struct{ *retainedStrictWorkerProbe }

func (retainedStrictWorkers) StrictRetainedResume() {}

type retainedStrictPendingWorker struct{ events chan harness.WorkerEvent }

func (w retainedStrictPendingWorker) Events() <-chan harness.WorkerEvent { return w.events }
func (retainedStrictPendingWorker) Wait() (harness.WorkerResult, error) {
	return harness.WorkerResult{}, harness.ErrRetainedResumePending
}

type retainedStrictWorkspaceProbe struct {
	prepares    atomic.Int64
	reattaches  atomic.Int64
	collections atomic.Int64
	finishes    atomic.Int64
	reattachErr error
}

func (p *retainedStrictWorkspaceProbe) Prepare(context.Context, protocol.Assignment) (Prepared, error) {
	p.prepares.Add(1)
	return Prepared{}, nil
}

func (p *retainedStrictWorkspaceProbe) Reattach(context.Context, protocol.Assignment) (Prepared, error) {
	p.reattaches.Add(1)
	return Prepared{}, p.reattachErr
}

func (p *retainedStrictWorkspaceProbe) Collect(context.Context, protocol.Assignment) (protocol.ArtifactPayload, error) {
	p.collections.Add(1)
	return protocol.ArtifactPayload{}, ErrNoChanges
}

func (p *retainedStrictWorkspaceProbe) Finish(context.Context, protocol.Assignment) error {
	p.finishes.Add(1)
	return nil
}

// Keep ordinary workspace support separate: implementing Prepare does not
// authorize recreating resources for an unresolved strict continuation.
type retainedStrictOrdinaryWorkspaces struct{ probe *retainedStrictWorkspaceProbe }

func (w retainedStrictOrdinaryWorkspaces) Prepare(ctx context.Context, as protocol.Assignment) (Prepared, error) {
	return w.probe.Prepare(ctx, as)
}
func (w retainedStrictOrdinaryWorkspaces) Collect(ctx context.Context, as protocol.Assignment) (protocol.ArtifactPayload, error) {
	return w.probe.Collect(ctx, as)
}
func (w retainedStrictOrdinaryWorkspaces) Finish(ctx context.Context, as protocol.Assignment) error {
	return w.probe.Finish(ctx, as)
}

func retainedStrictFixture(t *testing.T) (*Runner, protocol.Assignment, string) {
	t.Helper()
	clock := testkit.NewClock()
	path := filepath.Join(t.TempDir(), "journal.db")
	j, err := journal.Open(t.Context(), path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	r := New(Config{Journal: j, Worker: protocol.WorkerOrca, Now: clock.Now,
		IDs: testkit.NewIDGen(clock, 7), Workspaces: &retainedStrictWorkspaceProbe{},
		RetryDelay: time.Hour, RequestTimeout: time.Second, Log: slog.New(slog.DiscardHandler)})
	as := protocol.Assignment{Protocol: protocol.CurrentRunnerProtocol, RunID: "retained-run",
		LeaseID: "original-lease", TaskID: "original-task", Role: protocol.RoleImplement,
		Generation: 7, SpecVersion: 3, Desired: protocol.DesiredRun}
	if err := j.AcceptAssignmentFromWorker(t.Context(), as, "", protocol.WorkerOrca); err != nil {
		t.Fatal(err)
	}
	if err := j.SetThread(t.Context(), as.RunID, "original-thread"); err != nil {
		t.Fatal(err)
	}
	if err := j.SetPhase(t.Context(), as.RunID, journal.PhaseStarted); err != nil {
		t.Fatal(err)
	}
	if err := j.SetFenced(t.Context(), as.RunID, true); err != nil {
		t.Fatal(err)
	}
	original := journal.Request{RunID: as.RunID, Envelope: protocol.Envelope{
		RequestID: "original-business-request", Operation: protocol.OpRunsRelease,
		Target: protocol.Target{Type: "run", ID: as.RunID}, Payload: json.RawMessage(`{"reason":"unresolved original outcome"}`)}}
	if err := j.PutRequest(t.Context(), original); err != nil {
		t.Fatal(err)
	}
	return r, as, path
}

func retainedStrictHello(t *testing.T, r *Runner, v protocol.RunVerdict) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var env protocol.Envelope
		if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if env.Operation != protocol.OpRunnerHello {
			t.Errorf("unexpected business request during strict continuation: %s", env.Operation)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var p protocol.HelloPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil || p.Runtime == nil ||
			!slices.Contains(p.Runtime.Capabilities, protocol.CapabilityStrictRetainedResume) {
			t.Errorf("strict capability was not advertised: %v", err)
		}
		result, err := json.Marshal(protocol.HelloResult{LeaseTTLMillis: 60000, Runs: []protocol.RunVerdict{v}})
		if err != nil {
			t.Error(err)
			return
		}
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{
			RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: result}})
	}))
	r.c.base, r.c.http = server.URL, server.Client()
	t.Cleanup(server.Close)
	return server
}

func retainedStrictSnapshot(t *testing.T, j *journal.Journal, id string) (journal.Run, []journal.Request) {
	t.Helper()
	run, err := j.Run(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	requests, err := j.Requests(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return run, requests
}

func TestRetainedStrictResumeHelloAuthoritySurvivesRunnerRestart(t *testing.T) {
	r, as, path := retainedStrictFixture(t)
	spaces := r.cfg.Workspaces.(*retainedStrictWorkspaceProbe)
	authority := protocol.RetainedRunResume{RequestID: "independent-continuation", Mode: "attach"}
	before, requests := retainedStrictSnapshot(t, r.cfg.Journal, as.RunID)
	for attempt := 1; attempt <= 2; attempt++ {
		probe := &retainedStrictWorkerProbe{}
		started := make(chan harness.WorkerSpec, 1)
		probe.start = func(ctx context.Context, spec harness.WorkerSpec) (harness.Worker, error) {
			persisted, err := r.cfg.Journal.RetainedResume(ctx, as.RunID)
			if err != nil || persisted == nil || *persisted != authority {
				t.Errorf("worker entered before authority was durable: %v %v", persisted, err)
			}
			started <- spec
			return nil, harness.ErrRetainedResumePending
		}
		r.cfg.Workers = retainedStrictWorkers{probe}
		v := protocol.RunVerdict{RunID: as.RunID, Desired: protocol.DesiredRun,
			Verdict: protocol.VerdictContinueRetained, RetainedResume: &authority}
		if attempt == 2 {
			// A normal later reconciliation must not erase the strict boundary.
			v.Verdict, v.RetainedResume = protocol.VerdictContinue, nil
		}
		retainedStrictHello(t, r, v)
		ctx, cancel := context.WithCancel(t.Context())
		if err := r.freshHello(ctx); err != nil {
			cancel()
			r.wg.Wait()
			t.Fatal(err)
		}
		select {
		case spec := <-started:
			if spec.RunID != as.RunID || spec.Thread != before.Thread || spec.HandBack ||
				spec.RetainedResume == nil || *spec.RetainedResume != authority || spec.Attempt != attempt {
				t.Errorf("restart changed the original strict execution: %+v", spec)
			}
		case <-time.After(5 * time.Second):
			t.Error("strict worker was not reached")
		}
		cancel()
		r.wg.Wait()
		saved, pending := retainedStrictSnapshot(t, r.cfg.Journal, as.RunID)
		want := before
		want.Fenced, want.Attempts = false, attempt
		if !reflect.DeepEqual(saved, want) || !reflect.DeepEqual(pending, requests) ||
			probe.starts.Load() != 1 || probe.releases.Load() != 0 ||
			spaces.prepares.Load() != 0 || spaces.reattaches.Load() != int64(attempt) ||
			spaces.collections.Load() != 0 || spaces.finishes.Load() != 0 {
			t.Fatalf("strict pending continuation changed original resources: run=%+v requests=%+v starts=%d releases=%d prepares=%d reattaches=%d",
				saved, pending, probe.starts.Load(), probe.releases.Load(), spaces.prepares.Load(), spaces.reattaches.Load())
		}
		if attempt == 1 {
			if err := r.cfg.Journal.Close(); err != nil {
				t.Fatal(err)
			}
			j, err := journal.Open(t.Context(), path, r.cfg.Now)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = j.Close() })
			cfg := r.cfg
			cfg.Journal = j
			r = New(cfg)
		}
	}
}

func TestRetainedStrictResumeRejectsUnavailableJournalIdentity(t *testing.T) {
	for _, scenario := range []string{"missing-journal", "held-journal", "unstarted-journal", "missing-authority", "missing-request", "wrong-mode"} {
		t.Run(scenario, func(t *testing.T) {
			r, as, _ := retainedStrictFixture(t)
			probe := &retainedStrictWorkerProbe{}
			r.cfg.Workers = retainedStrictWorkers{probe}
			spaces := &retainedWorkspaceProbe{}
			r.cfg.Workspaces = spaces
			authority := &protocol.RetainedRunResume{RequestID: "independent-continuation", Mode: "attach"}
			id := as.RunID
			switch scenario {
			case "missing-journal":
				id = "missing-original-run"
			case "held-journal":
				if err := r.cfg.Journal.SetHeld(t.Context(), as.RunID, true); err != nil {
					t.Fatal(err)
				}
			case "unstarted-journal":
				if err := r.cfg.Journal.SetPhase(t.Context(), as.RunID, journal.PhaseClaimed); err != nil {
					t.Fatal(err)
				}
			case "missing-authority":
				authority = nil
			case "missing-request":
				authority.RequestID = ""
			case "wrong-mode":
				authority.Mode = "launch"
			}
			before, requests := retainedStrictSnapshot(t, r.cfg.Journal, as.RunID)
			retainedStrictHello(t, r, protocol.RunVerdict{RunID: id, Verdict: protocol.VerdictContinueRetained,
				Desired: protocol.DesiredRun, RetainedResume: authority})
			if err := r.freshHello(t.Context()); err == nil {
				t.Fatal("unavailable strict identity was accepted")
			}
			r.wg.Wait()
			saved, pending := retainedStrictSnapshot(t, r.cfg.Journal, as.RunID)
			permission, err := r.cfg.Journal.RetainedResume(t.Context(), id)
			if err != nil || permission != nil || !reflect.DeepEqual(saved, before) ||
				!reflect.DeepEqual(pending, requests) || spaces.calls.Load() != 0 ||
				probe.starts.Load() != 0 || probe.releases.Load() != 0 {
				t.Fatalf("rejected identity changed original ownership: permission=%v err=%v run=%+v requests=%+v",
					permission, err, saved, pending)
			}
			if scenario == "missing-journal" {
				if _, err := r.cfg.Journal.Run(t.Context(), id); !errors.Is(err, journal.ErrNotFound) {
					t.Fatalf("missing original journal became a reassignment: %v", err)
				}
			}
		})
	}
}

func TestRetainedStrictResumePreStartGatePreservesOriginalFacts(t *testing.T) {
	for _, scenario := range []string{"missing-thread", "held-journal", "unsupported-workers", "unsupported-workspace", "missing-original-workspace"} {
		t.Run(scenario, func(t *testing.T) {
			r, as, _ := retainedStrictFixture(t)
			spaces := r.cfg.Workspaces.(*retainedStrictWorkspaceProbe)
			probe := &retainedStrictWorkerProbe{}
			r.cfg.Workers = retainedStrictWorkers{probe}
			switch scenario {
			case "missing-thread":
				if err := r.cfg.Journal.SetThread(t.Context(), as.RunID, ""); err != nil {
					t.Fatal(err)
				}
			case "held-journal":
				if err := r.cfg.Journal.SetHeld(t.Context(), as.RunID, true); err != nil {
					t.Fatal(err)
				}
			case "unsupported-workers":
				r.cfg.Workers = probe
			case "unsupported-workspace":
				r.cfg.Workspaces = retainedStrictOrdinaryWorkspaces{probe: spaces}
			case "missing-original-workspace":
				spaces.reattachErr = harness.ErrRetainedResumePending
			}
			authority := protocol.RetainedRunResume{RequestID: "independent-continuation", Mode: "attach"}
			if err := r.cfg.Journal.SetRetainedResume(t.Context(), as.RunID, authority); err != nil {
				t.Fatal(err)
			}
			before, requests := retainedStrictSnapshot(t, r.cfg.Journal, as.RunID)
			a := &active{as: as, desired: protocol.DesiredRun, resume: make(chan struct{}, 1)}
			err := r.work(t.Context(), t.Context(), as, a)
			if !isCompletionPending(err) || !errors.Is(err, harness.ErrRetainedResumePending) {
				t.Fatalf("unavailable strict attachment was not retained: %v", err)
			}
			saved, pending := retainedStrictSnapshot(t, r.cfg.Journal, as.RunID)
			var wantReattaches int64
			if scenario == "missing-original-workspace" {
				wantReattaches = 1
			}
			if !reflect.DeepEqual(saved, before) || !reflect.DeepEqual(pending, requests) ||
				probe.starts.Load() != 0 || probe.releases.Load() != 0 ||
				spaces.prepares.Load() != 0 || spaces.reattaches.Load() != wantReattaches ||
				spaces.collections.Load() != 0 || spaces.finishes.Load() != 0 {
				t.Fatalf("pre-start gate launched, prepared, or settled original work: run=%+v requests=%+v prepares=%d reattaches=%d",
					saved, pending, spaces.prepares.Load(), spaces.reattaches.Load())
			}
		})
	}
}

func TestRetainedStrictResumePendingStartAndWaitDoNotFinalize(t *testing.T) {
	for _, stage := range []string{"start", "wait"} {
		t.Run(stage, func(t *testing.T) {
			r, as, _ := retainedStrictFixture(t)
			spaces := r.cfg.Workspaces.(*retainedStrictWorkspaceProbe)
			authority := protocol.RetainedRunResume{RequestID: "independent-continuation", Mode: "attach"}
			if err := r.cfg.Journal.SetRetainedResume(t.Context(), as.RunID, authority); err != nil {
				t.Fatal(err)
			}
			probe := &retainedStrictWorkerProbe{}
			probe.start = func(_ context.Context, spec harness.WorkerSpec) (harness.Worker, error) {
				if spec.RetainedResume == nil || *spec.RetainedResume != authority || spec.Thread != "original-thread" {
					t.Error("strict attachment identity was lost", spec.RetainedResume, spec.Thread)
				}
				if stage == "start" {
					return nil, harness.ErrRetainedResumePending
				}
				events := make(chan harness.WorkerEvent)
				close(events)
				return retainedStrictPendingWorker{events: events}, nil
			}
			r.cfg.Workers = retainedStrictWorkers{probe}
			before, requests := retainedStrictSnapshot(t, r.cfg.Journal, as.RunID)
			a := &active{as: as, desired: protocol.DesiredRun, resume: make(chan struct{}, 1)}
			err := r.work(t.Context(), t.Context(), as, a)
			if !isCompletionPending(err) || !errors.Is(err, harness.ErrRetainedResumePending) {
				t.Fatalf("strict native %s uncertainty was finalized: %v", stage, err)
			}
			saved, pending := retainedStrictSnapshot(t, r.cfg.Journal, as.RunID)
			before.Attempts++
			if !reflect.DeepEqual(saved, before) || !reflect.DeepEqual(pending, requests) ||
				probe.starts.Load() != 1 || probe.releases.Load() != 0 ||
				spaces.prepares.Load() != 0 || spaces.reattaches.Load() != 1 ||
				spaces.collections.Load() != 0 || spaces.finishes.Load() != 0 {
				t.Fatalf("strict %s uncertainty changed original execution: run=%+v requests=%+v prepares=%d reattaches=%d",
					stage, saved, pending, spaces.prepares.Load(), spaces.reattaches.Load())
			}
		})
	}
}
