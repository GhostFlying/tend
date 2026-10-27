package app

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func (h *harness) backgroundPage(message string, after int64) protocol.BackgroundSourcePage {
	h.t.Helper()
	var p protocol.BackgroundSourcePage
	if err := h.store.Read(h.t.Context(), func(tx *store.ReadTx) error {
		var err error
		p, err = store.BackgroundSources(h.t.Context(), tx.Q(), message, after)
		return err
	}); err != nil {
		h.t.Fatal(err)
	}
	return p
}
func (h *harness) startBackground() (Delivery, store.MainTurn) {
	h.t.Helper()
	h.legacyMainBatches()
	d, _, ok, err := h.app.NextDelivery(h.t.Context(), MainThreadDestination)
	if err != nil || !ok {
		h.t.Fatal(err, ok)
	}
	var p HandleMessage
	if err := json.Unmarshal(d.Payload, &p); err != nil {
		h.t.Fatal(err)
	}
	turn := store.MainTurn{RequestID: d.RequestID, MessageID: p.MessageID, ThreadID: mainAgent.Thread, TurnID: "background-" + d.RequestID, Status: store.TurnStarted}
	if err := h.app.PutMainTurn(h.t.Context(), turn); err != nil {
		h.t.Fatal(err)
	}
	return d, turn
}
func (h *harness) backgroundCall(d Delivery, turn store.MainTurn, action string, payload any) protocol.Operation {
	cmd := BackgroundCommand{Action: action, MessageID: turn.MessageID, TurnID: turn.TurnID, DeliveryID: d.RequestID}
	op, err := h.app.Execute(h.t.Context(), mainAgent, h.env(cmd.Operation(), protocol.Target{Type: "background", ID: turn.MessageID}, payload, nil), cmd)
	if err != nil {
		var e *protocol.Error
		if errors.As(err, &e) {
			return protocol.Operation{Status: protocol.OpFailed, Error: e}
		}
		h.t.Fatal(err)
	}
	return op
}
func TestBackgroundCoalescesPendingFreezesAndCancelsOnlySource(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	r1 := h.followup(w, protocol.FollowupSpec{Kind: "at", At: h.clock.Now().UnixMilli()})
	r2 := h.followup(w, protocol.FollowupSpec{Kind: "at", At: h.clock.Now().UnixMilli()})
	if h.count("background_batches") != 1 || h.count("background_sources") != 2 {
		t.Fatal("did not coalesce")
	}
	if op := h.controlFollowup(r1, "cancel", nil); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	d, turn := h.startBackground()
	page := h.backgroundPage(turn.MessageID, 0)
	if page.Batch.SourceCount != 1 || len(page.Sources) != 2 || page.Sources[0].CancelledAt == 0 || page.Batch.Status != "frozen" {
		t.Fatal(page)
	}
	read := h.backgroundCall(d, turn, "sources", map[string]any{})
	var modelPage protocol.BackgroundSourcePage
	if err := json.Unmarshal(read.Result, &modelPage); err != nil || read.Status != protocol.OpSucceeded {
		t.Fatal(read, err)
	}
	if modelPage.Sources[0].Body == page.Sources[0].Body || modelPage.Sources[0].Data != nil || modelPage.Sources[1].Body != page.Sources[1].Body {
		t.Fatal("cancelled input leaked through source tool, or active source was lost")
	}
	if op := h.controlFollowup(r2, "cancel", nil); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.occurrences(r2.RuleID)[0].Status != "processing" {
		t.Fatal("claimed input falsely cancelled")
	}
	h.followup(w, protocol.FollowupSpec{Kind: "at", At: h.clock.Now().UnixMilli()})
	if h.count("background_batches") != 2 {
		t.Fatal("frozen batch mutated")
	}
	if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Unknown}); err != nil {
		t.Fatal(err)
	}
	if h.backgroundPage(turn.MessageID, 0).Batch.Status != "unknown" {
		t.Fatal("unknown hidden")
	}
}
func TestBackgroundSilentChecksBufferTextAndBindCurrentTurn(t *testing.T) {
	for _, silent := range []bool{false, true} {
		t.Run(map[bool]string{false: "visible", true: "silent"}[silent], func(t *testing.T) {
			h := newHarness(t)
			w := h.createWork(workPlan())
			r := h.followup(w, protocol.FollowupSpec{Kind: "at", At: h.clock.Now().UnixMilli()})
			d, turn := h.startBackground()
			before := h.count("conversation_messages")
			op := h.exec(mainAgent, h.env("conversation.record_agent_message", protocol.Target{Type: "conversation"}, recordAgentMessagePayload{Body: "No changes today", ReplyTo: turn.MessageID, TurnID: turn.TurnID, ItemID: "reply", Status: protocol.AssistantCompleted}, nil), RecordAgentMessage{})
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if h.count("conversation_messages") != before {
				t.Fatal("background text leaked before settling")
			}
			if silent {
				if op := h.backgroundCall(d, turn, "checkin", protocol.BackgroundCheckin{Note: "waiting"}); op.Status != protocol.OpFailed {
					t.Fatal("unread source accepted", op)
				}
				if op := h.backgroundCall(d, turn, "sources", map[string]any{"after": 0}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				if op := h.backgroundCall(d, turn, "checkin", protocol.BackgroundCheckin{Note: "waiting", EvidenceIDs: []string{"invented"}}); op.Status != protocol.OpFailed {
					t.Fatal("invented evidence accepted")
				}
				if op := h.backgroundCall(d, turn, "checkin", protocol.BackgroundCheckin{Note: "waiting", EvidenceIDs: []string{r.RuleID}}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			turn.Status = store.TurnCompleted
			if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
				t.Fatal(err)
			}
			want := before + 1
			if silent {
				want = before
			}
			if h.count("conversation_messages") != want {
				t.Fatal("wrong publication")
			}
			if op := h.backgroundCall(d, turn, "checkin", protocol.BackgroundCheckin{Note: "late"}); op.Status != protocol.OpFailed {
				t.Fatal("completed turn accepted check-in")
			}
			if silent && h.occurrences(r.RuleID)[0].Checkin == nil {
				t.Fatal("silent history lost")
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error { _, err := store.ReadCoordinationMetrics(t.Context(), tx.Q()); return err }); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestBackgroundSourcesPaginateAndRequireAllPages(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	for range 51 {
		h.followup(w, protocol.FollowupSpec{Kind: "at", At: h.clock.Now().UnixMilli()})
	}
	d, turn := h.startBackground()
	op := h.backgroundCall(d, turn, "sources", map[string]any{"after": 0})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var page protocol.BackgroundSourcePage
	if err := json.Unmarshal(op.Result, &page); err != nil {
		t.Fatal(err)
	}
	if !page.More || len(page.Sources) != 50 {
		t.Fatal(page)
	}
	if op := h.backgroundCall(d, turn, "checkin", protocol.BackgroundCheckin{Note: "waiting"}); op.Status != protocol.OpFailed {
		t.Fatal("unread final page accepted")
	}
	if op := h.backgroundCall(d, turn, "sources", map[string]any{"after": page.Next}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.backgroundCall(d, turn, "checkin", protocol.BackgroundCheckin{Note: "waiting"}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	prompt, err := h.app.BackgroundPrompt(t.Context(), turn.MessageID)
	if err != nil || len(prompt) > 2000 || strings.Contains(prompt, "Recheck agreed work") {
		t.Fatal(prompt, err)
	}
}
func TestBackgroundRestartPublishesInterruptedText(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	h.followup(w, protocol.FollowupSpec{Kind: "at", At: h.clock.Now().UnixMilli()})
	_, turn := h.startBackground()
	before := h.count("conversation_messages")
	h.exec(mainAgent, h.env("conversation.record_agent_message", protocol.Target{Type: "conversation"}, recordAgentMessagePayload{Body: "partial result", ReplyTo: turn.MessageID, TurnID: turn.TurnID, ItemID: "reply", Status: protocol.AssistantStreaming}, nil), RecordAgentMessage{})
	h.app = New(h.store, h.ids, h.clock.Now, WithFollowups(true))
	if err := h.app.SettleOpenTurns(t.Context(), mainAgent.Thread); err != nil {
		t.Fatal(err)
	}
	if h.count("conversation_messages") != before+1 || h.backgroundPage(turn.MessageID, 0).Batch.Status != "interrupted" {
		t.Fatal("restart lost actual partial output")
	}
	h.clock.Advance(time.Hour)
}

func TestBackgroundCompletionAndConditionShareBatchPerWorkItem(t *testing.T) {
	h := newHarness(t)
	taskID, accept := h.toAwaitingAcceptance()
	plan := workPlan()
	plan.Stages[0].TaskIDs = []string{taskID}
	w := h.createWork(plan)
	r := h.followup(w, protocol.FollowupSpec{Kind: "condition", Condition: &protocol.FollowupCondition{Kind: "tasks_completed", TaskIDs: []string{taskID}}})
	if op := h.submitVerdict(accept, protocol.VerdictPassed); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var message string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT message_id FROM background_batches WHERE work_item_id=?`, w.WorkItemID).Scan(&message)
	}); err != nil {
		t.Fatal(err)
	}
	page := h.backgroundPage(message, 0)
	if len(page.Sources) != 2 || page.Sources[0].Kind != "task.completed" || page.Sources[1].RuleID != r.RuleID {
		t.Fatal(page)
	}
	// The original task's implicit work item retains a separate review obligation.
	if h.count("background_batches") != 2 || h.count("work_item_completion_reviews") != 2 {
		t.Fatal("multi-item responsibility lost")
	}
	// Claim both deliveries, and ensure the explicit item's unresolved acceptance
	// cannot be consumed by a silent check-in even after reading all sources.
	for range 2 {
		d, turn := h.startBackground()
		if turn.MessageID == message {
			if op := h.backgroundCall(d, turn, "sources", map[string]any{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if op := h.backgroundCall(d, turn, "checkin", protocol.BackgroundCheckin{Note: "waiting"}); op.Status != protocol.OpFailed {
				t.Fatal("unreviewed acceptance consumed silently")
			}
		}
		turn.Status = store.TurnCompleted
		if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
			t.Fatal(err)
		}
		if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Delivered}); err != nil {
			t.Fatal(err)
		}
	}
}
func TestBackgroundOwnerPriorityAndCurrentTurnIdentity(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	h.followup(w, protocol.FollowupSpec{Kind: "at", At: h.clock.Now().UnixMilli()})
	h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{Body: "Please use the revised constraints"}, nil), SubmitMessage{})
	d, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || !ok || d.Operation != OpHandleMessage {
		t.Fatal(d, err)
	}
	if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Delivered}); err != nil {
		t.Fatal(err)
	}
	d, turn := h.startBackground()
	wrong := turn
	wrong.TurnID = "foreign-turn"
	if op := h.backgroundCall(d, wrong, "sources", map[string]any{}); op.Status != protocol.OpFailed {
		t.Fatal("foreign turn read authenticated source")
	}
}

func TestBackgroundMainPlanningDoesNotWakeItselfForKnownCondition(t *testing.T) {
	h := newHarness(t)
	taskID, accept := h.toAwaitingAcceptance()
	plan := workPlan()
	plan.Stages[0].TaskIDs = []string{taskID}
	w := h.createWork(plan)
	if op := h.submitVerdict(accept, protocol.VerdictPassed); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	h.app.followupsEnabled = true
	source := h.ownerEvidence().Reference
	before := h.count("outbox")
	cmd := FollowupCommand{Action: "create"}
	op := h.exec(mainAgent, h.env(cmd.Operation(), workTarget(w.WorkItemID), protocol.FollowupSpec{Purpose: "Existing completion", Instruction: "Read accepted facts", SourceID: source, Kind: "condition", Timezone: "UTC", Condition: &protocol.FollowupCondition{Kind: "tasks_completed", TaskIDs: []string{taskID}}}, rev(w.Revision)), cmd)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var r protocol.FollowupRule
	if err := json.Unmarshal(op.Result, &r); err != nil {
		t.Fatal(err)
	}
	if r.Status != "finished" || len(h.occurrences(r.RuleID)) != 0 || h.count("outbox") != before {
		t.Fatal("current planning produced another wake", r)
	}
}

func TestBackgroundRetryDoesNotInheritAnotherTurnsCheckin(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	h.followup(w, protocol.FollowupSpec{Kind: "at", At: h.clock.Now().UnixMilli()})
	d, turn := h.startBackground()
	for _, action := range []string{"sources", "checkin"} {
		var payload any = map[string]any{}
		if action == "checkin" {
			payload = protocol.BackgroundCheckin{Note: "waiting"}
		}
		if op := h.backgroundCall(d, turn, action, payload); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	turn.Status = store.TurnRejected
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	turn.TurnID += "-retry"
	turn.Status = store.TurnStarted
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	before := h.count("conversation_messages")
	for _, item := range []string{"z-first", "a-second"} {
		op := h.exec(mainAgent, h.env("conversation.record_agent_message", protocol.Target{Type: "conversation"}, recordAgentMessagePayload{Body: item, ReplyTo: turn.MessageID, TurnID: turn.TurnID, ItemID: item}, nil), RecordAgentMessage{})
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	turn.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	if h.count("conversation_messages") != before+2 || h.backgroundPage(turn.MessageID, 0).Batch.Checkin != nil {
		t.Fatal("retry inherited a stale silent disposition")
	}
	var bodies []string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		rows, err := tx.Q().QueryContext(t.Context(), `SELECT body FROM conversation_messages WHERE reply_to=? AND author='main_agent' ORDER BY seq`, turn.MessageID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var body string
			if err := rows.Scan(&body); err != nil {
				return err
			}
			bodies = append(bodies, body)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(bodies, ",") != "z-first,a-second" {
		t.Fatalf("reply arrival order changed: %v", bodies)
	}
}

func TestBackgroundTranscriptKeepsLegacyFollowupTurns(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	r := h.followup(w, protocol.FollowupSpec{Kind: "at", At: h.clock.Now().UnixMilli()})
	original := h.occurrences(r.RuleID)[0].MessageID
	assertVisible := func(want bool) {
		t.Helper()
		var found bool
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			blocks, err := store.TranscriptBlocks(t.Context(), tx.Q(), 0, 20)
			for _, b := range blocks {
				if b.ID == original {
					found = true
				}
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if found != want {
			t.Fatalf("original followup visible=%v, want %v", found, want)
		}
	}
	assertVisible(false)
	// Old deliveries keep their original message identity and have no batch
	// source link. Their visible result must survive the projection upgrade.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM background_sources WHERE message_id=?`, original)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.app.PutMainTurn(t.Context(), store.MainTurn{RequestID: "legacy-request", MessageID: original, ThreadID: mainAgent.Thread, TurnID: "legacy-turn", Status: store.TurnStarted}); err != nil {
		t.Fatal(err)
	}
	assertVisible(true)
}

// legacyMainBatches models already-persisted pre-D112 main-owned batches. New
// production events are always routed to work-item conversations; these tests
// continue checking the old frozen batch identities and publication protocol.
func (h *harness) legacyMainBatches() {
	h.t.Helper()
	if err := h.store.Write(h.t.Context(), func(tx *store.Tx) error {
		if _, err := tx.Exec(h.t.Context(), `DELETE FROM message_dispositions WHERE message_id IN (SELECT message_id FROM background_batches WHERE status='pending')`); err != nil {
			return err
		}
		if _, err := tx.Exec(h.t.Context(), `UPDATE conversation_messages SET conversation_id='main' WHERE message_id IN (SELECT message_id FROM background_batches WHERE status='pending')`); err != nil {
			return err
		}
		_, err := tx.Exec(h.t.Context(), `UPDATE outbox SET destination='main_thread' WHERE status='pending' AND operation='main_thread.handle_event'`)
		return err
	}); err != nil {
		h.t.Fatal(err)
	}
}
