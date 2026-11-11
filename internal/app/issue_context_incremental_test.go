package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func readIncrementalContext(t *testing.T, h *harness, issue, cursor string, limit int) protocol.IssueContextIncremental {
	t.Helper()
	var out protocol.IssueContextIncremental
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		out, err = views.CanonicalIssueContextIncremental(t.Context(), tx.Q(), issue, protocol.IssueContextQuery{View: "incremental", Cursor: cursor, Limit: limit})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out
}
func finishIncrementalContext(t *testing.T, h *harness, issue, cursor string, limit int) ([]protocol.IssueContextChange, string) {
	t.Helper()
	var items []protocol.IssueContextChange
	for n := 0; n < 200; n++ {
		p := readIncrementalContext(t, h, issue, cursor, limit)
		items = append(items, p.Items...)
		cursor = p.NextCursor
		if !p.More {
			return items, cursor
		}
	}
	t.Fatal("context page did not terminate")
	return nil, ""
}
func contextTestEvent(t *testing.T, h *harness, tx *store.Tx, typ, id, kind string, body any) error {
	t.Helper()
	raw, _ := json.Marshal(body)
	_, err := tx.InsertEvent(t.Context(), store.EventRow{EventID: h.ids.New(protocol.PrefixEvent), Source: "test", ObjectType: typ, ObjectID: id, Kind: kind, BodyJSON: string(raw), CreatedAt: h.clock.Now().UnixMilli()})
	return err
}
func seedIncrementalContext(t *testing.T, h *harness, issue protocol.Issue, sources, facts int) {
	t.Helper()
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		for i := 0; i < sources; i++ {
			id := fmt.Sprintf("context-source-%s-%03d", issue.IssueID, i)
			if _, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: id, Author: "owner", Body: strings.Repeat("original body remains in material endpoint ", 40), CreatedAt: h.clock.Now().UnixMilli()}); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO message_work_items(message_id,work_item_id,source,updated_at) VALUES(?,?,'explicit',1)`, id, issue.IssueID); err != nil {
				return err
			}
			if err := contextTestEvent(t, h, tx, "message", id, "conversation.message", map[string]any{}); err != nil {
				return err
			}
		}
		for i := 0; i < facts; i++ {
			if _, err := tx.Exec(t.Context(), `INSERT INTO work_item_context_facts(fact_id,work_item_id,scope,kind,body,source_message_id,attribution,created_at) VALUES(?,?,'work_item','constraint',?,?,'owner',1)`, fmt.Sprintf("context-fact-%s-%03d", issue.IssueID, i), issue.IssueID, fmt.Sprintf("constraint %d", i), fmt.Sprintf("context-source-%s-000", issue.IssueID)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIncrementalIssueContextPagesFactsOnceWithoutSourceBodiesOrWrites(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	seedIncrementalContext(t, h, issue, 70, 4)
	beforeEvents, beforeOutbox, beforeFacts := h.count("events"), h.count("outbox"), h.count("work_item_context_facts")
	first := readIncrementalContext(t, h, issue.IssueID, "", 3)
	again := readIncrementalContext(t, h, issue.IssueID, "", 3)
	if first.Kind != "snapshot" || !first.More || first.ContentID != again.ContentID || len(first.Items) != 3 {
		t.Fatalf("snapshot: %+v", first)
	}
	seen := map[string]int{}
	items, cursor := finishIncrementalContext(t, h, issue.IssueID, "", 3)
	for _, v := range items {
		seen[v.Kind]++
		if v.Source != nil && (!v.Source.Linked || v.Source.ContentRevision != 1) {
			t.Fatal(v)
		}
	}
	if seen["fact"] != 4 || seen["source"] != 70 || seen["working"] != 1 {
		t.Fatal(seen)
	}
	raw, _ := json.Marshal(items)
	if strings.Contains(string(raw), "original body remains") {
		t.Fatal("source full text repeated in incremental index")
	}
	delta := readIncrementalContext(t, h, issue.IssueID, cursor, 3)
	same := readIncrementalContext(t, h, issue.IssueID, cursor, 3)
	if delta.Kind != "delta" || len(delta.Items) != 0 || delta.More || delta.ContentID != same.ContentID {
		t.Fatal(delta, same)
	}
	if h.count("events") != beforeEvents || h.count("work_item_context_facts") != beforeFacts || h.count("outbox") != beforeOutbox {
		t.Fatal("read wrote progress or authority")
	}
}

func TestIncrementalIssueContextTracksOldCorrectionsExactFactsAndScopedChanges(t *testing.T) {
	h := newHarness(t)
	issue, other := createCanonicalOwnerTestIssue(t, h), createCanonicalOwnerTestIssue(t, h)
	seedIncrementalContext(t, h, issue, 3, 3)
	seedIncrementalContext(t, h, other, 1, 1)
	_, cursor := finishIncrementalContext(t, h, issue.IssueID, "", 20)
	firstFact := fmt.Sprintf("context-fact-%s-000", issue.IssueID)
	otherFact := fmt.Sprintf("context-fact-%s-000", other.IssueID)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		return contextTestEvent(t, h, tx, "work_item", other.IssueID, "work_item.context_changed", map[string]any{"changed_fact_ids": []string{otherFact}})
	}); err != nil {
		t.Fatal(err)
	}
	unrelated := readIncrementalContext(t, h, issue.IssueID, cursor, 20)
	if len(unrelated.Items) != 0 {
		t.Fatalf("unrelated context re-sent facts: %+v", unrelated)
	}
	cursor = unrelated.NextCursor
	source := fmt.Sprintf("context-source-%s-001", issue.IssueID)
	unlinked := fmt.Sprintf("context-source-%s-002", issue.IssueID)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE work_item_context_facts SET superseded=1 WHERE fact_id=?`, firstFact); err != nil {
			return err
		}
		if err := contextTestEvent(t, h, tx, "work_item", issue.IssueID, "work_item.context_changed", map[string]any{"changed_fact_ids": []string{firstFact}}); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE conversation_messages SET body='corrected original' WHERE message_id=?`, source); err != nil {
			return err
		}
		if err := contextTestEvent(t, h, tx, "message", source, "conversation.associations_changed", map[string]any{}); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE message_work_items SET active=0 WHERE message_id=? AND work_item_id=?`, unlinked, issue.IssueID); err != nil {
			return err
		}
		return contextTestEvent(t, h, tx, "message", unlinked, "conversation.associations_changed", map[string]any{})
	}); err != nil {
		t.Fatal(err)
	}
	items, cursor := finishIncrementalContext(t, h, issue.IssueID, cursor, 1)
	facts, sources := 0, map[string]protocol.IssueContextChange{}
	for _, v := range items {
		if v.Fact != nil {
			facts++
			if v.ID != firstFact || !v.Removed {
				t.Fatal("unmodified fact repeated", v)
			}
		}
		if v.Source != nil {
			sources[v.ID] = v
		}
	}
	if facts != 1 || sources[source].Revision != 2 || !sources[unlinked].Removed || sources[unlinked].Source.Linked {
		t.Fatal(items)
	}
	// A scoped project/global edit originating elsewhere still reaches this Issue.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		for _, scope := range []string{"project", "global"} {
			id := "context-" + scope
			if _, err := tx.Exec(t.Context(), `INSERT INTO work_item_context_facts(fact_id,project,scope,kind,body,source_message_id,attribution,created_at) VALUES(?,?,?,'constraint','shared real constraint',?,'owner',2)`, id, issue.Plan.Project, scope, fmt.Sprintf("context-source-%s-000", other.IssueID)); err != nil {
				return err
			}
			if err := contextTestEvent(t, h, tx, "work_item", other.IssueID, "work_item.context_changed", map[string]any{"changed_fact_ids": []string{id}}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	items, _ = finishIncrementalContext(t, h, issue.IssueID, cursor, 1)
	if len(items) != 2 || items[0].Fact == nil || items[1].Fact == nil {
		t.Fatal(items)
	}
}

