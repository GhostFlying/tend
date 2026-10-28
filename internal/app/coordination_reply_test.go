package app

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

var allFactKinds = []string{"goal", "constraint", "authorization", "refusal", "commitment", "question", "suggestion"}

func TestCoordinationThreeWritesAndSameTurnClaim(t *testing.T) {
	h := newHarness(t)
	source, turn := startReviewTurn(t, h)
	before := h.associations(source)
	initial := protocol.InitialWorkItemContext{Coverage: "Owner request and all seven kinds reviewed", Gaps: "Investigation pending", SourceMessageIDs: []string{source}, SourceReviews: []protocol.LogicalTurnReview{{MessageID: source, ContentRevision: before.ContentRevision, AssociationRevision: before.Revision}}, ReviewedKinds: allFactKinds, Facts: []protocol.ContextFact{{Kind: "goal", Scope: "work_item", Body: "Investigate", SourceMessageID: source}}}
	env := h.env("work_items.create", workTarget(""), protocol.CreateWorkItemPayload{WorkItemPlan: workPlan(), InitialContext: &initial, SourceMessageID: source, CommitSeq: 1}, nil)
	env.ParentRequestID = turn.RequestID
	created := h.exec(mainAgent, env, CreateWorkItem{})
	if created.Status != protocol.OpSucceeded {
		t.Fatal(created)
	}
	var w protocol.WorkItem
	if err := json.Unmarshal(created.Result, &w); err != nil {
		t.Fatal(err)
	}
	if !h.associations(source).FactsReviewed {
		t.Fatal("initial sources were not reviewed")
	}
	taskEnv := h.env("tasks.create", protocol.Target{Type: "task"}, createTaskPayload{Title: "Investigate", Spec: testSpec, WorkItemID: w.WorkItemID, WorkItemRevision: w.Revision, StageIDs: []string{"investigate"}, SourceMessageID: source, CommitSeq: 2}, nil)
	taskEnv.ParentRequestID = turn.RequestID
	task := h.exec(mainAgent, taskEnv, CreateTask{})
	if task.Status != protocol.OpSucceeded {
		t.Fatal(task)
	}
	var receipt struct {
		Receipt struct {
			Contexts []struct {
				WorkItemID string `json:"work_item_id"`
				Revision   int64  `json:"context_revision"`
				Ready      bool   `json:"dispatch_ready"`
			}
		}
	}
	if err := json.Unmarshal(task.Result, &receipt); err != nil {
		t.Fatal(err)
	}
	if len(receipt.Receipt.Contexts) != 1 || !receipt.Receipt.Contexts[0].Ready {
		t.Fatal("task write lost reviewed context", string(task.Result))
	}
	v := receipt.Receipt.Contexts[0]
	p := protocol.CoordinationReply{Body: "Investigation is queued.", Purpose: "progress", Disposition: "linked", WorkItemIDs: []string{w.WorkItemID}, Contexts: []protocol.LogicalContextReview{{WorkItemID: v.WorkItemID, Revision: v.Revision}}, ReviewedKinds: allFactKinds, Facts: []protocol.ContextFact{}}
	replyEnv := h.env("conversation.reply", protocol.Target{Type: "logical_turn", ID: turn.RequestID}, p, nil)
	replyEnv.ParentRequestID = turn.RequestID
	result := h.exec(mainAgent, replyEnv, CoordinationReply{})
	if result.Status != protocol.OpSucceeded {
		t.Fatal(result)
	}
	messages, events, facts := h.count("conversation_messages"), h.count("events"), h.count("work_item_context_facts")
	replay := h.exec(mainAgent, replyEnv, CoordinationReply{})
	if string(replay.Result) != string(result.Result) || h.count("conversation_messages") != messages || h.count("events") != events || h.count("work_item_context_facts") != facts {
		t.Fatal("reply replay changed state")
	}
	if _, ok, _ := h.claim("linux-1"); !ok {
		t.Fatal("reviewed task did not run during native turn")
	}
	state, err := h.app.LogicalTurn(t.Context(), turn.RequestID)
	if err != nil || state.Status != "processing" {
		t.Fatal("reply ended logical turn", state, err)
	}
}

func TestCoordinationReplyRollsBackInvalidAuthority(t *testing.T) {
	h := newHarness(t)
	source, turn := startReviewTurn(t, h)
	w := h.createWork(workPlan())
	h.associate(mainAgent, source, w.WorkItemID)
	h.prepareAgent(w.WorkItemID)
	commitFixtureTurn(t, h, turn.RequestID)
	ctx := h.agentContext(w.WorkItemID)
	before := h.count("conversation_messages")
	p := protocol.CoordinationReply{Body: "Claiming new authorization", Purpose: "final", Disposition: "linked", WorkItemIDs: []string{w.WorkItemID}, Contexts: []protocol.LogicalContextReview{{WorkItemID: w.WorkItemID, Revision: ctx.Revision}}, ReviewedKinds: allFactKinds, Facts: []protocol.ContextFact{{WorkItemID: w.WorkItemID, Scope: "work_item", Kind: "authorization", Body: "deploy"}}}
	env := h.env("conversation.reply", protocol.Target{Type: "logical_turn", ID: turn.RequestID}, p, nil)
	env.ParentRequestID = turn.RequestID
	result := h.exec(mainAgent, env, CoordinationReply{})
	expectWorkCode(t, result, protocol.CodeInvalidRequest)
	if h.count("conversation_messages") != before {
		t.Fatal("failed reply leaked a message")
	}
	var ready bool
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		ready, err = store.WorkItemContextReady(t.Context(), tx.Q(), w.WorkItemID)
		return err
	}); err != nil || !ready {
		t.Fatal("failure changed readiness", err)
	}
}

