package app

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestConcurrencyRejectedRetryUsesCurrentBinding(t *testing.T) {
	h, sel := concurrencyHostedFixture(t)
	for _, id := range []string{"a", "b"} {
		concurrencyConfigure(t, h, "create", id, 0, protocol.ConfigureConcurrencyPayload{Name: id, Min: 1, Initial: 3, Max: 4})
	}
	binding := protocol.ConfigureConcurrencyPayload{EnvironmentID: sel.Identity.ExecutionEnvironmentID, RuntimeID: sel.Runtime.ID}
	concurrencyConfigure(t, h, "bind", "a", 1, binding)
	message := queueMessage(t, h, "Investigate")
	d, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	turn := store.MainTurn{RequestID: d.RequestID, MessageID: message, ThreadID: "native", Status: store.TurnStarting, Model: sel.Definition.Model, Effort: sel.Definition.Effort, Hosted: &sel}
	for _, status := range []string{store.TurnStarting, store.TurnRejected} {
		turn.Status = status
		if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
			t.Fatal(err)
		}
	}
	concurrencyConfigure(t, h, "bind", "b", 1, binding)
	turn.Status = store.TurnStarting
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	if err := h.app.ObserveNativeStart(t.Context(), d.RequestID, "native", "retry-turn"); err != nil {
		t.Fatal(err)
	}
	if err := h.app.RecordHostedConcurrencyEvent(t.Context(), d.RequestID, protocol.ConcurrencyFeedback{Kind: "congestion", SessionID: "native", TurnID: "retry-turn", Failure: &protocol.AgentFailure{Code: "rate_limit_exceeded", HTTPStatus: 429}}); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]int64{"a": 3, "b": 2} {
		g, err := h.app.ConcurrencyGroup(t.Context(), id)
		if err != nil || g.Current != want {
			t.Fatalf("retry feedback group %s: %+v %v", id, g, err)
		}
	}
}

func TestConcurrencyHostedLivenessRequiresNativeActivity(t *testing.T) {
	for _, active := range []bool{false, true} {
		t.Run(map[bool]string{false: "stale_snapshot", true: "live_events"}[active], func(t *testing.T) {
			h, sel := concurrencyHostedFixture(t)
			concurrencyConfigure(t, h, "create", "shared", 0, protocol.ConfigureConcurrencyPayload{Name: "Shared", Min: 1, Initial: 1, Max: 4})
			concurrencyConfigure(t, h, "bind", "shared", 1, protocol.ConfigureConcurrencyPayload{EnvironmentID: sel.Identity.ExecutionEnvironmentID, RuntimeID: sel.Runtime.ID})
			message := queueMessage(t, h, "Investigate")
			d, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			turn := store.MainTurn{RequestID: d.RequestID, MessageID: message, ThreadID: "native", Status: store.TurnStarting, Model: sel.Definition.Model, Effort: sel.Definition.Effort, Hosted: &sel}
			if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
				t.Fatal(err)
			}
			if err := h.app.ObserveNativeStart(t.Context(), d.RequestID, "native", "turn"); err != nil {
				t.Fatal(err)
			}
			if err := h.app.tickConcurrency(t.Context(), true); err != nil {
				t.Fatal(err)
			}
			for i := 1; i <= int(2*RunnerOfflineAfter/time.Second); i++ {
				h.clock.Advance(time.Second)
				// An unrelated thread cannot keep a stale execution healthy.
				thread := "unrelated"
				if active {
					thread = "native"
				}
				if i%15 == 0 {
					if err := h.app.ObserveConcurrencyActivity(t.Context(), "main", thread, "turn"); err != nil {
						t.Fatal(err)
					}
				}
				if err := h.app.tickConcurrency(t.Context(), true); err != nil {
					t.Fatal(err)
				}
			}
			g, err := h.app.ConcurrencyGroup(t.Context(), "shared")
			if err != nil || g.FeedbackHealthy != active || g.Occupied != 1 || active && g.SaturatedMillis != (2*RunnerOfflineAfter).Milliseconds() || !active && g.SaturatedMillis != 0 {
				t.Fatalf("native feedback active=%v: %+v %v", active, g, err)
			}
		})
	}
}

