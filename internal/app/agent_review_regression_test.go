package app

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestReviewCancelledFollowupMustNotRemainDeliverable(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	h.prepareAgent(w.WorkItemID)
	r := h.followup(w, protocol.FollowupSpec{Kind: "every", EverySeconds: 300, ExpiresAt: h.clock.Now().Add(24 * time.Hour).UnixMilli()})
	h.clock.Advance(5 * time.Minute)
	h.sweepFollowups()
	r = h.readFollowup(r.RuleID)
	op := h.controlFollowup(r, "pause", nil)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	h.reviewBackgroundOwnerNotices()
	d, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("cancelled followup still deliverable: %+v", d)
	}
}

func TestReviewAssociationEditMustInvalidateCachedSummary(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	source := "source-message"
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: source, Author: "main_agent", Body: "Work item association", CreatedAt: h.clock.Now().UnixMilli()})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if op := h.associate(mainAgent, source, w.WorkItemID); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if _, err := h.app.BindMainThread(t.Context(), "main", "summary-thread", "tool-hash", ""); err != nil {
		t.Fatal(err)
	}
	before, err := h.app.HandoffSnapshot(t.Context(), "main")
	if err != nil {
		t.Fatal(err)
	}
	summary, err := h.app.SaveHandoffSummary(t.Context(), store.HandoffSummary{Instance: "main", Snapshot: before, Body: "This source belongs to the work item", FormatVersion: 2})
	if err != nil {
		t.Fatal(err)
	}
	if op := h.associate(mainAgent, source); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	after, err := h.app.HandoffSnapshot(t.Context(), "main")
	if err != nil {
		t.Fatal(err)
	}
	if summary.Snapshot.Covers(after) {
		t.Fatalf("obsolete summary still current at conversation version %d", after.Version)
	}
}

func TestReviewSummaryObservationMustIncludeCorrectedAssociations(t *testing.T) {
	h := newHarness(t)
	w1, w2 := h.createWork(workPlan()), h.createWork(workPlan())
	const source = "summary-source"
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: source, Author: "owner", Body: "Proceed with that item", CreatedAt: h.clock.Now().UnixMilli()})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	h.associate(mainAgent, source, w1.WorkItemID)
	if _, err := h.app.BindMainThread(t.Context(), "main", "summary-thread", "hash", ""); err != nil {
		t.Fatal(err)
	}
	before, err := h.app.HandoffSnapshot(t.Context(), "main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.SaveHandoffSummary(t.Context(), store.HandoffSummary{Instance: "main", Snapshot: before, Body: "Source refers to first work item", FormatVersion: 2}); err != nil {
		t.Fatal(err)
	}
	h.associate(owner, source, w2.WorkItemID)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE outbox SET status='delivered' WHERE destination='main_thread'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	input, err := h.app.HandoffSummaryInput(t.Context(), "main", 256<<10)
	if err != nil {
		t.Fatal(err)
	}
	var items []map[string]any
	if err := json.Unmarshal(input.Observations, &items); err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item["message_id"] == source {
			if item["associations"] == nil {
				t.Fatalf("source replacement omits corrected associations: %s", input.Observations)
			}
			return
		}
	}
	t.Fatal("corrected source not in summary observations")
}
