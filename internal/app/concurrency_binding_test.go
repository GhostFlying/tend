package app

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func boundConcurrencyRunner(t *testing.T) (*harness, *protocol.RunnerRuntime) {
	t.Helper()
	h, r, projection, hash := inventorySetup(t)
	if op := h.exec(runner("linux-1"), inventoryRequest(h, r, projection, hash, 1, 0, "first"), RegisterAgentInventory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	r.Capabilities = append(r.Capabilities, protocol.CapabilityAgentExecution, protocol.CapabilityAdaptiveConcurrency)
	r.JournalVersion = 12
	for _, agent := range projection.Agents {
		r.AgentExecutors = append(r.AgentExecutors, protocol.AgentExecutor{
			Available: true,
			Pin: protocol.RunExecutionPin{
				Identity: agent.Identity, AgentRevision: agent.Revision, Runtime: agent.Binding,
				InventoryRevision: 1, SnapshotID: "first", PublicSHA256: hash, BackendID: "git",
				Profile: protocol.ExecutionProfile{Model: agent.RequestedModel, Effort: agent.RequestedEffort},
				Worker:  protocol.WorkerAppServer,
			},
		})
	}
	r.Sequence++
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	concurrencyConfigure(t, h, "create", "shared", 0, protocol.ConfigureConcurrencyPayload{Name: "Shared", Min: 1, Initial: 1, Max: 4})
	concurrencyConfigure(t, h, "bind", "shared", 1, protocol.ConfigureConcurrencyPayload{EnvironmentID: "linux-1", RuntimeID: "worker"})
	return h, r
}

func enqueueConcurrencyRun(h *harness, title string) {
	spec := testSpec
	spec.AgentSelection = &protocol.TaskAgentSelection{
		Implementation: protocol.AgentRequirement{CategoryID: "implementer"},
		Acceptance:     protocol.AgentRequirement{CategoryID: "reviewer"},
	}
	h.createTask(owner, createTaskPayload{Title: title, Spec: spec})
}

func observeConcurrencyGroup(t *testing.T, h *harness) domain.ConcurrencyState {
	t.Helper()
	var state domain.ConcurrencyState
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		state, err = concurrencyState(t.Context(), tx.Q(), "shared")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return state
}

func reportConcurrencyEvent(t *testing.T, h *harness, as protocol.Assignment, seq int64, feedback protocol.ConcurrencyFeedback) {
	t.Helper()
	body, err := json.Marshal(feedback)
	if err != nil {
		t.Fatal(err)
	}
	res, err := h.app.RecordRunEvents(t.Context(), runPrincipal(as.RunID, as.Role), as.RunID, []protocol.RunEvent{{Seq: seq, Kind: "worker.concurrency", Body: body}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted != 1 {
		t.Fatalf("feedback was not applied: %+v", res)
	}
}

func TestConcurrencyRunBindingPreservesProofWithoutPromotingOnIncompleteFeedback(t *testing.T) {
	h, runtime := boundConcurrencyRunner(t)
	for _, title := range []string{"initial run", "replacement run", "queued demand"} {
		enqueueConcurrencyRun(h, title)
	}
	claim := func() protocol.Assignment {
		t.Helper()
		env := h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{WorkspaceProtocol: 1, Runtime: runtime}, nil)
		as, ok := h.claimWith("linux-1", env)
		if !ok {
			t.Fatal("expected the queued run to be claimed")
		}
		return as
	}
	start := func(as protocol.Assignment) {
		t.Helper()
		if op := h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	tick := func() {
		t.Helper()
		if err := h.app.tickConcurrency(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	begin := h.clock.Now()
	sampleUntil := func(until time.Duration, as protocol.Assignment) {
		t.Helper()
		for h.clock.Now().Before(begin.Add(until)) {
			h.clock.Advance(min(30*time.Second, begin.Add(until).Sub(h.clock.Now())))
			if _, err := h.heartbeat(t.Context(), runner("linux-1"), []protocol.LeaseRef{{RunID: as.RunID, LeaseID: as.LeaseID}}); err != nil {
				t.Fatal(err)
			}
			tick()
		}
	}

	first := claim()
	start(first)
	reportConcurrencyEvent(t, h, first, 1, protocol.ConcurrencyFeedback{Kind: "running", SessionID: "first-native", TurnID: "turn-1"})
	sampleUntil(time.Minute, first)
	reportConcurrencyEvent(t, h, first, 2, protocol.ConcurrencyFeedback{Kind: "success", SessionID: "first-native", TurnID: "turn-1"})
	sampleUntil(8*time.Minute, first)
	if op := h.runAs(first, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "completed observation fixture"}, Release{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}

	second := claim()
	sampleUntil(15*time.Minute-6*time.Second, second)
	start(second)
	tick()
	assertPending := func(s domain.ConcurrencyState) {
		t.Helper()
		g := s.Group
		if g.FeedbackHealthy || g.Running != 0 || g.Occupied != 1 || g.Waiting != 1 || g.Current != 1 || g.ObservationSince != begin.UnixMilli() || g.SaturatedMillis != (8*time.Minute).Milliseconds() || g.SuccessfulTurns != 1 {
			t.Fatalf("pending binding changed prior evidence, counted as running, or promoted: %+v", s)
		}
	}
	assertPending(observeConcurrencyGroup(t, h))
	sampleUntil(15*time.Minute, second)
	assertPending(observeConcurrencyGroup(t, h))

	reportConcurrencyEvent(t, h, second, 1, protocol.ConcurrencyFeedback{Kind: "running", SessionID: "second-native", TurnID: "turn-2"})
	tick()
	if g := observeConcurrencyGroup(t, h).Group; !g.FeedbackHealthy || g.Running != 1 || g.Current != 1 || g.SaturatedMillis != (8*time.Minute).Milliseconds() {
		t.Fatalf("binding should resume from confirmed evidence without counting the handoff: %+v", g)
	}
	sampleUntil(15*time.Minute+6*time.Second, second)
	if g := observeConcurrencyGroup(t, h).Group; g.Current != 2 || g.ObservationSince != h.clock.Now().UnixMilli() || g.SaturatedMillis != 0 || g.SuccessfulTurns != 0 {
		t.Fatalf("expected one promotion after the missing healthy time was replaced: %+v", g)
	}

	history, err := h.app.ConcurrencyHistory(t.Context(), protocol.ConcurrencyQuery{GroupID: "shared", Reason: "stable_demand"})
	if err != nil || len(history.Items) != 1 || history.Items[0].SaturatedMillis != (8*time.Minute).Milliseconds() || !history.Items[0].FeedbackHealthy {
		t.Fatalf("promotion must retain the actual confirmed proof: %+v, %v", history, err)
	}

	// The real worker callback can arrive with an earlier clock reading.
	// Its 429 still reduces the cap and starts cooldown from the sample cursor.
	promotedAt := h.clock.Now()
	h.clock.Advance(-743 * time.Millisecond)
	reportConcurrencyEvent(t, h, second, 2, protocol.ConcurrencyFeedback{
		Kind: "congestion", SessionID: "second-native", TurnID: "turn-2",
		Failure: &protocol.AgentFailure{HTTPStatus: 429},
	})
	state := observeConcurrencyGroup(t, h)
	if state.Group.Current != 1 || state.LastSample != promotedAt.UnixMilli() || state.Group.CooldownUntil != promotedAt.Add(30*time.Second).UnixMilli() || state.Group.ObservationSince != state.Group.CooldownUntil {
		t.Fatalf("late congestion failed to keep the cursor and start cooldown: %+v", state)
	}
	h.clock.Set(promotedAt.Add(10 * time.Second))
	reportConcurrencyEvent(t, h, second, 3, protocol.ConcurrencyFeedback{
		Kind: "congestion", SessionID: "second-native", TurnID: "turn-3",
		Failure: &protocol.AgentFailure{Code: "server_overloaded"},
	})
	state = observeConcurrencyGroup(t, h)
	if state.Group.Current != 1 || state.LastDecrease != promotedAt.UnixMilli() || state.Group.CooldownUntil != promotedAt.Add(40*time.Second).UnixMilli() {
		t.Fatalf("real overload should extend cooldown and coalesce the decrease: %+v", state)
	}
}

func TestConcurrencyClaimTakenBeforeConfigurationUsesWriteTime(t *testing.T) {
	h, runtime := boundConcurrencyRunner(t)
	enqueueConcurrencyRun(h, "queued before configuration")
	env := h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{WorkspaceProtocol: 1, Runtime: runtime}, nil)

	early := h.clock.Now()
	captured, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	var firstClock atomic.Bool
	h.app.now = func() time.Time {
		if firstClock.CompareAndSwap(false, true) {
			close(captured)
			<-release
			return early
		}
		return h.clock.Now()
	}
	type claimReceipt struct {
		operation protocol.Operation
		err       error
	}
	done := make(chan claimReceipt, 1)
	go func() {
		op, err := h.app.Execute(t.Context(), runner("linux-1"), env, Claim{})
		done <- claimReceipt{operation: op, err: err}
	}()
	select {
	case <-captured:
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	h.clock.Advance(743 * time.Millisecond)
	concurrencyConfigure(t, h, "update", "shared", 2, protocol.ConfigureConcurrencyPayload{Name: "Updated", Min: 1, Initial: 1, Max: 4})
	configuredAt := h.clock.Now()
	h.clock.Advance(time.Millisecond)
	writeAt := h.clock.Now()
	unblock()
	result := <-done
	var claim claimResult
	if result.err != nil || result.operation.Status != protocol.OpSucceeded || json.Unmarshal(result.operation.Result, &claim) != nil || claim.Assignment.RunID == "" {
		t.Fatalf("expected a real claim: %+v", result)
	}
	if run := h.run(claim.Assignment.RunID); run.CreatedAt != early.UnixMilli() || run.ObservedState != protocol.RunStarting {
		t.Fatalf("expected early request time on the committed Run: %+v", run)
	}
	state := observeConcurrencyGroup(t, h)
	if state.LastSample != writeAt.UnixMilli() || state.Group.ObservationSince != configuredAt.UnixMilli() || state.Group.Occupied != 1 || state.Group.Running != 0 || state.Group.SaturatedMillis != 0 || !state.Group.FeedbackHealthy {
		t.Fatalf("late claim rolled back the configuration cursor or lost its reservation: %+v", state)
	}
}

func TestConcurrencyBindingGraceDoesNotHideLostFeedback(t *testing.T) {
	for _, scenario := range []string{"runner reports lost run", "runner offline", "binding never arrives"} {
		t.Run(scenario, func(t *testing.T) {
			h, runtime := boundConcurrencyRunner(t)
			claimAndStart := func(title string) protocol.Assignment {
				t.Helper()
				enqueueConcurrencyRun(h, title)
				env := h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{WorkspaceProtocol: 1, Runtime: runtime}, nil)
				as, ok := h.claimWith("linux-1", env)
				if !ok {
					t.Fatal("expected a real run")
				}
				if op := h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				return as
			}
			tick := func() {
				t.Helper()
				if err := h.app.tickConcurrency(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			first := claimAndStart("observed load")
			reportConcurrencyEvent(t, h, first, 1, protocol.ConcurrencyFeedback{Kind: "running", SessionID: "first-native", TurnID: "turn"})
			h.clock.Advance(30 * time.Second)
			tick()
			if op := h.runAs(first, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "completed observation fixture"}, Release{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			second := claimAndStart("pending native binding")
			tick()
			if s := observeConcurrencyGroup(t, h); !s.LastBindingPending || s.Group.SaturatedMillis != 30000 {
				t.Fatalf("fixture should begin with retained evidence: %+v", s)
			}

			if scenario == "runner reports lost run" {
				h.clock.Advance(6 * time.Second)
				runtime.Sequence++
				if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				if run := h.run(second.RunID); run.ObservedState != protocol.RunUnknown {
					t.Fatalf("runner reconciliation should mark the run unknown: %+v", run)
				}
			} else {
				// Tick before expiration to distinguish liveness and binding
				// deadlines from a missing controller sample.
				h.clock.Advance(30 * time.Second)
				if scenario == "binding never arrives" {
					if _, err := h.heartbeat(t.Context(), runner("linux-1"), []protocol.LeaseRef{{RunID: second.RunID, LeaseID: second.LeaseID}}); err != nil {
						t.Fatal(err)
					}
				}
				tick()
				if scenario == "binding never arrives" {
					if s := observeConcurrencyGroup(t, h); !s.LastBindingPending || s.Group.SaturatedMillis != 30000 {
						t.Fatalf("fresh heartbeat within binding grace should retain evidence: %+v", s)
					}
					h.clock.Advance(30*time.Second + time.Millisecond)
					if _, err := h.heartbeat(t.Context(), runner("linux-1"), []protocol.LeaseRef{{RunID: second.RunID, LeaseID: second.LeaseID}}); err != nil {
						t.Fatal(err)
					}
				} else {
					h.clock.Advance(time.Millisecond)
					if n, err := h.app.SweepRunnerPresence(t.Context()); err != nil || n != 1 {
						t.Fatalf("expected the runner to be marked offline: %d, %v", n, err)
					}
				}
			}
			tick()
			if s := observeConcurrencyGroup(t, h); s.LastBindingPending || s.Group.FeedbackHealthy || s.Group.SaturatedMillis != 0 || s.Group.Current != 1 {
				t.Fatalf("real loss of feedback should invalidate proof without a promotion: %+v", s)
			}
		})
	}
}