func TestConcurrencyCompactUnknownAndEarlyCompletion(t *testing.T) {
	h, sel := concurrencyHostedFixture(t)
	concurrencyConfigure(t, h, "create", "shared", 0, protocol.ConfigureConcurrencyPayload{Name: "Shared", Min: 1, Initial: 1, Max: 4})
	concurrencyConfigure(t, h, "bind", "shared", 1, protocol.ConfigureConcurrencyPayload{EnvironmentID: sel.Identity.ExecutionEnvironmentID, RuntimeID: sel.Runtime.ID})
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_compactions(operation_id,conversation_id,status,source,before_json,created_at,updated_at) VALUES('compact-test','main','queued','manual','{}',0,0)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.app.SetCompactState(t.Context(), "compact-test", "native", "starting", &sel); err != nil {
		t.Fatal(err)
	}
	if err := h.app.SetCompactState(t.Context(), "compact-test", "native", "unknown"); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(time.Hour)
	if err := h.app.tickConcurrency(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.AcquireAuxiliaryConcurrency(t.Context(), "main", &sel); !errors.Is(err, ErrConcurrencyCapacity) {
		t.Fatalf("unknown compact released capacity: %v", err)
	}
	if err := h.app.CompleteNativeCompact(t.Context(), "main", "native", "compact-item"); err != nil {
		t.Fatal(err)
	}
	// A completion notification can precede the compact RPC response.
	if err := h.app.SetCompactState(t.Context(), "compact-test", "native", "running"); err != nil {
		t.Fatal(err)
	}
	g, err := h.app.ConcurrencyGroup(t.Context(), "shared")
	if err != nil || g.Occupied != 0 {
		t.Fatalf("late RPC response resurrected completed compact: %+v %v", g, err)
	}
	if _, err := h.app.AcquireAuxiliaryConcurrency(t.Context(), "main", &sel); err != nil {
		t.Fatalf("compact completion did not release capacity: %v", err)
	}
}

func TestConcurrencyCompactFeedbackAndCooldown(t *testing.T) {
	h, sel := concurrencyHostedFixture(t)
	concurrencyConfigure(t, h, "create", "shared", 0, protocol.ConfigureConcurrencyPayload{Name: "Shared", Min: 1, Initial: 2, Max: 4})
	concurrencyConfigure(t, h, "bind", "shared", 1, protocol.ConfigureConcurrencyPayload{EnvironmentID: sel.Identity.ExecutionEnvironmentID, RuntimeID: sel.Runtime.ID})
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_compactions(operation_id,conversation_id,status,source,before_json,created_at,updated_at) VALUES('compact-test','main','queued','manual','{}',0,0)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.app.SetCompactState(t.Context(), "compact-test", "native", "starting", &sel); err != nil {
		t.Fatal(err)
	}
	if err := h.app.RecordHostedConcurrencyEvent(t.Context(), "", protocol.ConcurrencyFeedback{Kind: "congestion", SessionID: "native", TurnID: "compact-turn", Failure: &protocol.AgentFailure{Code: "rate_limit_exceeded", HTTPStatus: 429}}); err != nil {
		t.Fatal(err)
	}
	if err := h.app.SetCompactState(t.Context(), "compact-test", "native", "failed"); err != nil {
		t.Fatal(err)
	}
	g, err := h.app.ConcurrencyGroup(t.Context(), "shared")
	if err != nil || g.Current != 1 || g.CooldownUntil <= h.clock.Now().UnixMilli() || g.Occupied != 0 {
		t.Fatalf("compact feedback: %+v %v", g, err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_compactions(operation_id,conversation_id,status,source,before_json,created_at,updated_at) VALUES('compact-next','main','queued','manual','{}',0,0)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.app.SetCompactState(t.Context(), "compact-next", "native", "starting", &sel); !errors.Is(err, ErrConcurrencyCapacity) {
		t.Fatalf("compact bypassed cooldown: %v", err)
	}
	if state, err := h.app.CompactState(t.Context(), "compact-next"); err != nil || state != "queued" {
		t.Fatalf("capacity rejection must retain queued state: %s %v", state, err)
	}
	if h.count("agent_compaction_runtimes") != 1 {
		t.Fatal("rejected compact admission left a runtime pin outside its transaction")
	}

}
func TestConcurrencyRunnerSamplingUsesObservationTime(t *testing.T) {
	h, r, p, hash := inventorySetup(t)
	if op := h.exec(runner("linux-1"), inventoryRequest(h, r, p, hash, 1, 0, "first"), RegisterAgentInventory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	r.Capabilities = append(r.Capabilities, protocol.CapabilityAgentExecution, protocol.CapabilityAdaptiveConcurrency)
	r.JournalVersion = 12
	for _, agent := range p.Agents {
		r.AgentExecutors = append(r.AgentExecutors, protocol.AgentExecutor{Available: true, Pin: protocol.RunExecutionPin{Identity: agent.Identity, AgentRevision: agent.Revision, Runtime: agent.Binding, InventoryRevision: 1, SnapshotID: "first", PublicSHA256: hash, Profile: protocol.ExecutionProfile{Model: agent.RequestedModel, Effort: agent.RequestedEffort}, Worker: protocol.WorkerAppServer, BackendID: "git"}})
	}
	r.Sequence++
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	concurrencyConfigure(t, h, "create", "shared", 0, protocol.ConfigureConcurrencyPayload{Name: "Shared", Min: 1, Initial: 1, Max: 4})
	concurrencyConfigure(t, h, "bind", "shared", 1, protocol.ConfigureConcurrencyPayload{EnvironmentID: "linux-1", RuntimeID: "worker"})
	spec := testSpec
	spec.AgentSelection = &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer"}, Acceptance: protocol.AgentRequirement{CategoryID: "reviewer"}}
	h.createTask(owner, createTaskPayload{Title: "first", Spec: spec})
	op := h.exec(runner("linux-1"), h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{WorkspaceProtocol: 1, Runtime: r}, nil), Claim{})
	var out claimResult
	if err := json.Unmarshal(op.Result, &out); err != nil || out.Assignment.RunID == "" {
		t.Fatal(op, err)
	}
	run := out.Assignment.RunID
	// This is the run's confirmed native-start fact.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		return tx.UpdateRunObserved(t.Context(), run, protocol.RunRunning, h.clock.Now().UnixMilli())
	}); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(protocol.ConcurrencyFeedback{Kind: "running", SessionID: "native", TurnID: "turn"})
	if _, err := h.app.RecordRunEvents(t.Context(), runPrincipal(run, protocol.RoleImplement), run, []protocol.RunEvent{{Seq: 1, Kind: "worker.concurrency", Body: b}}); err != nil {
		t.Fatal(err)
	}
	if err := h.app.tickConcurrency(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	initial, err := h.app.ConcurrencyGroup(t.Context(), "shared")
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 20; i++ {
		h.clock.Advance(time.Second)
		if i%15 == 0 {
			r.Sequence++
			if _, err := h.app.RuntimeHeartbeat(t.Context(), runner("linux-1"), protocol.HeartbeatPayload{Runtime: r}); err != nil {
				t.Fatal(err)
			}
		}
		if err := h.app.tickConcurrency(t.Context(), true); err != nil {
			t.Fatal(err)
		}
	}
	g, err := h.app.ConcurrencyGroup(t.Context(), "shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("initial observation=%d, after 20s=%+v", initial.ObservationSince, g)
	if g.ObservationSince != initial.ObservationSince || g.SaturatedMillis != 20000 {
		t.Fatalf("a healthy confirmed running execution lost its continuous observation: start=%d final=%+v", initial.ObservationSince, g)
	}
}
func TestConcurrencyHostedSuccessfulRetryCountsSuccess(t *testing.T) {
	h, sel := concurrencyHostedFixture(t)
	concurrencyConfigure(t, h, "create", "shared", 0, protocol.ConfigureConcurrencyPayload{Name: "Shared", Min: 1, Initial: 2, Max: 4})
	concurrencyConfigure(t, h, "bind", "shared", 1, protocol.ConfigureConcurrencyPayload{EnvironmentID: sel.Identity.ExecutionEnvironmentID, RuntimeID: sel.Runtime.ID})
	message := queueMessage(t, h, "Investigate")
	d, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	turn := store.MainTurn{RequestID: d.RequestID, MessageID: message, ThreadID: "native", Status: store.TurnStarting, Model: sel.Definition.Model, Effort: sel.Definition.Effort, Hosted: &sel}
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	turn.Status = store.TurnStarted
	turn.TurnID = "turn"
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	failure := &protocol.AgentFailure{Code: "rate_limit_exceeded", HTTPStatus: 429, WillRetry: true}
	if err := h.app.RecordHostedConcurrencyEvent(t.Context(), d.RequestID, protocol.ConcurrencyFeedback{Kind: "congestion", SessionID: "native", TurnID: "turn", Failure: failure}); err != nil {
		t.Fatal(err)
	}
	// mainthread keeps the last retry diagnostic in captureFailure even on success.
	h.clock.Advance(31 * time.Second)
	turn.Status = store.TurnCompleted
	turn.Failure = failure
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	g, err := h.app.ConcurrencyGroup(t.Context(), "shared")
	if err != nil {
		t.Fatal(err)
	}
	if g.SuccessfulTurns != 1 {
		t.Fatalf("successful native completion was dropped: %+v", g)
	}
}

func TestConcurrencySuccessfulRetryDoesNotInventCongestion(t *testing.T) {
	h, sel := concurrencyHostedFixture(t)
	concurrencyConfigure(t, h, "create", "shared", 0, protocol.ConfigureConcurrencyPayload{Name: "Shared", Min: 1, Initial: 3, Max: 4})
	concurrencyConfigure(t, h, "bind", "shared", 1, protocol.ConfigureConcurrencyPayload{EnvironmentID: sel.Identity.ExecutionEnvironmentID, RuntimeID: sel.Runtime.ID})
	message := queueMessage(t, h, "Investigate")
	d, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	turn := store.MainTurn{RequestID: d.RequestID, MessageID: message, ThreadID: "native", Status: store.TurnStarting, Model: sel.Definition.Model, Effort: sel.Definition.Effort, Hosted: &sel}
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	turn.Status = store.TurnStarted
	turn.TurnID = "turn"
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	failure := &protocol.AgentFailure{Code: "responseStreamDisconnected", HTTPStatus: 503, WillRetry: true}
	if err := h.app.RecordHostedConcurrencyEvent(t.Context(), d.RequestID, protocol.ConcurrencyFeedback{Kind: "congestion", SessionID: "native", TurnID: "turn", Failure: failure}); err != nil {
		t.Fatal(err)
	}
	turn.Status = store.TurnCompleted
	turn.Failure = failure
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	g, err := h.app.ConcurrencyGroup(t.Context(), "shared")
	if err != nil {
		t.Fatal(err)
	}
	if g.Current != 3 {
		t.Fatalf("successful completion fabricated congestion from a non-congestion retry: %+v", g)
	}
}

func TestConcurrencyWorkerSupportsBindingAfterClaim(t *testing.T) {
	h, r, p, hash := inventorySetup(t)
	if op := h.exec(runner("linux-1"), inventoryRequest(h, r, p, hash, 1, 0, "first"), RegisterAgentInventory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	r.Capabilities = append(r.Capabilities, protocol.CapabilityAgentExecution, protocol.CapabilityAdaptiveConcurrency)
	r.JournalVersion = 12
	for _, agent := range p.Agents {
		r.AgentExecutors = append(r.AgentExecutors, protocol.AgentExecutor{Available: true, Pin: protocol.RunExecutionPin{Identity: agent.Identity, AgentRevision: agent.Revision, Runtime: agent.Binding, InventoryRevision: 1, SnapshotID: "first", PublicSHA256: hash, Profile: protocol.ExecutionProfile{Model: agent.RequestedModel, Effort: agent.RequestedEffort}, Worker: protocol.WorkerAppServer, BackendID: "git"}})
	}
	r.Sequence++
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	spec := testSpec
	spec.AgentSelection = &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer"}, Acceptance: protocol.AgentRequirement{CategoryID: "reviewer"}}
	h.createTask(owner, createTaskPayload{Title: "first", Spec: spec})
	op := h.exec(runner("linux-1"), h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{WorkspaceProtocol: 1, Runtime: r}, nil), Claim{})
	var out claimResult
	if err := json.Unmarshal(op.Result, &out); err != nil || out.Assignment.RunID == "" {
		t.Fatal(op, err)
	}
	if !out.Assignment.ConcurrencyFeedback {
		t.Fatal("capable unbound worker must report feedback before a later binding")
	}
	concurrencyConfigure(t, h, "create", "shared", 0, protocol.ConfigureConcurrencyPayload{Name: "Shared", Min: 1, Initial: 3, Max: 4})
	concurrencyConfigure(t, h, "bind", "shared", 1, protocol.ConfigureConcurrencyPayload{EnvironmentID: "linux-1", RuntimeID: "worker"})
	feedback, _ := json.Marshal(protocol.ConcurrencyFeedback{Kind: "congestion", SessionID: "native", TurnID: "turn", Failure: &protocol.AgentFailure{Code: "rate_limit_exceeded", HTTPStatus: 429}})
	if _, err := h.app.RecordRunEvents(t.Context(), runPrincipal(out.Assignment.RunID, protocol.RoleImplement), out.Assignment.RunID, []protocol.RunEvent{{Seq: 1, Kind: "worker.concurrency", Body: feedback}}); err != nil {
		t.Fatal(err)
	}
	g, err := h.app.ConcurrencyGroup(t.Context(), "shared")
	if err != nil || g.Occupied != 1 || g.Current != 2 {
		t.Fatalf("late-bound worker feedback: %+v %v", g, err)
	}
}
