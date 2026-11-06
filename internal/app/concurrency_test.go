package app

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func concurrencyConfigure(t *testing.T, h *harness, action, id string, revision int64, p protocol.ConfigureConcurrencyPayload) protocol.Operation {
	t.Helper()
	cmd := ConfigureConcurrency{Action: action}
	op := h.exec(owner, h.env(cmd.Operation(), protocol.Target{Type: "concurrency", ID: id}, p, rev(revision)), cmd)
	if op.Status != protocol.OpSucceeded {
		t.Fatalf("%s: %+v", action, op)
	}
	return op
}
func TestConcurrencyOwnerCASAndHistory(t *testing.T) {
	h := newHarness(t)
	p := protocol.ConfigureConcurrencyPayload{Name: "Shared", Min: 1, Initial: 4, Max: 8}
	create := h.env("concurrency.create", protocol.Target{Type: "concurrency", ID: "shared"}, p, rev(0))
	op := h.exec(owner, create, ConfigureConcurrency{Action: "create"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	p.Max = 3
	p.Initial = 2
	concurrencyConfigure(t, h, "update", "shared", 1, p)
	g, err := h.app.ConcurrencyGroup(t.Context(), "shared")
	if err != nil || g.Current != 3 || g.Revision != 2 {
		t.Fatal(g, err)
	}
	stale := h.exec(owner, h.env("concurrency.update", create.Target, p, rev(1)), ConfigureConcurrency{Action: "update"})
	if stale.Error == nil || stale.Error.Code != protocol.CodeStaleRevision {
		t.Fatal(stale)
	}
	replay := h.exec(owner, create, ConfigureConcurrency{Action: "create"})
	if string(replay.Result) != string(op.Result) {
		t.Fatal("replay changed")
	}
	history, err := h.app.ConcurrencyHistory(t.Context(), protocol.ConcurrencyQuery{Limit: 1})
	if err != nil || len(history.Items) != 1 || !history.More || history.Items[0].Before != 4 || history.Items[0].After != 3 {
		t.Fatal(history, err)
	}
	next, err := h.app.ConcurrencyHistory(t.Context(), protocol.ConcurrencyQuery{After: history.Next})
	if err != nil || len(next.Items) != 1 || next.Items[0].Reason != "initial" {
		t.Fatal(next, err)
	}
	if err := h.app.tickConcurrency(t.Context(), true); err != nil {
		t.Fatal(err)
	}
}
func TestConcurrencyFeedbackAttributionCoalescingAndRetention(t *testing.T) {
	h := newHarness(t)
	concurrencyConfigure(t, h, "create", "shared", 0, protocol.ConfigureConcurrencyPayload{Name: "Shared", Min: 1, Initial: 4, Max: 8})
	now := h.clock.Now().UnixMilli()
	source := protocol.ConcurrencySource{ExecutionID: "run:one", SessionID: "run:one", EnvironmentID: "env", RuntimeID: "worker", AgentID: "agent", IssueID: "issue", IssueTitle: "Original title", RunID: "one"}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO concurrency_bindings VALUES('env','worker','shared')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	feedback := protocol.ConcurrencyFeedback{Kind: "congestion", SessionID: "native", TurnID: "turn", Failure: &protocol.AgentFailure{Code: "rate_limit_exceeded", HTTPStatus: 429, WillRetry: true}}
	for i := 0; i < 2; i++ {
		if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
			return recordConcurrencyFeedback(t.Context(), tx, source, feedback, now)
		}); err != nil {
			t.Fatal(err)
		}
	}
	source.ExecutionID = "run:two"
	feedback.SessionID = "native-two"
	source.SessionID = "run:two"
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		return recordConcurrencyFeedback(t.Context(), tx, source, feedback, now+1000)
	}); err != nil {
		t.Fatal(err)
	}
	g, err := h.app.ConcurrencyGroup(t.Context(), "shared")
	if err != nil || g.Current != 3 {
		t.Fatal(g, err)
	}
	history, err := h.app.ConcurrencyHistory(t.Context(), protocol.ConcurrencyQuery{IssueID: "issue"})
	if err != nil || len(history.Items) != 1 || len(history.Items[0].Sources) != 2 || history.Items[0].Sources[0].IssueTitle != "Original title" {
		t.Fatal(history, err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		s, err := concurrencyState(t.Context(), tx.Q(), "shared")
		if err != nil {
			return err
		}
		s.Group.Current = 2
		return saveConcurrency(t.Context(), tx, s)
	}); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(8 * 24 * time.Hour)
	if err := h.app.tickConcurrency(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if err := h.app.cleanConcurrencyHistory(t.Context()); err != nil {
		t.Fatal(err)
	}
	history, err = h.app.ConcurrencyHistory(t.Context(), protocol.ConcurrencyQuery{})
	if err != nil || len(history.Items) != 0 {
		t.Fatal(history, err)
	}
	// Cleaning display evidence cannot make a final replay count as a new failure.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		return recordConcurrencyFeedback(t.Context(), tx, source, feedback, h.clock.Now().UnixMilli())
	}); err != nil {
		t.Fatal(err)
	}
	g, err = h.app.ConcurrencyGroup(t.Context(), "shared")
	if err != nil || g.Current != 2 {
		t.Fatal(g, err)
	}
}
func TestConcurrencyLedgerReconciliationAndAdmission(t *testing.T) {
	h := newHarness(t)
	concurrencyConfigure(t, h, "create", "shared", 0, protocol.ConfigureConcurrencyPayload{Name: "Shared", Min: 1, Initial: 1, Max: 8})
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO concurrency_bindings VALUES('env','worker','shared')`)
		if err != nil {
			return err
		}
		s := protocol.ConcurrencySource{ExecutionID: "run:a", SessionID: "run:a", EnvironmentID: "env", RuntimeID: "worker"}
		return putConcurrencyExecution(t.Context(), tx, s, "unknown", h.clock.Now().UnixMilli())
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		ok, err := concurrencyAvailable(t.Context(), tx.Q(), "env", "worker", "run:b", h.clock.Now().UnixMilli())
		if ok {
			t.Fatal("unknown permit released")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		s, err := concurrencyState(t.Context(), tx.Q(), "shared")
		if err != nil {
			return err
		}
		if err := refreshConcurrencyLoad(t.Context(), tx, &s, h.clock.Now().UnixMilli()); err != nil {
			return err
		}
		if s.Group.FeedbackHealthy || s.Group.Running != 0 || s.Group.Occupied != 1 {
			t.Fatal(s)
		}
		_, err = tx.Exec(t.Context(), `UPDATE concurrency_executions SET state='stopped'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		ok, err := concurrencyAvailable(t.Context(), tx.Q(), "env", "worker", "run:b", h.clock.Now().UnixMilli())
		if !ok {
			t.Fatal("stopped permit retained")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrencyClaimReservesBeforeNextClaimAndCountsWaiting(t *testing.T) {
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
	h.createTask(owner, createTaskPayload{Title: "second", Spec: spec})
	claim := func() protocol.Assignment {
		op := h.exec(runner("linux-1"), h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{WorkspaceProtocol: 1, Runtime: r}, nil), Claim{})
		var out claimResult
		if err := json.Unmarshal(op.Result, &out); err != nil {
			t.Fatal(err)
		}
		return out.Assignment
	}
	first := claim()
	if first.RunID == "" {
		t.Fatal("first claim empty")
	}
	if second := claim(); second.RunID != "" {
		t.Fatal("second claim exceeded group", second)
	}
	if err := h.app.tickConcurrency(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	g, err := h.app.ConcurrencyGroup(t.Context(), "shared")
	if err != nil || g.Occupied != 1 || g.Running != 0 || g.Waiting != 1 {
		t.Fatal(g, err)
	}
	// An older runner must not receive grouped work; unbinding restores its old admission.
	old := *r
	old.Capabilities = slices.DeleteFunc(slices.Clone(r.Capabilities), func(c string) bool { return c == protocol.CapabilityAdaptiveConcurrency })
	op := h.exec(runner("linux-1"), h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{WorkspaceProtocol: 1, Runtime: &old}, nil), Claim{})
	var out claimResult
	json.Unmarshal(op.Result, &out)
	if out.Assignment.RunID != "" {
		t.Fatal("old capability bypassed group")
	}
}

func TestConcurrencyManagerSummaryBorrowsIdleReservation(t *testing.T) {
	h, r := managerClaimHarness(t)
	r.Capabilities = append(r.Capabilities, protocol.CapabilityAdaptiveConcurrency)
	r.Sequence++
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	concurrencyConfigure(t, h, "create", "shared", 0, protocol.ConfigureConcurrencyPayload{Name: "Shared", Min: 1, Initial: 1, Max: 4})
	concurrencyConfigure(t, h, "bind", "shared", 1, protocol.ConfigureConcurrencyPayload{EnvironmentID: "linux-1", RuntimeID: "worker"})
	issue := createCanonicalOwnerTestIssue(t, h)
	request := h.env(protocol.OpIssueRequestManager, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.RequestIssueManagerPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Delegate"}, rev(issue.Revision))
	if op := h.exec(owner, request, RequestIssueManager{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	op := h.exec(runner("linux-1"), h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{Runtime: r, WorkspaceProtocol: 1}, nil), Claim{})
	var claimed claimResult
	if err := json.Unmarshal(op.Result, &claimed); err != nil {
		t.Fatal(err)
	}
	if claimed.Assignment.RunID == "" {
		t.Fatal(op)
	}
	selected, named, err := h.app.HostedRuntimeSelection(t.Context(), claimed.Assignment.Manager.ConversationID)
	if err != nil || !named {
		t.Fatal(selected, named, err)
	}
	permit, err := h.app.AcquireAuxiliaryConcurrency(t.Context(), claimed.Assignment.Manager.ConversationID, &selected)
	if err != nil || permit == "" {
		t.Fatal("serial preparation deadlocked at budget one", permit, err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		s, err := concurrencyState(t.Context(), tx.Q(), "shared")
		if err != nil {
			return err
		}
		if err := refreshConcurrencyLoad(t.Context(), tx, &s, h.clock.Now().UnixMilli()); err != nil {
			return err
		}
		if s.Group.Occupied != 1 {
			t.Fatal("borrow counted twice", s.Group)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if second, err := h.app.AcquireAuxiliaryConcurrency(t.Context(), claimed.Assignment.Manager.ConversationID, &selected); err != ErrConcurrencyCapacity || second != "" {
		t.Fatal("second fork borrowed same reservation", second, err)
	}
	if err := h.app.ObserveAuxiliaryConcurrency(t.Context(), permit, protocol.ConcurrencyFeedback{SessionID: "fork", TurnID: "summary", Kind: "running"}, false); err != nil {
		t.Fatal(err)
	}
	if err := h.app.ObserveAuxiliaryConcurrency(t.Context(), permit, protocol.ConcurrencyFeedback{SessionID: "fork"}, true); err != nil {
		t.Fatal(err)
	}
	next, err := h.app.AcquireAuxiliaryConcurrency(t.Context(), claimed.Assignment.Manager.ConversationID, &selected)
	if err != nil || next == "" {
		t.Fatal("summary did not return reservation", next, err)
	}
	// Closing a different session cannot release this permit.
	if err := h.app.StopAuxiliaryConcurrency(t.Context(), []string{permit}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.AcquireAuxiliaryConcurrency(t.Context(), claimed.Assignment.Manager.ConversationID, &selected); err != ErrConcurrencyCapacity {
		t.Fatal("unrelated process exit released a permit", err)
	}
}

func concurrencyHostedFixture(t *testing.T) (*harness, hostedconfig.Selection) {
	t.Helper()
	h := newHarness(t)
	inventoryCategory(t, h, "planner", 0, "active")
	op, prepared := acceptHostedFixture(t, h, hostedConfigurationFixture(), 0)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	h.app = New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(prepared))
	selected, named, err := h.app.NamedHostedSelection(t.Context(), "main")
	if err != nil || !named {
		t.Fatal(selected, named, err)
	}
	return h, selected
}
func TestConcurrencyUnboundAuxiliaryIsAdoptedAndRebound(t *testing.T) {
	h, selected := concurrencyHostedFixture(t)
	permit, err := h.app.AcquireAuxiliaryConcurrency(t.Context(), "main", &selected)
	if err != nil || permit == "" {
		t.Fatal(permit, err)
	}
	p := protocol.ConfigureConcurrencyPayload{Name: "A", Min: 1, Initial: 3, Max: 4}
	concurrencyConfigure(t, h, "create", "a", 0, p)
	p.Name = "B"
	concurrencyConfigure(t, h, "create", "b", 0, p)
	binding := protocol.ConfigureConcurrencyPayload{EnvironmentID: selected.Identity.ExecutionEnvironmentID, RuntimeID: selected.Runtime.ID}
	concurrencyConfigure(t, h, "bind", "a", 1, binding)
	g, err := h.app.ConcurrencyGroup(t.Context(), "a")
	if err != nil || g.Occupied != 1 {
		t.Fatal("unbound fork was not adopted", g, err)
	}
	concurrencyConfigure(t, h, "bind", "b", 1, binding)
	feedback := protocol.ConcurrencyFeedback{SessionID: "summary-native", TurnID: "summary-turn", Kind: "congestion", Failure: &protocol.AgentFailure{Code: "rate_limit_exceeded", HTTPStatus: 429}}
	if err := h.app.ObserveAuxiliaryConcurrency(t.Context(), permit, feedback, false); err != nil {
		t.Fatal(err)
	}
	a, err := h.app.ConcurrencyGroup(t.Context(), "a")
	if err != nil || a.Current != 3 {
		t.Fatal("rebound feedback reduced old group", a, err)
	}
	b, err := h.app.ConcurrencyGroup(t.Context(), "b")
	if err != nil || b.Current != 2 || b.Occupied != 1 {
		t.Fatal("new group missed congestion", b, err)
	}
	history, err := h.app.ConcurrencyHistory(t.Context(), protocol.ConcurrencyQuery{EnvironmentID: binding.EnvironmentID, RuntimeID: binding.RuntimeID})
	if err != nil || len(history.Items) < 3 {
		t.Fatal("membership filter lost control records", history, err)
	}
	concurrencyConfigure(t, h, "unbind", "b", b.Revision, binding)
	if err := h.app.ObserveAuxiliaryConcurrency(t.Context(), permit, protocol.ConcurrencyFeedback{SessionID: "summary-native", TurnID: "another-turn", Kind: "congestion", Failure: feedback.Failure}, false); err != nil {
		t.Fatal(err)
	}
	b, err = h.app.ConcurrencyGroup(t.Context(), "b")
	if err != nil || b.Current != 2 {
		t.Fatal("unbound feedback still changed old group", b, err)
	}
}
func TestConcurrencyPendingHostedReceiptRetainsPermit(t *testing.T) {
	h, selected := concurrencyHostedFixture(t)
	concurrencyConfigure(t, h, "create", "shared", 0, protocol.ConfigureConcurrencyPayload{Name: "Shared", Min: 1, Initial: 1, Max: 4})
	concurrencyConfigure(t, h, "bind", "shared", 1, protocol.ConfigureConcurrencyPayload{EnvironmentID: selected.Identity.ExecutionEnvironmentID, RuntimeID: selected.Runtime.ID})
	message := queueMessage(t, h, "Investigate")
	d, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	turn := store.MainTurn{RequestID: d.RequestID, MessageID: message, ThreadID: "native", Status: store.TurnStarting, Model: selected.Definition.Model, Effort: selected.Definition.Effort, Hosted: &selected}
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	turn.Status = store.TurnStarted
	turn.TurnID = "original"
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	if err := h.app.SettleOpenTurns(t.Context(), "native"); err != nil {
		t.Fatal(err)
	}
	if err := h.app.tickConcurrency(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	group, err := h.app.ConcurrencyGroup(t.Context(), "shared")
	if err != nil || group.Occupied != 1 || group.FeedbackHealthy {
		t.Fatal("unreconciled native execution released its permit", group, err)
	}
	if _, err := h.app.AcquireAuxiliaryConcurrency(t.Context(), "main", &selected); err != ErrConcurrencyCapacity {
		t.Fatal("unknown permit could be oversubscribed", err)
	}
}
