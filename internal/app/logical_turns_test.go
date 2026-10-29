package app

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func startReviewTurn(t *testing.T, h *harness) (string, store.MainTurn) {
	t.Helper()
	id, d := h.say("Review this message")
	turn := store.MainTurn{RequestID: d.RequestID, MessageID: id, ThreadID: mainAgent.Thread, TurnID: "native-1", Status: store.TurnStarted}
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	return id, turn
}
func commitFixtureTurn(t *testing.T, h *harness, request string) {
	t.Helper()
	state, err := h.app.LogicalTurn(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	p := protocol.LogicalTurnCommit{Contexts: state.Contexts}
	for _, m := range state.Messages {
		p.Messages = append(p.Messages, protocol.LogicalTurnReview{MessageID: m.MessageID, ContentRevision: m.ContentRevision, AssociationRevision: m.Revision})
	}
	cmd := CommitLogicalTurn{RequestID: request}
	op := h.exec(mainAgent, h.env(cmd.Operation(), protocol.Target{Type: "logical_turn", ID: request}, p, nil), cmd)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}
func TestLogicalTurnRequiresExplicitEmptyDecisionAndFactReview(t *testing.T) {
	h := newHarness(t)
	id, turn := startReviewTurn(t, h)
	original := h.associations(id)
	if original.Disposition != "pending" || original.FactsReviewed {
		t.Fatal(original)
	}
	invalid := h.exec(mainAgent, h.env("conversation.set_associations", protocol.Target{Type: "message", ID: id}, protocol.SetMessageAssociationsPayload{ContentRevision: original.ContentRevision}, rev(original.Revision)), SetMessageAssociations{})
	expectWorkCode(t, invalid, protocol.CodeInvalidRequest)
	if op := h.associate(mainAgent, id); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.associations(id).FactsReviewed {
		t.Fatal("classification alone certified facts")
	}
	commitFixtureTurn(t, h, turn.RequestID)
	turn.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	state, err := h.app.LogicalTurn(t.Context(), turn.RequestID)
	if err != nil || state.Status != "completed" || state.RepairAttempts != 0 {
		t.Fatal(state, err)
	}
}
func TestOwnerUnassignedLocksEntireMessageIncludingNewItems(t *testing.T) {
	h := newHarness(t)
	id, _ := startReviewTurn(t, h)
	w := h.createWork(workPlan())
	if op := h.associate(owner, id); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	expectWorkCode(t, h.associate(mainAgent, id, w.WorkItemID), protocol.CodeInvalidRequest)
	err := h.store.Write(t.Context(), func(tx *store.Tx) error { return tx.BindMessage(t.Context(), id, "work_item", w.WorkItemID, "source") })
	if protocol.CodeOf(err) != protocol.CodeInvalidRequest {
		t.Fatal("business binding bypassed owner lock", err)
	}
	if op := h.associate(owner, id, w.WorkItemID); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}
func TestLogicalRepairRestartSchedulesOnceAndOwnerStopPersists(t *testing.T) {
	h := newHarness(t)
	id, turn := startReviewTurn(t, h)
	turn.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	state, err := h.app.LogicalTurn(t.Context(), turn.RequestID)
	if err != nil || state.RepairAttempts != 1 || state.Status != "recovering" {
		t.Fatal(state, err)
	}
	repair, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	// h.say's original delivery is still sending; finish its durable receipt first.
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE outbox SET status='delivered' WHERE request_id=?`, turn.RequestID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		repair, _, ok, err = h.app.NextDelivery(t.Context(), MainThreadDestination)
	}
	if err != nil || !ok || repair.Operation != "conversation.repair_turn" {
		t.Fatal(repair, ok, err)
	}
	r := store.MainTurn{RequestID: repair.RequestID, MessageID: id, ThreadID: mainAgent.Thread, TurnID: "repair-1", Status: store.TurnStarted}
	if err := h.app.PutMainTurn(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if err := h.app.SettleOpenTurns(t.Context(), r.ThreadID); err != nil {
		t.Fatal(err)
	}
	if err := h.app.SettleOpenTurns(t.Context(), r.ThreadID); err != nil {
		t.Fatal(err)
	}
	state, err = h.app.LogicalTurn(t.Context(), turn.RequestID)
	if err != nil || state.Status != "unknown" || state.RepairAttempts != 1 {
		t.Fatal("crash must wait for native receipt", state, err)
	}
	r.Status = store.TurnInterrupted
	r.NativeReconciled = true
	if err := h.app.PutMainTurn(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	state, err = h.app.LogicalTurn(t.Context(), turn.RequestID)
	if err != nil || state.RepairAttempts != 2 {
		t.Fatal("repair crash lost or duplicated responsibility", state, err)
	}
	cmd := InterruptReply{}
	op := h.exec(owner, h.env(cmd.Operation(), protocol.Target{Type: "conversation"}, protocol.InterruptReplyPayload{MessageID: id}, nil), cmd)
	if op.Status == protocol.OpFailed {
		t.Fatal(op)
	}
	state, _ = h.app.LogicalTurn(t.Context(), turn.RequestID)
	if state.Status != "stopped" {
		t.Fatal(state)
	}
}
func TestLogicalTurnLateReplyReopensAndSchedulesRepair(t *testing.T) {
	h := newHarness(t)
	id, turn := startReviewTurn(t, h)
	h.associate(mainAgent, id)
	commitFixtureTurn(t, h, turn.RequestID)
	turn.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	payload := recordAgentMessagePayload{Body: "A late final reply", ReplyTo: id, TurnID: turn.TurnID, ItemID: "late"}
	op := h.exec(mainAgent, h.env("conversation.record_agent_message", protocol.Target{Type: "conversation"}, payload, nil), RecordAgentMessage{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	state, _ := h.app.LogicalTurn(t.Context(), turn.RequestID)
	if state.Status != "pending_repair" || len(state.Messages) != 2 {
		t.Fatal(state)
	}
	if _, _, _, err := h.app.NextDelivery(t.Context(), MainThreadDestination); err != nil {
		t.Fatal(err)
	}
	state, _ = h.app.LogicalTurn(t.Context(), turn.RequestID)
	if state.Status != "recovering" || state.RepairAttempts != 1 {
		t.Fatal(state)
	}
}
func TestClassificationRejectsChangedStreamingContent(t *testing.T) {
	h := newHarness(t)
	id, turn := startReviewTurn(t, h)
	p := recordAgentMessagePayload{Body: "Progress", ReplyTo: id, TurnID: turn.TurnID, ItemID: "reply", Status: protocol.AssistantStreaming}
	op := h.exec(mainAgent, h.env("conversation.record_agent_message", protocol.Target{Type: "conversation"}, p, nil), RecordAgentMessage{})
	var reply struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(op.Result, &reply); err != nil {
		t.Fatal(err)
	}
	before := h.associations(reply.MessageID)
	p.Body = "Progress updated"
	h.exec(mainAgent, h.env("conversation.record_agent_message", protocol.Target{Type: "conversation"}, p, nil), RecordAgentMessage{})
	stale := h.exec(mainAgent, h.env("conversation.set_associations", protocol.Target{Type: "message", ID: reply.MessageID}, protocol.SetMessageAssociationsPayload{Disposition: "unassigned", ContentRevision: before.ContentRevision}, rev(before.Revision)), SetMessageAssociations{})
	expectWorkCode(t, stale, protocol.CodeStaleRevision)
}

func TestRemovedAssociationRemainsFencedUntilFinalContextVersion(t *testing.T) {
	h := newHarness(t)
	id, turn := startReviewTurn(t, h)
	w := h.createWork(workPlan())
	h.associate(mainAgent, id, w.WorkItemID)
	h.prepareAgent(w.WorkItemID)
	h.associate(owner, id)
	h.prepareAgent(w.WorkItemID)
	commitFixtureTurn(t, h, turn.RequestID)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		ready, err := store.WorkItemContextReady(t.Context(), tx.Q(), w.WorkItemID)
		if !ready {
			t.Error("reviewed removed-side context must be ready before native completion")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// A real concurrent plan edit after commit must invalidate the final fence.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE work_item_contexts SET revision=revision+1 WHERE work_item_id=?`, w.WorkItemID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	turn.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	state, err := h.app.LogicalTurn(t.Context(), turn.RequestID)
	if err != nil || state.Status != "recovering" || len(state.Contexts) != 1 {
		t.Fatal(state, err)
	}
}

func TestClarificationAnswerAdoptsOriginalResponsibility(t *testing.T) {
	h := newHarness(t)
	id, turn := startReviewTurn(t, h)
	cmd := AwaitTurnClarification{RequestID: turn.RequestID}
	op := h.exec(mainAgent, h.env(cmd.Operation(), protocol.Target{Type: "logical_turn", ID: turn.RequestID}, protocol.TurnClarification{Question: "Which work item?"}, nil), cmd)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	turn.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE outbox SET status='delivered' WHERE request_id=?`, turn.RequestID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	answer, next := startReviewTurn(t, h)
	state, err := h.app.LogicalTurn(t.Context(), next.RequestID)
	if err != nil || len(state.Awaiting) != 1 {
		t.Fatal(state, err)
	}
	adopt := AdoptClarification{RequestID: next.RequestID}
	p := state.Awaiting[0]
	op = h.exec(mainAgent, h.env(adopt.Operation(), protocol.Target{Type: "logical_turn", ID: p.LogicalTurnID}, protocol.AdoptClarificationPayload{LogicalTurnID: p.LogicalTurnID, Revision: p.Revision}, rev(p.Revision)), adopt)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	h.associate(mainAgent, id)
	h.associate(mainAgent, answer)
	commitFixtureTurn(t, h, next.RequestID)
	next.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), next); err != nil {
		t.Fatal(err)
	}
	for _, req := range []string{turn.RequestID, next.RequestID} {
		state, err := h.app.LogicalTurn(t.Context(), req)
		if err != nil || state.Status != "completed" {
			t.Fatal(state, err)
		}
	}
}

func TestCorrectionRequiresExplicitFactReconciliation(t *testing.T) {
	h := newHarness(t)
	id, turn := startReviewTurn(t, h)
	w := h.createWork(workPlan())
	h.associate(mainAgent, id, w.WorkItemID)
	ctx := h.agentContext(w.WorkItemID)
	fact := protocol.ContextFact{Scope: "work_item", Kind: "constraint", Body: "Only this scope", SourceMessageID: id}
	op := h.exec(mainAgent, h.env("work_item_context.fact", workTarget(w.WorkItemID), fact, rev(ctx.Revision)), ContextCommand{Action: "fact"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	h.prepareAgent(w.WorkItemID)
	h.associate(owner, id)
	ctx = h.agentContext(w.WorkItemID)
	prepare := protocol.PrepareWorkItemContextPayload{Coverage: "Checked correction", Gaps: "none"}
	op = h.exec(mainAgent, h.env("work_item_context.prepare", workTarget(w.WorkItemID), prepare, rev(ctx.Revision)), ContextCommand{Action: "prepare"})
	expectWorkCode(t, op, protocol.CodeInvalidTransition)
	prepare.RetiredFactIDs = []string{ctx.Facts[0].ID}
	op = h.exec(mainAgent, h.env("work_item_context.prepare", workTarget(w.WorkItemID), prepare, rev(ctx.Revision)), ContextCommand{Action: "prepare"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	commitFixtureTurn(t, h, turn.RequestID)
}

func TestExplicitHistoricalSourceJoinsCurrentReview(t *testing.T) {
	h := newHarness(t)
	old, first := startReviewTurn(t, h)
	h.associate(mainAgent, old)
	commitFixtureTurn(t, h, first.RequestID)
	first.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE outbox SET status='delivered' WHERE request_id=?`, first.RequestID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	current, second := startReviewTurn(t, h)
	h.associate(mainAgent, current)
	w := h.createWork(workPlan())
	m := h.associations(old)
	env := h.env("conversation.set_associations", protocol.Target{Type: "message", ID: old}, protocol.SetMessageAssociationsPayload{Disposition: "linked", WorkItemIDs: []string{w.WorkItemID}, ContentRevision: m.ContentRevision}, rev(m.Revision))
	env.ParentRequestID = second.RequestID
	if op := h.exec(mainAgent, env, SetMessageAssociations{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	h.prepareAgent(w.WorkItemID)
	state, err := h.app.LogicalTurn(t.Context(), second.RequestID)
	if err != nil || len(state.Messages) != 2 {
		t.Fatal(state, err)
	}
	commitFixtureTurn(t, h, second.RequestID)
	second.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		ready, err := store.WorkItemContextReady(t.Context(), tx.Q(), w.WorkItemID)
		if !ready {
			t.Error("reviewed historical source still blocks background")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestInitialContextDoesNotMutateCreateEnvelope(t *testing.T) {
	h := newHarness(t)
	source, _ := startReviewTurn(t, h)
	p := protocol.CreateWorkItemPayload{IssuePlan: workPlan(), InitialContext: &protocol.InitialWorkItemContext{SourceMessageIDs: []string{source}, Coverage: "Initial owner message", ReviewedKinds: []string{"goal", "constraint", "authorization", "refusal", "commitment", "question", "suggestion"}, Facts: []protocol.ContextFact{{Kind: "goal", Scope: "work_item", Body: "Initial goal", SourceMessageID: source}}}}
	env := h.env("work_items.create", workTarget(""), p, nil)
	before, _ := json.Marshal(env)
	op := h.exec(mainAgent, env, CreateIssue{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	after, _ := json.Marshal(env)
	if string(before) != string(after) {
		t.Fatal("nested fact changed caller envelope")
	}
	retry := h.exec(mainAgent, env, CreateIssue{})
	if retry.Status != protocol.OpSucceeded || string(retry.Result) != string(op.Result) {
		t.Fatal(retry)
	}
}

func TestInheritedBackgroundReplyHasNoMainReviewObligation(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	h.prepareAgent(w.WorkItemID)
	var source string
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		source = h.ids.New(protocol.PrefixMessage)
		_, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: source, ConversationID: "work-item:" + w.WorkItemID, Author: "system", Body: "Background batch", CreatedAt: 1})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	op := h.exec(mainAgent, h.env("conversation.record_agent_message", protocol.Target{Type: "conversation"}, recordAgentMessagePayload{ReplyTo: source, TurnID: "background-reply", ItemID: "reply", Body: "No changes", Status: protocol.AssistantCompleted}, nil), RecordAgentMessage{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		ready, err := store.WorkItemContextReady(t.Context(), tx.Q(), w.WorkItemID)
		if !ready {
			t.Error("program-linked background reply blocked its own conversation")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