func TestIncrementalIssueContextPinsEventIntervalAndDoesNotSkipConcurrentSources(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	seedIncrementalContext(t, h, issue, 3, 1)
	first := readIncrementalContext(t, h, issue.IssueID, "", 1)
	late := "context-concurrent-source"
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: late, Author: "owner", Body: "concurrent source"}); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO message_work_items(message_id,work_item_id,source,updated_at) VALUES(?,?,'explicit',1)`, late, issue.IssueID); err != nil {
			return err
		}
		return contextTestEvent(t, h, tx, "message", late, "conversation.message", map[string]any{})
	}); err != nil {
		t.Fatal(err)
	}
	cutoff := first.Through
	cursor := first.NextCursor
	for first.More {
		first = readIncrementalContext(t, h, issue.IssueID, cursor, 1)
		cursor = first.NextCursor
		if first.Through != cutoff {
			t.Fatal("snapshot cutoff moved")
		}
		for _, v := range first.Items {
			if v.ID == late {
				t.Fatal("concurrent member leaked into fixed snapshot")
			}
		}
	}
	delta := readIncrementalContext(t, h, issue.IssueID, cursor, 20)
	if len(delta.Items) != 1 || delta.Items[0].ID != late || delta.Kind != "delta" {
		t.Fatal(delta)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		_, err := views.CanonicalIssueContextIncremental(t.Context(), tx.Q(), issue.IssueID, protocol.IssueContextQuery{View: "incremental", Cursor: "broken"})
		if err == nil {
			t.Fatal("bad cursor silently became empty delta")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIncrementalIssueContextWithdrawsAndResyncsOnlyRealScopeChanges(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	other := createCanonicalOwnerTestIssue(t, h)
	source := queueMessage(t, h, "original owner source before withdrawal")
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO message_work_items(message_id,work_item_id,source,updated_at) VALUES(?,?,'explicit',1)`, source, issue.IssueID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_, cursor := finishIncrementalContext(t, h, issue.IssueID, "", 10)
	op := h.exec(owner, h.env("conversation.withdraw_message", protocol.Target{Type: "message", ID: source}, protocol.WithdrawMessagePayload{}, rev(1)), WithdrawMessage{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	items, cursor := finishIncrementalContext(t, h, issue.IssueID, cursor, 10)
	if len(items) != 1 || items[0].Source == nil || !items[0].Source.Withdrawn || !items[0].Removed || len(items[0].Source.Attachments) != 0 {
		t.Fatal(items)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		_, err := views.CanonicalIssueContextIncremental(t.Context(), tx.Q(), other.IssueID, protocol.IssueContextQuery{View: "incremental", Cursor: cursor})
		if err == nil {
			t.Fatal("cursor crossed Issue scope")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	changed := h.exec(owner, h.env("issues.reparent", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.ReparentIssuePayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Change actual inherited context scope"}, ParentIssueID: other.IssueID, ExpectedParentRevision: rev(other.Revision)}, rev(issue.Revision)), CanonicalOwnerIssueCommand{Action: "reparent"})
	if changed.Status != protocol.OpSucceeded {
		t.Fatal(changed)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		_, err := views.CanonicalIssueContextIncremental(t.Context(), tx.Q(), issue.IssueID, protocol.IssueContextQuery{View: "incremental", Cursor: cursor})
		if err == nil {
			t.Fatal("old ancestry cursor remained applicable")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	fresh := readIncrementalContext(t, h, issue.IssueID, "", 10)
	if fresh.Kind != "snapshot" {
		t.Fatal(fresh)
	}
}

func TestIncrementalContextNewFactEventsIdentifyOnlyActualChanges(t *testing.T) {
	h := newHarness(t)
	source, turn := startReviewTurn(t, h)
	w := h.createWork(workPlan())
	h.associate(mainAgent, source, w.WorkItemID)
	var previous string
	for i := 0; i < 2; i++ {
		c := h.agentContext(w.WorkItemID)
		env := h.env("work_item_context.fact", workTarget(w.WorkItemID), protocol.ContextFact{Scope: "work_item", Kind: "constraint", Body: fmt.Sprintf("real constraint %d", i), SourceMessageID: source, Supersedes: previous}, rev(c.Revision))
		env.ParentRequestID = turn.RequestID
		op := h.exec(mainAgent, env, ContextCommand{Action: "fact"})
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			var raw string
			if err := tx.Q().QueryRowContext(t.Context(), `SELECT body_json FROM events WHERE request_id=? AND kind='work_item.context_changed'`, op.RequestID).Scan(&raw); err != nil {
				return err
			}
			var body struct {
				IDs []string `json:"changed_fact_ids"`
			}
			if err := json.Unmarshal([]byte(raw), &body); err != nil {
				return err
			}
			if len(body.IDs) != i+1 || body.IDs[len(body.IDs)-1] == "" || i == 1 && body.IDs[0] != previous {
				t.Fatal("fact event lost exact replacement", raw)
			}
			previous = body.IDs[len(body.IDs)-1]
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestIncrementalIssueContextBoundsUnrelatedEventScanAndReportsExpiredHistory(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	_, cursor := finishIncrementalContext(t, h, issue.IssueID, "", 10)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		for i := 0; i < 1005; i++ {
			if err := contextTestEvent(t, h, tx, "host", "unrelated", "host.observed", map[string]any{}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	first := readIncrementalContext(t, h, issue.IssueID, cursor, 10)
	if !first.More || len(first.Items) != 0 {
		t.Fatal("unrelated events became Issue changes or unbounded read", first)
	}
	next := readIncrementalContext(t, h, issue.IssueID, first.NextCursor, 10)
	if next.More || len(next.Items) != 0 || next.Through != first.Through {
		t.Fatal(next)
	}
	// A retained cursor cannot silently skip an archived boundary.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM events WHERE event_seq=?`, next.Through)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		_, err := views.CanonicalIssueContextIncremental(t.Context(), tx.Q(), issue.IssueID, protocol.IssueContextQuery{View: "incremental", Cursor: next.NextCursor})
		if err == nil {
			t.Fatal("unavailable event boundary looked synchronized")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIncrementalIssueContextSeparatesProgressDeliveryAndProviderFacts(t *testing.T) {
	h, _, issue, impl, candidate := liveReviewedDeliveryFixture(t)
	prepareLiveDeliveryForTest(t, h, issue.IssueID, impl.TaskID, candidate)
	_, cursor := finishIncrementalContext(t, h, issue.IssueID, "", 20)
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	p := protocol.UpdateIssueProgressPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Summary: "Continue original delivery", NextStep: "Inspect current provider evidence"}
	op := h.exec(owner, h.env("issues.update_progress", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(issue.Revision)), UpdateIssueProgress{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	items, cursor := finishIncrementalContext(t, h, issue.IssueID, cursor, 20)
	if len(items) != 1 || items[0].Working == nil || items[0].Working.Progress == nil {
		t.Fatal("progress repeated unchanged candidates", items)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		return contextTestEvent(t, h, tx, "work_item", issue.IssueID, "work_item.delivery_changed", map[string]any{"delivery_id": candidate.DeliveryID})
	}); err != nil {
		t.Fatal(err)
	}
	items, _ = finishIncrementalContext(t, h, issue.IssueID, cursor, 20)
	observations := 0
	for _, i := range items {
		if i.Delivery != nil {
			t.Fatal("provider fact rewrote candidate", i)
		}
		if i.Observation != nil {
			observations++
			if i.Observation.Delivery.DeliveryID != candidate.DeliveryID || i.Observation.Observation.State == "" {
				t.Fatal(i)
			}
		}
	}
	if observations != 1 {
		t.Fatal("provider current fact unavailable", items)
	}
}

func TestIncrementalIssueContextPreservesActualUnknownRunFact(t *testing.T) {
	h, _, issue, impl, _ := liveReviewedDeliveryFixture(t)
	_, cursor := finishIncrementalContext(t, h, issue.IssueID, "", 20)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE runs SET observed_state='unknown' WHERE run_id=?`, impl.RunID); err != nil {
			return err
		}
		return contextTestEvent(t, h, tx, "run", impl.RunID, "run.observed", map[string]any{"state": "unknown"})
	}); err != nil {
		t.Fatal(err)
	}
	items, _ := finishIncrementalContext(t, h, issue.IssueID, cursor, 20)
	if len(items) != 1 || items[0].Task == nil || items[0].Task.Execution == nil || items[0].Task.Execution.State != protocol.ExecutionUnknown || items[0].Task.Execution.RunID != impl.RunID {
		t.Fatal("actual unknown was hidden by current task lifecycle", items)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		r, err := store.GetRun(t.Context(), tx.Q(), impl.RunID)
		if err == nil && r.ObservedState != protocol.RunUnknown {
			t.Fatal("reading settled native unknown", r)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
