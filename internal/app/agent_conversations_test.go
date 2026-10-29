package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func (h *harness) associations(message string) protocol.MessageAssociations {
	h.t.Helper()
	var v protocol.MessageAssociations
	if err := h.store.Read(h.t.Context(), func(tx *store.ReadTx) error {
		var err error
		v, err = store.MessageAssociations(h.t.Context(), tx.Q(), message)
		return err
	}); err != nil {
		h.t.Fatal(err)
	}
	return v
}
func (h *harness) agentContext(item string) protocol.WorkItemContext {
	h.t.Helper()
	var v protocol.WorkItemContext
	if err := h.store.Read(h.t.Context(), func(tx *store.ReadTx) error {
		var err error
		v, err = views.IssueContext(h.t.Context(), tx.Q(), item, 0)
		return err
	}); err != nil {
		h.t.Fatal(err)
	}
	return v
}
func (h *harness) associate(p protocol.Principal, message string, items ...string) protocol.Operation {
	h.t.Helper()
	a := h.associations(message)
	return h.exec(p, h.env("conversation.set_associations", protocol.Target{Type: "message", ID: message}, protocol.SetMessageAssociationsPayload{WorkItemIDs: items, Disposition: dispositionForTest(items), ContentRevision: a.ContentRevision}, rev(a.Revision)), SetMessageAssociations{})
}
func (h *harness) prepareAgent(item string) {
	h.t.Helper()
	c := h.agentContext(item)
	op := h.exec(mainAgent, h.env("work_item_context.prepare", workTarget(item), protocol.PrepareWorkItemContextPayload{Coverage: "Reviewed all linked original messages", Gaps: "No known gaps"}, rev(c.Revision)), ContextCommand{Action: "prepare"})
	if op.Status != protocol.OpSucceeded {
		h.t.Fatal(op)
	}
}
func TestAgentAssociationCorrectionsVersionSourcesAndDedup(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	other := h.createWork(workPlan())
	source := h.ownerEvidence().Reference
	if op := h.associate(mainAgent, source, w.WorkItemID, other.WorkItemID); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	h.prepareAgent(w.WorkItemID)
	before := h.agentContext(w.WorkItemID)
	a := h.associations(source)
	env := h.env("conversation.set_associations", protocol.Target{Type: "message", ID: source}, protocol.SetMessageAssociationsPayload{WorkItemIDs: []string{other.WorkItemID}, Disposition: "linked", ContentRevision: a.ContentRevision}, rev(a.Revision))
	first := h.exec(owner, env, SetMessageAssociations{})
	second := h.exec(owner, env, SetMessageAssociations{})
	if first.Status != protocol.OpSucceeded || string(first.Result) != string(second.Result) {
		t.Fatal(first, second)
	}
	after := h.agentContext(w.WorkItemID)
	if after.Prepared || after.Revision <= before.Revision || len(after.Sources) != 0 {
		t.Fatal("correction did not invalidate baseline", after)
	}
	expectWorkCode(t, h.associate(mainAgent, source, w.WorkItemID, other.WorkItemID), protocol.CodeInvalidRequest)
	if op := h.associate(owner, source, w.WorkItemID); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	stale := h.exec(owner, h.env("conversation.set_associations", protocol.Target{Type: "message", ID: source}, protocol.SetMessageAssociationsPayload{}, rev(a.Revision)), SetMessageAssociations{})
	expectWorkCode(t, stale, protocol.CodeStaleRevision)
}
func TestAgentAutomaticBindingConflictsWithConcurrentOwnerEdit(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	source := h.ownerEvidence().Reference
	a := h.associations(source)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		return tx.BindMessage(t.Context(), source, "work_item", w.WorkItemID, "source")
	}); err != nil {
		t.Fatal(err)
	}
	op := h.exec(owner, h.env("conversation.set_associations", protocol.Target{Type: "message", ID: source}, protocol.SetMessageAssociationsPayload{}, rev(a.Revision)), SetMessageAssociations{})
	expectWorkCode(t, op, protocol.CodeStaleRevision)
}
func TestAgentConversationScopeAndBaseline(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	op := h.exec(owner, h.env("conversation.open_background", workTarget(w.WorkItemID), struct{}{}, nil), OpenAgentConversation{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var v struct {
		ID string `json:"conversation_id"`
	}
	if err := json.Unmarshal(op.Result, &v); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.AgentContext(t.Context(), v.ID, "delivery"); protocol.CodeOf(err) != protocol.CodeInvalidTransition {
		t.Fatal("missing baseline admitted", err)
	}
	h.prepareAgent(w.WorkItemID)
	text, err := h.app.AgentContext(t.Context(), v.ID, "delivery")
	if err != nil || !strings.Contains(text, w.Title) {
		t.Fatal(text, err)
	}
	op = h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{ConversationID: v.ID, Body: "background original"}, nil), SubmitMessage{})
	if op.Status != protocol.OpAccepted {
		t.Fatal(op)
	}
	if err = h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		rows, err := store.RecentMessages(t.Context(), tx.Q(), 100, 0)
		if err != nil {
			return err
		}
		for _, m := range rows {
			if m.Body == "background original" {
				t.Fatal("background leaked into main context")
			}
		}
		page, err := views.Conversation(t.Context(), tx.Q(), 0, 0, 100, v.ID)
		if err != nil {
			return err
		}
		if len(page.Items) != 1 || page.Items[0].Body != "background original" {
			t.Fatal(page)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	h.reviewBackgroundOwnerNotices()
	if d, _, ok, err := h.app.NextDelivery(t.Context(), store.ConversationDestination(v.ID)); err != nil || !ok || d.Destination == MainThreadDestination {
		t.Fatal(d, ok, err)
	}
}
func TestAgentMissingAssessmentEscalatesExactlyOnce(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	h.prepareAgent(w.WorkItemID)
	h.exec(owner, h.env("conversation.open_background", workTarget(w.WorkItemID), struct{}{}, nil), OpenAgentConversation{})
	id := "work-item:" + w.WorkItemID
	op := h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{ConversationID: id, Body: "inspect"}, nil), SubmitMessage{})
	if op.Status != protocol.OpAccepted {
		t.Fatal(op)
	}
	h.reviewBackgroundOwnerNotices()
	d, _, ok, err := h.app.NextDelivery(t.Context(), store.ConversationDestination(id))
	if err != nil || !ok {
		t.Fatal(err)
	}
	var p HandleMessage
	_ = json.Unmarshal(d.Payload, &p)
	if _, err = h.app.AgentContext(t.Context(), id, d.RequestID); err != nil {
		t.Fatal(err)
	}
	turn := store.MainTurn{RequestID: d.RequestID, MessageID: p.MessageID, ThreadID: "background-thread", TurnID: "turn", Status: store.TurnStarted}
	if err = h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	count := h.count("outbox")
	turn.Status = store.TurnCompleted
	for range 2 {
		if err = h.app.PutMainTurn(t.Context(), turn); err != nil {
			t.Fatal(err)
		}
	}
	if h.count("outbox") != count+1 {
		t.Fatal("missing assessment was silently completed or escalated twice")
	}
}