func TestReviewedNewConstraintFencesFrozenTaskUntilSpecRevision(t *testing.T) {
	h := newHarness(t)
	source, turn := startReviewTurn(t, h)
	w := h.createWork(workPlan())
	h.associate(mainAgent, source, w.WorkItemID)
	h.prepareAgent(w.WorkItemID)
	commitFixtureTurn(t, h, turn.RequestID)
	env := h.env("tasks.create", taskTarget(""), createTaskPayload{Title: "Frozen agreement", Spec: testSpec, WorkItemID: w.WorkItemID, WorkItemRevision: w.Revision, StageIDs: []string{"investigate"}, SourceMessageID: source, CommitSeq: 1}, nil)
	env.ParentRequestID = turn.RequestID
	created := h.exec(mainAgent, env, CreateTask{})
	if created.Status != protocol.OpSucceeded {
		t.Fatal(created)
	}
	var identity struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(created.Result, &identity); err != nil {
		t.Fatal(err)
	}
	original := h.storedSpec(identity.TaskID, 1)
	c := h.agentContext(w.WorkItemID)
	fact := protocol.ContextFact{Scope: "work_item", Kind: "constraint", Body: "Retain all original data", SourceMessageID: source}
	add := h.env("work_item_context.fact", workTarget(w.WorkItemID), fact, rev(c.Revision))
	add.ParentRequestID = turn.RequestID
	if op := h.exec(mainAgent, add, ContextCommand{Action: "fact"}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	commitFixtureTurn(t, h, turn.RequestID)
	if _, ok, _ := h.claim("linux-1"); ok {
		t.Fatal("changed semantic inputs ran under old agreement")
	}
	if len(h.storedSpec(identity.TaskID, 1).Constraints) != len(original.Constraints) {
		t.Fatal("old agreement mutated")
	}
	next := original
	next.Constraints = append(next.Constraints, "Retain all original data")
	update := h.env("tasks.update_intent", taskTarget(identity.TaskID), updateIntentPayload{Spec: &next, SourceMessageID: source, CommitSeq: 2}, rev(1))
	update.ParentRequestID = turn.RequestID
	if op := h.exec(mainAgent, update, UpdateIntent{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if _, ok, _ := h.claim("linux-1"); !ok {
		t.Fatal("revised and reviewed agreement stayed blocked")
	}
}

func TestInitialSourceConflictLeavesNoWorkItem(t *testing.T) {
	h := newHarness(t)
	source, turn := startReviewTurn(t, h)
	before := h.count("work_items")
	p := protocol.CreateWorkItemPayload{WorkItemPlan: workPlan(), InitialContext: &protocol.InitialWorkItemContext{SourceMessageIDs: []string{source}, SourceReviews: []protocol.LogicalTurnReview{{MessageID: source, ContentRevision: 99}}, Coverage: "Checked", ReviewedKinds: allFactKinds}, SourceMessageID: source, CommitSeq: 1}
	env := h.env("work_items.create", workTarget(""), p, nil)
	env.ParentRequestID = turn.RequestID
	expectWorkCode(t, h.exec(mainAgent, env, CreateWorkItem{}), protocol.CodeStaleRevision)
	if h.count("work_items") != before || h.count("work_item_context_facts") != 0 {
		t.Fatal("stale source left partial state")
	}
}

func TestRepeatedPrepareAndAssociationKeepExactVersions(t *testing.T) {
	h := newHarness(t)
	source, turn := startReviewTurn(t, h)
	w := h.createWork(workPlan())
	h.associate(mainAgent, source, w.WorkItemID)
	h.prepareAgent(w.WorkItemID)
	commitFixtureTurn(t, h, turn.RequestID)
	before, assoc := h.agentContext(w.WorkItemID), h.associations(source)
	events := h.count("events")
	for range 2 {
		env := h.env("work_item_context.prepare", workTarget(w.WorkItemID), protocol.PrepareWorkItemContextPayload{Coverage: before.Coverage, Gaps: before.Gaps}, rev(before.Revision))
		env.ParentRequestID = turn.RequestID
		if op := h.exec(mainAgent, env, ContextCommand{Action: "prepare"}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		a := h.associate(mainAgent, source, w.WorkItemID)
		if a.Status != protocol.OpSucceeded {
			t.Fatal(a)
		}
	}
	after, next := h.agentContext(w.WorkItemID), h.associations(source)
	if after.Revision != before.Revision || next.Revision != assoc.Revision || !next.FactsReviewed || h.count("events") != events {
		t.Fatal("identical input invalidated review")
	}
}

func TestNoEffectReceiptReplaysOriginalVersions(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	args := protocol.UpdateWorkItemPayload{WorkItemPlan: w.WorkItemPlan}
	env := h.env("work_items.update", workTarget(w.WorkItemID), args, rev(w.Revision))
	first := h.exec(mainAgent, env, UpdateWorkItem{})
	if first.Status != protocol.OpSucceeded || !json.Valid(first.Result) {
		t.Fatal(first)
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(first.Result, &result); err != nil || result["receipt"] == nil {
		t.Fatal("no-effect omitted receipt", err)
	}
	args.Intent += " and document evidence"
	if op := h.exec(mainAgent, h.env("work_items.update", workTarget(w.WorkItemID), args, rev(w.Revision)), UpdateWorkItem{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	replay := h.exec(mainAgent, env, UpdateWorkItem{})
	if string(replay.Result) != string(first.Result) {
		t.Fatal("replay refreshed an old receipt")
	}
}

func TestLegacyUnclassifiedTextBlocksReviewedContext(t *testing.T) {
	h := newHarness(t)
	source, turn := startReviewTurn(t, h)
	w := h.createWork(workPlan())
	h.associate(mainAgent, source, w.WorkItemID)
	h.prepareAgent(w.WorkItemID)
	commitFixtureTurn(t, h, turn.RequestID)
	env := h.env("tasks.create", taskTarget(""), createTaskPayload{Title: "Investigate", Spec: testSpec, WorkItemID: w.WorkItemID, WorkItemRevision: w.Revision, StageIDs: []string{"investigate"}, SourceMessageID: source, CommitSeq: 1}, nil)
	env.ParentRequestID = turn.RequestID
	if op := h.exec(mainAgent, env, CreateTask{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	legacy := h.env("conversation.record_agent_message", protocol.Target{Type: "conversation"}, recordAgentMessagePayload{Body: "Legacy native progress", ReplyTo: source, TurnID: turn.TurnID, ItemID: "legacy"}, nil)
	legacy.ParentRequestID = turn.RequestID
	if op := h.exec(mainAgent, legacy, RecordAgentMessage{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if _, ok, _ := h.claim("linux-1"); ok {
		t.Fatal("unclassified legacy text bypassed dispatch review")
	}
	if c := h.agentContext(w.WorkItemID); c.SyncStatus != "synced" || c.DispatchReady {
		t.Fatal("lifecycle and semantic state conflated", c)
	}
	state, err := h.app.LogicalTurn(t.Context(), turn.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range state.Messages {
		if msg.Disposition == "pending" {
			if op := h.associate(mainAgent, msg.MessageID); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
		}
	}
	commitFixtureTurn(t, h, turn.RequestID)
	if _, ok, _ := h.claim("linux-1"); !ok {
		t.Fatal("reviewed legacy text stayed blocked")
	}
}

func TestPlanContinueExistingPreservesFrozenAgreement(t *testing.T) {
	h := newHarness(t)
	source, turn := startReviewTurn(t, h)
	w := h.createWork(workPlan())
	h.associate(mainAgent, source, w.WorkItemID)
	h.prepareAgent(w.WorkItemID)
	commitFixtureTurn(t, h, turn.RequestID)
	env := h.env("tasks.create", taskTarget(""), createTaskPayload{Title: "Original agreement", Spec: testSpec, WorkItemID: w.WorkItemID, WorkItemRevision: w.Revision, StageIDs: []string{"investigate"}, SourceMessageID: source, CommitSeq: 1}, nil)
	env.ParentRequestID = turn.RequestID
	op := h.exec(mainAgent, env, CreateTask{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var task struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(op.Result, &task); err != nil {
		t.Fatal(err)
	}
	w = h.work(w.WorkItemID)
	w.Intent += "; expansion will be handled in a separate future task"
	p := protocol.UpdateWorkItemPayload{WorkItemPlan: w.WorkItemPlan, TaskDispositions: []protocol.WorkItemTaskDisposition{{TaskID: task.TaskID, SpecVersion: 1, Action: "continue_existing", Reason: "Keep the existing execution agreement unchanged"}}}
	env = h.env("work_items.update", workTarget(w.WorkItemID), p, rev(w.Revision))
	env.ParentRequestID = turn.RequestID
	op = h.exec(mainAgent, env, UpdateWorkItem{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	commitFixtureTurn(t, h, turn.RequestID)
	if _, ok, _ := h.claim("linux-1"); !ok {
		t.Fatal("continue_existing task was blocked after explicitly retaining its existing agreement")
	}
}

func TestSummaryRetainsAssociationOnlyCorrections(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	source, turn := startReviewTurn(t, h)
	w := h.createWork(workPlan())
	h.associate(mainAgent, source, w.WorkItemID)
	h.prepareAgent(w.WorkItemID)
	commitFixtureTurn(t, h, turn.RequestID)
	turn.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.BindMainThread(ctx, "main", mainAgent.Thread, "schema", ""); err != nil {
		t.Fatal(err)
	}
	// Retain an otherwise unchanged, native-covered owner message with a later association change.
	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE outbox SET status='delivered'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	initial, err := h.app.HandoffSummaryInput(ctx, "main", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.SaveHandoffSummary(ctx, store.HandoffSummary{Instance: "main", Snapshot: initial.Snapshot, Body: "Owner source belongs to the original work item", FormatVersion: 3}); err != nil {
		t.Fatal(err)
	}
	other := h.createWork(workPlan())
	op := h.associate(mainAgent, source, other.WorkItemID)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := h.store.Write(ctx, func(tx *store.Tx) error { _, err := tx.Exec(ctx, `UPDATE outbox SET status='delivered'`); return err }); err != nil {
		t.Fatal(err)
	}
	input, err := h.app.HandoffSummaryInput(ctx, "main", 4096)
	if err != nil {
		t.Fatal(err)
	}
	var corrections []protocol.ConversationItem
	if err := json.Unmarshal(input.Observations, &corrections); err != nil {
		t.Fatal(err)
	}
	for _, c := range corrections {
		if c.MessageID == source {
			return
		}
	}
	t.Fatalf("association-only correction absent from D: %s", input.Observations)
}

func TestAcceptanceFencesChangedSourceFacts(t *testing.T) {
	h := newHarness(t)
	source, turn := startReviewTurn(t, h)
	w := h.createWork(workPlan())
	h.associate(mainAgent, source, w.WorkItemID)
	h.prepareAgent(w.WorkItemID)
	commitFixtureTurn(t, h, turn.RequestID)
	env := h.env("tasks.create", taskTarget(""), createTaskPayload{Title: "Frozen agreement", Spec: testSpec, WorkItemID: w.WorkItemID, WorkItemRevision: w.Revision, StageIDs: []string{"investigate"}, SourceMessageID: source, CommitSeq: 1}, nil)
	env.ParentRequestID = turn.RequestID
	created := h.exec(mainAgent, env, CreateTask{})
	if created.Status != protocol.OpSucceeded {
		t.Fatal(created)
	}
	var task struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(created.Result, &task); err != nil {
		t.Fatal(err)
	}
	commitFixtureTurn(t, h, turn.RequestID)
	impl := h.startRun(task.TaskID, protocol.ActionImplement)
	if op := h.exec(runPrincipal(impl, protocol.RoleImplement), h.env("runs.report_started", runTarget(impl), nil, nil), ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.exec(runPrincipal(impl, protocol.RoleImplement), h.env("runs.submit_artifact", runTarget(impl), testArtifact(h.task(task.TaskID).WorkspaceName), nil), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	commitFixtureTurn(t, h, turn.RequestID)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		ready, err := store.WorkItemContextReady(t.Context(), tx.Q(), w.WorkItemID)
		if !ready {
			t.Fatal("context not ready before acceptance claim")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	accept := h.startRun(task.TaskID, protocol.ActionAccept)
	nextSource, nextTurn := source, turn
	c := h.agentContext(w.WorkItemID)
	fact := protocol.ContextFact{Scope: "work_item", Kind: "constraint", Body: "Retain all original data", SourceMessageID: nextSource}
	env = h.env("work_item_context.fact", workTarget(w.WorkItemID), fact, rev(c.Revision))
	env.ParentRequestID = nextTurn.RequestID
	if op := h.exec(mainAgent, env, ContextCommand{Action: "fact"}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	commitFixtureTurn(t, h, nextTurn.RequestID)
	frozen := h.storedSpec(task.TaskID, 1).SharedContext[0].ContextDigest
	var current string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		current, _, err = store.EffectiveContext(t.Context(), tx.Q(), w.WorkItemID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if frozen == "" || frozen == current {
		t.Fatal("fixture did not change the frozen context")
	}
	if op := h.submitVerdict(accept, protocol.VerdictPassed); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.task(task.TaskID).Lifecycle == protocol.TaskCompleted {
		t.Fatal("acceptance completed a task after its frozen context had been invalidated")
	}
}

func TestSourceAssociationInvalidationFencesAcceptance(t *testing.T) {
	h := newHarness(t)
	source, turn := startReviewTurn(t, h)
	w := h.createWork(workPlan())
	h.associate(mainAgent, source, w.WorkItemID)
	h.prepareAgent(w.WorkItemID)
	c := h.agentContext(w.WorkItemID)
	fact := protocol.ContextFact{Scope: "work_item", Kind: "goal", Body: "Investigate", SourceMessageID: source}
	add := h.env("work_item_context.fact", workTarget(w.WorkItemID), fact, rev(c.Revision))
	add.ParentRequestID = turn.RequestID
	if op := h.exec(mainAgent, add, ContextCommand{Action: "fact"}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	commitFixtureTurn(t, h, turn.RequestID)
	env := h.env("tasks.create", taskTarget(""), createTaskPayload{Title: "Frozen agreement", Spec: testSpec, WorkItemID: w.WorkItemID, WorkItemRevision: w.Revision, StageIDs: []string{"investigate"}, SourceMessageID: source, CommitSeq: 1}, nil)
	env.ParentRequestID = turn.RequestID
	op := h.exec(mainAgent, env, CreateTask{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var identity struct {
		TaskID string `json:"task_id"`
	}
	json.Unmarshal(op.Result, &identity)
	implAssignment, claimed, _ := h.claim("linux-1")
	if !claimed {
		t.Fatal("same-turn implementation was not dispatched")
	}
	impl := implAssignment.RunID
	if op := h.exec(runPrincipal(impl, protocol.RoleImplement), h.env("runs.submit_artifact", runTarget(impl), testArtifact(h.task(identity.TaskID).WorkspaceName), nil), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	commitFixtureTurn(t, h, turn.RequestID)
	acceptAssignment, claimed, _ := h.claim("linux-1")
	if !claimed {
		t.Fatal("same-turn acceptance was not dispatched")
	}
	accept := acceptAssignment.RunID
	before := h.storedSpec(identity.TaskID, 1).SharedContext[0].ContextDigest
	if op := h.associate(owner, source); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	after := ""
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		after, _, err = store.EffectiveContext(t.Context(), tx.Q(), w.WorkItemID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	t.Logf("digest unchanged=%v, context prepared=%v, fact needs review=%v", before == after, h.agentContext(w.WorkItemID).Prepared, h.agentContext(w.WorkItemID).Facts[0].NeedsReview)
	if op := h.submitVerdict(accept, protocol.VerdictPassed); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.task(identity.TaskID).Lifecycle == protocol.TaskCompleted {
		t.Fatal("acceptance completed responsibility after owner removed its source association")
	}
}

func TestSummaryUsesEffectiveArtifactPointer(t *testing.T) {
	h := newHarness(t)
	_, task := h.createTask(owner, createTaskPayload{Title: "Artifact provenance", Spec: testSpec})
	old := h.startRun(task.TaskID, protocol.ActionImplement)
	changed := testSpec
	changed.Constraints = []string{"Current agreement"}
	if op := h.exec(owner, h.env("tasks.update_intent", taskTarget(task.TaskID), updateIntentPayload{Spec: &changed}, rev(h.task(task.TaskID).ControlRevision)), UpdateIntent{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	current := h.startRun(task.TaskID, protocol.ActionImplement)
	if op := h.exec(runPrincipal(current, protocol.RoleImplement), h.env("runs.submit_artifact", runTarget(current), testArtifact(h.task(task.TaskID).WorkspaceName), nil), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	expected := h.task(task.TaskID).CurrentArtifactID
	h.clock.Advance(1000000)
	if op := h.exec(runPrincipal(old, protocol.RoleImplement), h.env("runs.submit_artifact", runTarget(old), testArtifact(h.task(task.TaskID).WorkspaceName), nil), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.task(task.TaskID).CurrentArtifactID != expected {
		t.Fatal("fixture changed current artifact")
	}
	if _, err := h.app.BindMainThread(t.Context(), "main", "summary-source", "hash", ""); err != nil {
		t.Fatal(err)
	}
	input, err := h.app.HandoffSummaryInput(t.Context(), "main", 4096)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range input.Ledger {
		var r map[string]any
		json.Unmarshal(raw, &r)
		if r["object_type"] == "artifact" && r["task_id"] == task.TaskID && r["artifact_id"] != expected {
			t.Fatalf("current ledger selected stale artifact %v instead of current %v", r["artifact_id"], expected)
		}
	}
}

func TestLedgerChangeInvalidatesSummaryCoverage(t *testing.T) {
	h := newHarness(t)
	if _, err := h.app.BindMainThread(t.Context(), "main", "summary-source", "hash", ""); err != nil {
		t.Fatal(err)
	}
	w := h.createWork(workPlan())
	before, err := h.app.HandoffSummaryInput(t.Context(), "main", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.SaveHandoffSummary(t.Context(), store.HandoffSummary{Instance: "main", Snapshot: before.Snapshot, Body: "Old work item intent", FormatVersion: 3}); err != nil {
		t.Fatal(err)
	}
	w.Intent = "New current owner intent"
	if op := h.exec(owner, h.env("work_items.update", workTarget(w.WorkItemID), protocol.UpdateWorkItemPayload{WorkItemPlan: w.WorkItemPlan}, rev(w.Revision)), UpdateWorkItem{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	after, err := h.app.HandoffSummaryInput(t.Context(), "main", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if before.Snapshot.Covers(after.Snapshot) {
		t.Fatalf("L changed but v3 source coverage is still identical: version=%d records=%s", after.Snapshot.Version, after.Ledger)
	}
}

func TestScopedProgressCommitmentKeepsDelegatedAgreement(t *testing.T) {
	h := newHarness(t)
	source, turn := startReviewTurn(t, h)
	w := h.createWork(workPlan())
	h.associate(mainAgent, source, w.WorkItemID)
	h.prepareAgent(w.WorkItemID)
	commitFixtureTurn(t, h, turn.RequestID)
	env := h.env("tasks.create", taskTarget(""), createTaskPayload{Title: "Investigate", Spec: testSpec, WorkItemID: w.WorkItemID, WorkItemRevision: w.Revision, StageIDs: []string{"investigate"}, SourceMessageID: source, CommitSeq: 1}, nil)
	env.ParentRequestID = turn.RequestID
	if op := h.exec(mainAgent, env, CreateTask{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	c := h.agentContext(w.WorkItemID)
	p := protocol.CoordinationReply{Body: "Investigation is assigned; I will report its result.", Purpose: "progress", Disposition: "linked", WorkItemIDs: []string{w.WorkItemID}, Contexts: []protocol.LogicalContextReview{{WorkItemID: w.WorkItemID, Revision: c.Revision}}, ReviewedKinds: allFactKinds, Facts: []protocol.ContextFact{{WorkItemID: w.WorkItemID, Scope: "work_item", Kind: "commitment", Body: "Report the investigation result to the owner"}}}
	reply := h.env("conversation.reply", protocol.Target{Type: "conversation"}, p, nil)
	reply.ParentRequestID = turn.RequestID
	if op := h.exec(mainAgent, reply, CoordinationReply{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if _, ok, _ := h.claim("linux-1"); !ok {
		t.Fatal("coordinator reporting commitment invalidated the worker agreement")
	}
}

func TestNoopAssociationAttachesHistoricalReviewSource(t *testing.T) {
	h := newHarness(t)
	source, turn := startReviewTurn(t, h)
	w := h.createWork(workPlan())
	h.associate(mainAgent, source, w.WorkItemID)
	h.prepareAgent(w.WorkItemID)
	commitFixtureTurn(t, h, turn.RequestID)
	turn.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE outbox SET status='delivered'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	msg, delivery := h.say("Review that same source again")
	next := store.MainTurn{RequestID: delivery.RequestID, MessageID: msg, ThreadID: mainAgent.Thread, TurnID: "native-2", Status: store.TurnStarted}
	if err := h.app.PutMainTurn(t.Context(), next); err != nil {
		t.Fatal(err)
	}
	a := h.associations(source)
	env := h.env("conversation.set_associations", protocol.Target{Type: "message", ID: source}, protocol.SetMessageAssociationsPayload{Disposition: "linked", WorkItemIDs: []string{w.WorkItemID}, ContentRevision: a.ContentRevision}, rev(a.Revision))
	env.ParentRequestID = next.RequestID
	if op := h.exec(mainAgent, env, SetMessageAssociations{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	state, err := h.app.LogicalTurn(t.Context(), next.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range state.Messages {
		if m.MessageID == source {
			return
		}
	}
	t.Fatal("explicit source reference no longer attaches the historical message to this turn")
}

func TestDispatchViewReportsFrozenAgreementMismatch(t *testing.T) {
	h := newHarness(t)
	source, turn := startReviewTurn(t, h)
	w := h.createWork(workPlan())
	h.associate(mainAgent, source, w.WorkItemID)
	h.prepareAgent(w.WorkItemID)
	commitFixtureTurn(t, h, turn.RequestID)
	env := h.env("tasks.create", taskTarget(""), createTaskPayload{Title: "Frozen", Spec: testSpec, WorkItemID: w.WorkItemID, WorkItemRevision: w.Revision, StageIDs: []string{"investigate"}, SourceMessageID: source, CommitSeq: 1}, nil)
	env.ParentRequestID = turn.RequestID
	op := h.exec(mainAgent, env, CreateTask{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	c := h.agentContext(w.WorkItemID)
	env = h.env("work_item_context.fact", workTarget(w.WorkItemID), protocol.ContextFact{Scope: "work_item", Kind: "constraint", Body: "Retain data", SourceMessageID: source}, rev(c.Revision))
	env.ParentRequestID = turn.RequestID
	op = h.exec(mainAgent, env, ContextCommand{Action: "fact"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	commitFixtureTurn(t, h, turn.RequestID)
	c = h.agentContext(w.WorkItemID)
	if _, ok, _ := h.claim("linux-1"); ok {
		t.Fatal("fixture unexpectedly dispatched")
	}
	if c.DispatchReady {
		t.Fatalf("dispatch blocked by frozen digest but view claims ready; blockers=%v", c.DispatchBlockers)
	}
}

func TestSourceRemovalWithoutFactsFencesAcceptance(t *testing.T) {
	h := newHarness(t)
	source, turn := startReviewTurn(t, h)
	w := h.createWork(workPlan())
	h.associate(mainAgent, source, w.WorkItemID)
	h.prepareAgent(w.WorkItemID)
	commitFixtureTurn(t, h, turn.RequestID)
	env := h.env("tasks.create", taskTarget(""), createTaskPayload{Title: "Frozen", Spec: testSpec, WorkItemID: w.WorkItemID, WorkItemRevision: w.Revision, StageIDs: []string{"investigate"}, SourceMessageID: source, CommitSeq: 1}, nil)
	env.ParentRequestID = turn.RequestID
	op := h.exec(mainAgent, env, CreateTask{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var v struct {
		TaskID string `json:"task_id"`
	}
	json.Unmarshal(op.Result, &v)
	impl, ok, _ := h.claim("linux-1")
	if !ok {
		t.Fatal("impl not claimed")
	}
	op = h.exec(runPrincipal(impl.RunID, protocol.RoleImplement), h.env("runs.submit_artifact", runTarget(impl.RunID), testArtifact(h.task(v.TaskID).WorkspaceName), nil), SubmitArtifact{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	commitFixtureTurn(t, h, turn.RequestID)
	accept, ok, _ := h.claim("linux-1")
	if !ok {
		t.Fatal("accept not claimed")
	}
	if op := h.associate(owner, source); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.submitVerdict(accept.RunID, protocol.VerdictPassed); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.task(v.TaskID).Lifecycle == protocol.TaskCompleted {
		t.Fatal("task completed after owner removed source classification; no fact rows means unchanged empty digest")
	}
}

func TestWithdrawalInvalidatesCompositeSnapshotSummary(t *testing.T) {
	h := newHarness(t)
	for range 10 {
		h.createWork(workPlan())
	}
	if _, err := h.app.BindMainThread(t.Context(), "main", "source", "schema", ""); err != nil {
		t.Fatal(err)
	}
	snap, err := h.app.HandoffSnapshot(t.Context(), "main")
	if err != nil {
		t.Fatal(err)
	}
	snap.Summary = &store.SummaryCoverage{Complete: true}
	if _, err := h.app.SaveHandoffSummary(t.Context(), store.HandoffSummary{Instance: "main", Snapshot: snap, Body: "Prior authority", FormatVersion: 3}); err != nil {
		t.Fatal(err)
	}
	id := queueMessage(t, h, "withdrawn instruction")
	q := readMessageQueue(t, h, "owner")
	op := h.exec(owner, h.env("conversation.withdraw_message", protocol.Target{Type: "message", ID: id}, protocol.WithdrawMessagePayload{}, rev(q.Items[0].Revision)), WithdrawMessage{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if _, ok, err := h.app.HandoffSummary(t.Context(), "main"); err != nil || ok {
		t.Fatalf("summary still available after withdrawal: available=%v err=%v conversation_version=%d combined_version=%d", ok, err, snap.ConversationVersion, snap.Version)
	}
}

func createFingerprintTask(t *testing.T, h *harness, source string, turn store.MainTurn, w protocol.WorkItem) string {
	t.Helper()
	e := h.env("tasks.create", taskTarget(""), createTaskPayload{Title: "frozen", Spec: testSpec, WorkItemID: w.WorkItemID, WorkItemRevision: w.Revision, StageIDs: []string{"investigate"}, SourceMessageID: source, CommitSeq: 1}, nil)
	e.ParentRequestID = turn.RequestID
	op := h.exec(mainAgent, e, CreateTask{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var out struct {
		TaskID string `json:"task_id"`
	}
	json.Unmarshal(op.Result, &out)
	return out.TaskID
}

func TestNewSharedWorkItemFreezesSourcedContext(t *testing.T) {
	h := newHarness(t)
	source, turn := startReviewTurn(t, h)
	a, b := h.createWork(workPlan()), h.createWork(workPlan())
	h.associate(mainAgent, source, a.WorkItemID, b.WorkItemID)
	h.prepareAgent(a.WorkItemID)
	h.prepareAgent(b.WorkItemID)
	commitFixtureTurn(t, h, turn.RequestID)
	id := createFingerprintTask(t, h, source, turn, a)
	op := h.exec(owner, h.env("work_items.link_task", workTarget(b.WorkItemID), protocol.LinkWorkItemTaskPayload{TaskID: id, StageIDs: []string{"investigate"}}, rev(b.Revision)), LinkWorkItemTask{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	next := h.storedSpec(id, 1)
	next.Goal += " and cover the second concern"
	e := h.env("tasks.update_intent", taskTarget(id), updateIntentPayload{Spec: &next}, rev(h.task(id).ControlRevision))
	e.ParentRequestID = turn.RequestID
	op = h.exec(mainAgent, e, UpdateIntent{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	commitFixtureTurn(t, h, turn.RequestID)
	c := h.agentContext(b.WorkItemID)
	e = h.env("work_item_context.fact", workTarget(b.WorkItemID), protocol.ContextFact{Scope: "work_item", Kind: "constraint", Body: "Do not change production data", SourceMessageID: source}, rev(c.Revision))
	e.ParentRequestID = turn.RequestID
	op = h.exec(mainAgent, e, ContextCommand{Action: "fact"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	commitFixtureTurn(t, h, turn.RequestID)
	frozen := h.storedSpec(id, h.task(id).SpecVersion).SharedContext
	t.Logf("frozen: %+v", frozen)
	if _, ok, _ := h.claim("linux-1"); ok {
		t.Fatal("task dispatched after secondary source facts changed without a spec revision")
	}
}

func TestStaleGenerationCannotChangeTaskControl(t *testing.T) {
	h := newHarness(t)
	source, turn := startReviewTurn(t, h)
	w := h.createWork(workPlan())
	h.associate(mainAgent, source, w.WorkItemID)
	h.prepareAgent(w.WorkItemID)
	commitFixtureTurn(t, h, turn.RequestID)
	id := createFingerprintTask(t, h, source, turn, w)
	old := h.startRun(id, protocol.ActionImplement)
	op := h.exec(owner, h.env("actions.create", taskTarget(id), protocol.CreateActionPayload{Kind: protocol.ActionImplement, Generation: 2}, rev(h.task(id).ControlRevision)), CreateAction{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	c := h.agentContext(w.WorkItemID)
	e := h.env("work_item_context.fact", workTarget(w.WorkItemID), protocol.ContextFact{Scope: "work_item", Kind: "constraint", Body: "Retain original data", SourceMessageID: source}, rev(c.Revision))
	e.ParentRequestID = turn.RequestID
	op = h.exec(mainAgent, e, ContextCommand{Action: "fact"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	op = h.exec(runPrincipal(old, protocol.RoleImplement), h.env("runs.submit_artifact", runTarget(old), testArtifact(h.task(id).WorkspaceName), nil), SubmitArtifact{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if got := h.task(id); got.ControlMode != protocol.ControlManaged {
		t.Fatalf("stale generation changed task control: %+v", got)
	}
}

func TestDuplicateFactRegistersHistoricalReviewScope(t *testing.T) {
	h := newHarness(t)
	source, turn := startReviewTurn(t, h)
	w := h.createWork(workPlan())
	h.associate(mainAgent, source, w.WorkItemID)
	h.prepareAgent(w.WorkItemID)
	fact := protocol.ContextFact{Scope: "work_item", Kind: "constraint", Body: "Keep the original data", SourceMessageID: source}
	c := h.agentContext(w.WorkItemID)
	e := h.env("work_item_context.fact", workTarget(w.WorkItemID), fact, rev(c.Revision))
	e.ParentRequestID = turn.RequestID
	op := h.exec(mainAgent, e, ContextCommand{Action: "fact"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	commitFixtureTurn(t, h, turn.RequestID)
	turn.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE outbox SET status='delivered'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	msg, d := h.say("Use that constraint again")
	next := store.MainTurn{RequestID: d.RequestID, MessageID: msg, ThreadID: mainAgent.Thread, TurnID: "next-native", Status: store.TurnStarted}
	if err := h.app.PutMainTurn(t.Context(), next); err != nil {
		t.Fatal(err)
	}
	c = h.agentContext(w.WorkItemID)
	e = h.env("work_item_context.fact", workTarget(w.WorkItemID), fact, rev(c.Revision))
	e.ParentRequestID = next.RequestID
	op = h.exec(mainAgent, e, ContextCommand{Action: "fact"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	t.Logf("duplicate receipt: %s", op.Result)
	state, err := h.app.LogicalTurn(t.Context(), next.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range state.Messages {
		if m.MessageID == source {
			return
		}
	}
	t.Fatal("duplicate fact did not attach its explicitly cited source to the new logical turn")
}

func TestNewFrozenSecondaryNeedsItsInitialBaseline(t *testing.T) {
	h := newHarness(t)
	source, turn := startReviewTurn(t, h)
	a := h.createWork(workPlan())
	h.associate(mainAgent, source, a.WorkItemID)
	h.prepareAgent(a.WorkItemID)
	commitFixtureTurn(t, h, turn.RequestID)
	id := createFingerprintTask(t, h, source, turn, a)
	commitFixtureTurn(t, h, turn.RequestID)
	turn.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	b := h.createWork(workPlan())
	if op := h.exec(owner, h.env("work_items.link_task", workTarget(b.WorkItemID), protocol.LinkWorkItemTaskPayload{TaskID: id, StageIDs: []string{"investigate"}}, rev(b.Revision)), LinkWorkItemTask{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	next := h.storedSpec(id, 1)
	if op := h.exec(owner, h.env("tasks.update_intent", taskTarget(id), updateIntentPayload{Spec: &next}, rev(h.task(id).ControlRevision)), UpdateIntent{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if _, ok, _ := h.claim("linux-1"); ok {
		t.Fatal("new frozen dependency dispatched without initial source preparation")
	}
	h.prepareAgent(b.WorkItemID)
	if _, ok, _ := h.claim("linux-1"); !ok {
		t.Fatal("prepared unchanged dependency remained blocked")
	}
}

func TestEarlyNativeStartProjectsAdmission(t *testing.T) {
	h := newHarness(t)
	id, d := h.say("Please investigate")
	rec := store.MainTurn{RequestID: d.RequestID, MessageID: id, ThreadID: mainAgent.Thread, Status: store.TurnStarting}
	if err := h.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	if err := h.app.ObserveNativeStart(t.Context(), d.RequestID, mainAgent.Thread, "early-turn"); err != nil {
		t.Fatal(err)
	}
	rec.TurnID, rec.Status = "early-turn", store.TurnStarted
	if err := h.app.PutMainTurn(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		row, err := store.GetConversationItem(t.Context(), tx.Q(), id)
		if err != nil {
			return err
		}
		var blocks int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM conversation_blocks WHERE message_id=? AND notice_only=0`, id).Scan(&blocks); err != nil {
			return err
		}
		t.Logf("message state=%q presentation blocks=%d", row.State, blocks)
		if row.State != string(protocol.MessageProcessing) || blocks != 1 {
			t.Errorf("early callback skipped normal admitted-message projection")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRoutineWorkerStartPreservesExactSourceReview(t *testing.T) {
	h := newHarness(t)
	source, turn := startReviewTurn(t, h)
	w := h.createWork(workPlan())
	h.associate(mainAgent, source, w.WorkItemID)
	h.prepareAgent(w.WorkItemID)
	commitFixtureTurn(t, h, turn.RequestID)
	id := createFingerprintTask(t, h, source, turn, w)
	as, ok, _ := h.claim("linux-1")
	if !ok {
		t.Fatal("claim failed")
	}
	if op := h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	c := h.agentContext(w.WorkItemID)
	if !c.DispatchReady {
		t.Fatalf("routine worker start invalidated review: %+v", c.DispatchBlockers)
	}
	turn.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	l, err := h.app.LogicalTurn(t.Context(), turn.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if l.Status != "completed" || l.RepairAttempts != 0 {
		t.Fatalf("routine start caused needless repair: %+v", l)
	}
	_ = id
}

func TestUnknownOriginRetainsArtifactWithoutAdvancingTask(t *testing.T) {
	h := newHarness(t)
	source, turn := startReviewTurn(t, h)
	w := h.createWork(workPlan())
	h.associate(mainAgent, source, w.WorkItemID)
	h.prepareAgent(w.WorkItemID)
	commitFixtureTurn(t, h, turn.RequestID)
	id := createFingerprintTask(t, h, source, turn, w)
	as, ok, _ := h.claim("linux-1")
	if !ok {
		t.Fatal("same-turn task not claimed")
	}
	if op := h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := h.app.AwaitNativeReceipts(t.Context(), turn.ThreadID); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		reviewed, err := store.WorkItemContextReviewed(t.Context(), tx.Q(), w.WorkItemID)
		if err != nil {
			return err
		}
		ready, err := store.WorkItemContextReady(t.Context(), tx.Q(), w.WorkItemID)
		t.Logf("reviewed=%v dispatchReady=%v", reviewed, ready)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	state, err := h.app.LogicalTurn(t.Context(), turn.RequestID)
	if err != nil || state.Status != "unknown" {
		t.Fatal(state, err)
	}
	if op := h.runAs(as, protocol.OpRunsSubmitArtifact, testArtifact(h.task(id).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if got := h.task(id); got.Lifecycle == protocol.TaskAwaitingAcceptance || got.ControlMode != protocol.ControlPaused {
		t.Fatalf("unknown execution did not fence progression: %+v", got)
	}
	assertUnknownResultEvidence(t, h, id, "artifacts")
}

func TestUnknownOriginRetainsVerdictWithoutCompletingTask(t *testing.T) {
	h := newHarness(t)
	source, turn := startReviewTurn(t, h)
	w := h.createWork(workPlan())
	h.associate(mainAgent, source, w.WorkItemID)
	h.prepareAgent(w.WorkItemID)
	commitFixtureTurn(t, h, turn.RequestID)
	id := createFingerprintTask(t, h, source, turn, w)
	impl, ok, _ := h.claim("linux-1")
	if !ok {
		t.Fatal("no implement claim")
	}
	if op := h.runAs(impl, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.runAs(impl, protocol.OpRunsSubmitArtifact, testArtifact(h.task(id).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	commitFixtureTurn(t, h, turn.RequestID)
	accept, ok, _ := h.claim("linux-1")
	if !ok {
		t.Fatal("no acceptance claim")
	}
	if err := h.app.AwaitNativeReceipts(t.Context(), turn.ThreadID); err != nil {
		t.Fatal(err)
	}
	if op := h.submitVerdict(accept.RunID, protocol.VerdictPassed); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if got := h.task(id); got.Lifecycle == protocol.TaskCompleted || got.ControlMode != protocol.ControlPaused {
		t.Fatalf("unknown execution did not fence acceptance: %+v", got)
	}
	assertUnknownResultEvidence(t, h, id, "acceptances")
}

func assertUnknownResultEvidence(t *testing.T, h *harness, task, table string) {
	t.Helper()
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var count int
		if err := tx.Q().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table+" WHERE task_id=?", task).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			t.Errorf("uncertain result was not retained in %s: %d", table, count)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTaskControlWritesReturnReceiptAndPreserveSameTurnDispatch(t *testing.T) {
	h := newHarness(t)
	source, turn := startReviewTurn(t, h)
	w := h.createWork(workPlan())
	h.associate(mainAgent, source, w.WorkItemID)
	h.prepareAgent(w.WorkItemID)
	commitFixtureTurn(t, h, turn.RequestID)
	id := createFingerprintTask(t, h, source, turn, w)
	before := h.agentContext(w.WorkItemID)
	for _, mode := range []protocol.ControlMode{protocol.ControlPaused, protocol.ControlManaged} {
		env := h.env("control.set_mode", taskTarget(id), protocol.SetControlModePayload{Mode: mode}, rev(h.task(id).ControlRevision))
		env.ParentRequestID = turn.RequestID
		op := h.exec(mainAgent, env, SetControlMode{})
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		var result map[string]json.RawMessage
		json.Unmarshal(op.Result, &result)
		t.Logf("mode %s result %s", mode, op.Result)
		if result["receipt"] == nil {
			t.Error("control write omitted coordination receipt")
		}
	}
	after := h.agentContext(w.WorkItemID)
	t.Logf("context revision before=%d after=%d blockers=%+v", before.Revision, after.Revision, after.DispatchBlockers)
	if _, ok, _ := h.claim("linux-1"); !ok {
		t.Error("reviewed task could not dispatch after explicitly resuming it")
	}
}

func TestPendingLegacyMessageFencesCurrentAcceptance(t *testing.T) {
	h := newHarness(t)
	source, turn := startReviewTurn(t, h)
	w := h.createWork(workPlan())
	h.associate(mainAgent, source, w.WorkItemID)
	h.prepareAgent(w.WorkItemID)
	commitFixtureTurn(t, h, turn.RequestID)
	id := createFingerprintTask(t, h, source, turn, w)
	impl, ok, _ := h.claim("linux-1")
	if !ok {
		t.Fatal("no implementation")
	}
	if op := h.runAs(impl, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.runAs(impl, protocol.OpRunsSubmitArtifact, testArtifact(h.task(id).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	commitFixtureTurn(t, h, turn.RequestID)
	accept, ok, _ := h.claim("linux-1")
	if !ok {
		t.Fatal("no acceptance")
	}
	e := h.env("conversation.record_agent_message", protocol.Target{Type: "conversation"}, recordAgentMessagePayload{Body: "We must not treat the existing authorization as permission to finish yet.", ReplyTo: source, TurnID: turn.TurnID, ItemID: "legacy", Status: protocol.AssistantStreaming}, nil)
	if op := h.exec(mainAgent, e, RecordAgentMessage{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	c := h.agentContext(w.WorkItemID)
	t.Logf("pending message context ready=%v blockers=%+v", c.DispatchReady, c.DispatchBlockers)
	if op := h.submitVerdict(accept.RunID, protocol.VerdictPassed); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if got := h.task(id); got.Lifecycle == protocol.TaskCompleted {
		t.Errorf("completed task with an unreviewed formal message in its originating responsibility: %+v", got)
	}
}

func TestSameTurnWorkerLifecyclePreservesReviewedSources(t *testing.T) {
	h := newHarness(t)
	source, turn := startReviewTurn(t, h)
	w := h.createWork(workPlan())
	h.associate(mainAgent, source, w.WorkItemID)
	h.prepareAgent(w.WorkItemID)
	commitFixtureTurn(t, h, turn.RequestID)
	id := createFingerprintTask(t, h, source, turn, w)
	impl, ok, _ := h.claim("linux-1")
	if !ok {
		t.Fatal("no implementation claim")
	}
	if op := h.runAs(impl, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.runAs(impl, protocol.OpRunsSubmitArtifact, testArtifact(h.task(id).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	accept, ok, _ := h.claim("linux-1")
	if !ok {
		t.Fatal("worker progress invalidated unchanged source review")
	}
	if op := h.runAs(accept, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.submitVerdict(accept.RunID, protocol.VerdictPassed); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.task(id).Lifecycle != protocol.TaskCompleted {
		t.Fatal("reviewed known execution failed to complete")
	}
	turn.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	state, err := h.app.LogicalTurn(t.Context(), turn.RequestID)
	if err != nil || state.Status != "completed" || state.RepairAttempts != 0 {
		t.Fatalf("worker lifecycle caused unnecessary source review: %+v %v", state, err)
	}
}

func TestSupplementCoordinationUsesNativeExecutionAndAdmittedReplySources(t *testing.T) {
	h, active := activeSteerEnv(t)
	root, ok, err := h.app.MainTurn(t.Context(), active.RequestID)
	if err != nil || !ok {
		t.Fatal(err, ok)
	}
	supplement := consumedSteerFixture(t, h, active)
	var parent string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT request_id FROM main_steers WHERE message_id=?`, supplement).Scan(&parent)
	}); err != nil {
		t.Fatal(err)
	}
	principal := protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "main", Thread: "native"}
	initial := protocol.InitialWorkItemContext{Coverage: "Original and consumed supplement reviewed", ReviewedKinds: allFactKinds, SourceMessageIDs: []string{root.MessageID, supplement}, Facts: []protocol.ContextFact{{Kind: "goal", Scope: "work_item", Body: "Investigate the supplement", SourceMessageID: supplement}}}
	for _, id := range initial.SourceMessageIDs {
		v := h.associations(id)
		initial.SourceReviews = append(initial.SourceReviews, protocol.LogicalTurnReview{MessageID: id, ContentRevision: v.ContentRevision, AssociationRevision: v.Revision})
	}
	env := h.env("work_items.create", workTarget(""), protocol.CreateWorkItemPayload{WorkItemPlan: workPlan(), InitialContext: &initial, SourceMessageID: supplement, CommitSeq: 1}, nil)
	env.ParentRequestID = parent
	op := h.exec(principal, env, WithExecutionRequest(WithPlanningSource(CreateWorkItem{}, root.MessageID), active.RequestID))
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var w protocol.WorkItem
	if err := json.Unmarshal(op.Result, &w); err != nil {
		t.Fatal(err)
	}
	env = h.env("tasks.create", taskTarget(""), createTaskPayload{Title: "Investigate supplement", Spec: testSpec, WorkItemID: w.WorkItemID, WorkItemRevision: w.Revision, StageIDs: []string{"investigate"}, SourceMessageID: supplement, CommitSeq: 2}, nil)
	env.ParentRequestID = parent
	op = h.exec(principal, env, WithExecutionRequest(WithPlanningSource(CreateTask{}, root.MessageID), active.RequestID))
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var result struct {
		Receipt struct {
			Turn    *protocol.LogicalTurnState `json:"turn"`
			Pending []json.RawMessage          `json:"pending"`
		}
	}
	if err := json.Unmarshal(op.Result, &result); err != nil {
		t.Fatal(err)
	}
	state, err := h.app.LogicalTurn(t.Context(), active.RequestID)
	if err != nil || result.Receipt.Turn == nil || result.Receipt.Turn.ID != state.ID || len(result.Receipt.Pending) != 0 {
		t.Fatalf("supplement lost current review receipt: %s %v", op.Result, err)
	}
	unadmitted := queueMessage(t, h, "Queued input that this reply must not claim")
	c := h.agentContext(w.WorkItemID)
	env = h.env("conversation.reply", protocol.Target{Type: "conversation"}, protocol.CoordinationReply{Body: "The admitted supplement is assigned.", Purpose: "progress", Disposition: "linked", WorkItemIDs: []string{w.WorkItemID}, Contexts: []protocol.LogicalContextReview{{WorkItemID: w.WorkItemID, Revision: c.Revision}}, ReviewedKinds: allFactKinds, Facts: []protocol.ContextFact{}}, nil)
	env.ParentRequestID = parent
	op = h.exec(principal, env, WithExecutionRequest(CoordinationReply{}, active.RequestID))
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var published struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(op.Result, &published); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		item, err := store.GetMessage(t.Context(), tx.Q(), published.MessageID)
		if err != nil {
			return err
		}
		if item.ReplyTo != supplement {
			t.Errorf("reply focus=%s want consumed supplement", item.ReplyTo)
		}
		var total, queued int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*),COALESCE(SUM(source_id=?),0) FROM conversation_reply_sources WHERE reply_id=?`, unadmitted, published.MessageID).Scan(&total, &queued); err != nil {
			return err
		}
		if total != 2 || queued != 0 {
			t.Errorf("reply sources total=%d queued=%d", total, queued)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := h.claim("linux-1"); !ok {
		t.Fatal("reviewed supplement task did not dispatch during its native turn")
	}
}

func TestTasklessBackgroundFormalReplyIsPublishedOnceAndCannotBecomeSilent(t *testing.T) {
	for _, quietFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "publish", true: "replace_prior_checkin"}[quietFirst], func(t *testing.T) {
			h := newHarness(t)
			opened := h.exec(owner, h.env("decisions.open", protocol.Target{Type: "decision"}, openDecisionPayload{Question: "Choose scope", Options: []string{"narrow", "broad"}}, nil), OpenDecision{})
			if opened.Status != protocol.OpSucceeded {
				t.Fatal(opened)
			}
			d, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			var p HandleMessage
			if err := json.Unmarshal(d.Payload, &p); err != nil {
				t.Fatal(err)
			}
			turn := store.MainTurn{RequestID: d.RequestID, MessageID: p.MessageID, ThreadID: mainAgent.Thread, TurnID: "taskless-native", Status: store.TurnStarted}
			if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
				t.Fatal(err)
			}
			if op := h.backgroundCall(d, turn, "sources", map[string]any{"after": 0}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if quietFirst {
				if op := h.backgroundCall(d, turn, "checkin", protocol.BackgroundCheckin{Note: "No visible result yet"}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			env := h.env("conversation.reply", protocol.Target{Type: "logical_turn", ID: d.RequestID}, protocol.CoordinationReply{Body: "Please choose the scope.", Purpose: "final", Disposition: "unassigned", ReviewedKinds: allFactKinds}, nil)
			env.ParentRequestID = d.RequestID
			out := h.exec(mainAgent, env, CoordinationReply{})
			if out.Status != protocol.OpSucceeded {
				t.Fatal(out)
			}
			var pub struct {
				MessageID string `json:"message_id"`
			}
			if err := json.Unmarshal(out.Result, &pub); err != nil || pub.MessageID == "" {
				t.Fatal(string(out.Result), err)
			}
			if !h.associations(pub.MessageID).FactsReviewed {
				t.Fatal("formal background message has an unreviewed body")
			}
			if batch := h.backgroundPage(turn.MessageID, 0).Batch; batch.Checkin != nil {
				t.Fatal("explicit publication retained old silence")
			}
			count := h.count("conversation_messages")
			if op := h.backgroundCall(d, turn, "checkin", protocol.BackgroundCheckin{Note: "Hide the already published reply"}); op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeInvalidTransition {
				t.Fatal(op)
			}
			replay := h.exec(mainAgent, env, CoordinationReply{})
			if string(replay.Result) != string(out.Result) || h.count("conversation_messages") != count {
				t.Fatal("formal background replay duplicated a message")
			}
			h.associate(mainAgent, turn.MessageID)
			commitFixtureTurn(t, h, turn.RequestID)
			turn.Status = store.TurnCompleted
			if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
				t.Fatal(err)
			}
			if h.count("conversation_messages") != count {
				t.Fatal("batch settlement duplicated formal output")
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				msg, err := store.GetMessage(t.Context(), tx.Q(), pub.MessageID)
				if err != nil {
					return err
				}
				if msg.Body != "Please choose the scope." {
					t.Errorf("published body changed: %q", msg.Body)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRejectedBackgroundFormalReplyPreservesItsPriorSilentChoice(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	h.prepareAgent(w.WorkItemID)
	opened := h.exec(owner, h.env("decisions.open", protocol.Target{Type: "decision"}, openDecisionPayload{Question: "Choose scope", Options: []string{"narrow", "broad"}}, nil), OpenDecision{})
	if opened.Status != protocol.OpSucceeded {
		t.Fatal(opened)
	}
	d, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	var trigger HandleMessage
	if err := json.Unmarshal(d.Payload, &trigger); err != nil {
		t.Fatal(err)
	}
	turn := store.MainTurn{RequestID: d.RequestID, MessageID: trigger.MessageID, ThreadID: mainAgent.Thread, TurnID: "reject-background-formal", Status: store.TurnStarted}
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	if op := h.backgroundCall(d, turn, "sources", map[string]any{"after": 0}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.backgroundCall(d, turn, "checkin", protocol.BackgroundCheckin{Note: "Quiet before rejected publication"}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	c := h.agentContext(w.WorkItemID)
	before := h.count("conversation_messages")
	env := h.env("conversation.reply", protocol.Target{Type: "conversation"}, protocol.CoordinationReply{Body: "Invalid authorization must roll back", Purpose: "progress", Disposition: "linked", WorkItemIDs: []string{w.WorkItemID}, Contexts: []protocol.LogicalContextReview{{WorkItemID: w.WorkItemID, Revision: c.Revision}}, ReviewedKinds: allFactKinds, Facts: []protocol.ContextFact{{WorkItemID: w.WorkItemID, Scope: "work_item", Kind: "authorization", Body: "New permission from an assistant"}}}, nil)
	env.ParentRequestID = d.RequestID
	op := h.exec(mainAgent, env, CoordinationReply{})
	if op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeInvalidRequest {
		t.Fatal(op)
	}
	if h.count("conversation_messages") != before || h.agentContext(w.WorkItemID).Revision != c.Revision {
		t.Fatal("rejected background reply leaked message or context changes")
	}
	batch := h.backgroundPage(turn.MessageID, 0).Batch
	if batch.Checkin == nil || batch.Checkin.Note != "Quiet before rejected publication" {
		t.Fatal("rejected publication changed prior silence")
	}
}

func TestAppliedOwnerRetriesKeepTheirAgreementButCannotOverrideNewConstraints(t *testing.T) {
	for _, acceptance := range []bool{false, true} {
		for _, changed := range []bool{false, true} {
			for _, completed := range []bool{false, true} {
				name := fmt.Sprintf("acceptance_%t_constraint_%t_native_completed_%t", acceptance, changed, completed)
				t.Run(name, func(t *testing.T) {
					h := newHarness(t)
					source, turn := startReviewTurn(t, h)
					w := h.createWork(workPlan())
					h.associate(mainAgent, source, w.WorkItemID)
					h.prepareAgent(w.WorkItemID)
					commitFixtureTurn(t, h, turn.RequestID)
					id := createFingerprintTask(t, h, source, turn, w)
					frozen := h.storedSpec(id, 1)
					run, ok, _ := h.claim("linux-1")
					if !ok {
						t.Fatal("no initial implementation")
					}
					if op := h.runAs(run, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
						t.Fatal(op)
					}
					option := "retry the implementation"
					if acceptance {
						if op := h.runAs(run, protocol.OpRunsSubmitArtifact, testArtifact(h.task(id).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
							t.Fatal(op)
						}
						run, ok, _ = h.claim("linux-1")
						if !ok {
							t.Fatal("no initial acceptance")
						}
						option = "retry acceptance"
					}
					if op := h.runAs(run, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "sandbox crashed"}, Release{}); op.Status != protocol.OpSucceeded {
						t.Fatal(op)
					}
					if changed {
						c := h.agentContext(w.WorkItemID)
						env := h.env("work_item_context.fact", workTarget(w.WorkItemID), protocol.ContextFact{Scope: "work_item", Kind: "constraint", Body: "Preserve the newly constrained input", SourceMessageID: source}, rev(c.Revision))
						env.ParentRequestID = turn.RequestID
						if op := h.exec(mainAgent, env, ContextCommand{Action: "fact"}); op.Status != protocol.OpSucceeded {
							t.Fatal(op)
						}
					}
					commitFixtureTurn(t, h, turn.RequestID)
					if completed {
						turn.Status = store.TurnCompleted
						if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
							t.Fatal(err)
						}
					}
					d := h.openDecision(id)
					answer := h.answer(d, option)
					if answer.Effect == nil || !answer.Effect.Applied {
						t.Fatal(answer)
					}
					if h.task(id).SpecVersion != 1 || !h.storedSpec(id, 1).Equal(frozen) {
						t.Fatal("owner retry rewrote the frozen spec")
					}
					next, claimed, _ := h.claim("linux-1")
					if changed {
						if claimed {
							t.Fatal("retry bypassed new source constraints")
						}
						return
					}
					role := protocol.RoleImplement
					if acceptance {
						role = protocol.RoleAccept
					}
					if !claimed || next.Role != role || next.Generation != 2 {
						t.Fatalf("verified retry blocked: claimed=%t assignment=%+v context=%+v", claimed, next, h.agentContext(w.WorkItemID).DispatchBlockers)
					}
				})
			}
		}
	}
}

func TestOwnerRatingPreservesRunningSourceReviewButNotSourceCorrections(t *testing.T) {
	for _, corrected := range []bool{false, true} {
		t.Run(fmt.Sprint("source_corrected_", corrected), func(t *testing.T) {
			h := newHarness(t)
			source, turn := startReviewTurn(t, h)
			w := h.createWork(workPlan())
			h.associate(mainAgent, source, w.WorkItemID)
			h.prepareAgent(w.WorkItemID)
			commitFixtureTurn(t, h, turn.RequestID)
			id := createFingerprintTask(t, h, source, turn, w)
			run, ok, _ := h.claim("linux-1")
			if !ok {
				t.Fatal("no implementation")
			}
			if op := h.runAs(run, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if corrected {
				a := h.associations(source)
				env := h.env("conversation.set_associations", protocol.Target{Type: "message", ID: source}, protocol.SetMessageAssociationsPayload{Disposition: "unassigned", ContentRevision: a.ContentRevision}, rev(a.Revision))
				if op := h.exec(owner, env, SetMessageAssociations{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			if op := h.exec(owner, h.env("tasks.rate", taskTarget(id), protocol.RateTaskPayload{Rating: protocol.RatingP0}, rev(h.task(id).ControlRevision)), RateTask{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if op := h.runAs(run, protocol.OpRunsSubmitArtifact, testArtifact(h.task(id).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			got := h.task(id)
			if corrected {
				if got.ControlMode != protocol.ControlPaused || got.Lifecycle == protocol.TaskAwaitingAcceptance {
					t.Fatal("rating cleared a source correction", got)
				}
				return
			}
			if got.ControlMode != protocol.ControlManaged || got.Lifecycle != protocol.TaskAwaitingAcceptance {
				t.Fatalf("queue-only rating invalidated the execution: %+v", got)
			}
		})
	}
}

func TestUnverifiedOneShotAuthorizationStillChangesTheAgreement(t *testing.T) {
	h := newHarness(t)
	source, turn := startReviewTurn(t, h)
	w := h.createWork(workPlan())
	h.associate(mainAgent, source, w.WorkItemID)
	h.prepareAgent(w.WorkItemID)
	commitFixtureTurn(t, h, turn.RequestID)
	createFingerprintTask(t, h, source, turn, w)
	body, _ := json.Marshal(map[string]any{"scope": protocol.AppliedTransitionAuthorizationScope, "decision_id": "unknown-decision", "task_id": "unknown-task", "request_id": turn.RequestID})
	c := h.agentContext(w.WorkItemID)
	env := h.env("work_item_context.fact", workTarget(w.WorkItemID), protocol.ContextFact{Scope: "work_item", Kind: "authorization", Body: string(body), SourceMessageID: source}, rev(c.Revision))
	env.ParentRequestID = turn.RequestID
	if op := h.exec(mainAgent, env, ContextCommand{Action: "fact"}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	commitFixtureTurn(t, h, turn.RequestID)
	if _, ok, _ := h.claim("linux-1"); ok {
		t.Fatal("unverified model text was treated as an applied owner transition")
	}
}