func TestAgentNativeCompactUnknownCannotBeReplayed(t *testing.T) {
	h := newHarness(t)
	usage := protocol.AgentContextUsage{ThreadID: "native-thread", Revision: 3, Measurement: "observed", CompactSupported: true}
	if err := h.app.RecordAgentUsage(t.Context(), "main", usage); err != nil {
		t.Fatal(err)
	}
	env := h.env("conversation.compact", protocol.Target{Type: "conversation", ID: "main"}, struct{}{}, rev(0))
	first := h.exec(owner, env, CompactConversation{})
	if first.Status != protocol.OpAccepted {
		t.Fatal(first)
	}
	var result struct {
		ID string `json:"operation_id"`
	}
	if err := json.Unmarshal(first.Result, &result); err != nil {
		t.Fatal(err)
	}
	if err := h.app.SetCompactState(t.Context(), result.ID, "native-thread", "unknown"); err != nil {
		t.Fatal(err)
	}
	repeated := h.exec(owner, env, CompactConversation{})
	if string(repeated.Result) != string(first.Result) {
		t.Fatal("request identity changed")
	}
	next := h.exec(owner, h.env("conversation.compact", protocol.Target{Type: "conversation", ID: "main"}, struct{}{}, rev(0)), CompactConversation{})
	expectWorkCode(t, next, protocol.CodeInvalidTransition)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		return tx.PutBaseline(t.Context(), store.Baseline{ThreadKey: "main/native-thread", Epoch: 1, Valid: true}, 0)
	}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := h.app.CompleteNativeCompact(t.Context(), "main", "native-thread", "native-compact-item"); err != nil {
			t.Fatal(err)
		}
	}
	state, err := h.app.CompactState(t.Context(), result.ID)
	if err != nil || state != "completed" {
		t.Fatal(state, err)
	}
	if h.count("agent_compactions") != 1 {
		t.Fatal("native event duplicated compaction")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		baseline, err := store.GetBaseline(t.Context(), tx.Q(), "main/native-thread")
		if err == nil && baseline.Valid {
			t.Fatal("completed compact retained pre-compact baseline")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAgentRolloutWindowsRetainUnicodeAndScope(t *testing.T) {
	h := newHarness(t)
	text := strings.Repeat("约束", 10000)
	raw, _ := json.Marshal(map[string]string{"text": text})
	if err := h.app.RecordRolloutEvent(t.Context(), "main", "native", "turn", "item", "item_completed", raw); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		page, err := store.AgentRollout(t.Context(), tx.Q(), "main", "turn", 0)
		if err != nil {
			return err
		}
		if len(page.Records) != 1 || !page.Records[0].Truncated {
			t.Fatal(page)
		}
		record := page.Records[0]
		rest, err := store.AgentRolloutWindow(t.Context(), tx.Q(), "main", record.Seq, int64(len([]rune(record.Data))))
		if err != nil {
			return err
		}
		if record.Data+rest.Text != string(raw) || rest.More {
			t.Fatal("raw unicode evidence lost")
		}
		if _, err = store.AgentRolloutWindow(t.Context(), tx.Q(), "other", record.Seq, 0); err == nil {
			t.Fatal("unregistered conversation read another rollout")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAgentReplyDoesNotReorderMainConversation(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	h.exec(owner, h.env("conversation.open_background", workTarget(w.WorkItemID), struct{}{}, nil), OpenAgentConversation{})
	id := "work-item:" + w.WorkItemID
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: "main-sort", Author: "owner", Body: "main"}); err != nil {
			return err
		}
		if _, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: "background-sort", Author: "owner", Body: "background", ConversationID: id}); err != nil {
			return err
		}
		before, err := store.AgentConversation(t.Context(), tx.Q(), "main")
		if err != nil {
			return err
		}
		if _, err = tx.InsertMessage(t.Context(), store.MessageRow{MessageID: "background-reply-sort", Author: "main_agent", Body: "reply", ReplyTo: "background-sort"}); err != nil {
			return err
		}
		after, err := store.AgentConversation(t.Context(), tx.Q(), "main")
		if err != nil {
			return err
		}
		if before.LastMessageSeq != after.LastMessageSeq {
			t.Fatal("background activity reordered main")
		}
		c, err := store.AgentConversation(t.Context(), tx.Q(), id)
		if err != nil {
			return err
		}
		if c.Preview != "reply" {
			t.Fatal("background latest message missing", c)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAgentInitialWorkItemSourceIsAssociatedAfterCreation(t *testing.T) {
	h := newHarness(t)
	source := h.ownerEvidence().Reference
	p := protocol.CreateWorkItemPayload{IssuePlan: workPlan(), SourceMessageID: source, CommitSeq: 1}
	op := h.exec(mainAgent, h.env("work_items.create", workTarget(""), p, nil), CreateIssue{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var item protocol.WorkItem
	if err := json.Unmarshal(op.Result, &item); err != nil {
		t.Fatal(err)
	}
	links := h.associations(source)
	if len(links.Items) != 1 || links.Items[0].WorkItemID != item.WorkItemID {
		t.Fatal("initial source was bound before work item existed and then lost", links)
	}
}

func dispositionForTest(ids []string) string {
	if len(ids) == 0 {
		return "unassigned"
	}
	return "linked"
}

// reviewBackgroundOwnerNotices simulates the main coordinator reviewing the
// original owner source before releasing a direct background message.
func (h *harness) reviewBackgroundOwnerNotices() {
	h.t.Helper()
	for {
		var pending bool
		if err := h.store.Read(h.t.Context(), func(tx *store.ReadTx) error {
			return tx.Q().QueryRowContext(h.t.Context(), `SELECT EXISTS(SELECT 1 FROM outbox o JOIN conversation_return_links r ON r.message_id=json_extract(o.payload_json,'$.message_id') WHERE o.destination='main_thread' AND o.status='pending')`).Scan(&pending)
		}); err != nil {
			h.t.Fatal(err)
		}
		if !pending {
			return
		}
		d, _, ok, err := h.app.NextDelivery(h.t.Context(), MainThreadDestination)
		if err != nil || !ok {
			h.t.Fatal("main review delivery", ok, err)
		}
		var p HandleMessage
		if err := json.Unmarshal(d.Payload, &p); err != nil {
			h.t.Fatal(err)
		}
		turn := store.MainTurn{RequestID: d.RequestID, MessageID: p.MessageID, ThreadID: mainAgent.Thread, TurnID: "review-" + d.RequestID, Status: store.TurnStarted}
		if err := h.app.PutMainTurn(h.t.Context(), turn); err != nil {
			h.t.Fatal(err)
		}
		state, err := h.app.LogicalTurn(h.t.Context(), d.RequestID)
		if err != nil {
			h.t.Fatal(err)
		}
		for _, m := range state.Messages {
			if m.Disposition == "pending" {
				if op := h.associate(mainAgent, m.MessageID); op.Status != protocol.OpSucceeded {
					h.t.Fatal(op)
				}
			}
		}
		for _, c := range state.Contexts {
			h.prepareAgent(c.WorkItemID)
		}
		commitFixtureTurn(h.t, h, d.RequestID)
		turn.Status = store.TurnCompleted
		if err := h.app.PutMainTurn(h.t.Context(), turn); err != nil {
			h.t.Fatal(err)
		}
		if err := h.app.FinishDelivery(h.t.Context(), d, DeliveryResult{Outcome: Delivered}); err != nil {
			h.t.Fatal(err)
		}
	}
}
