package app

import (
	"bytes"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestNamedMainClassifiesExistingCanonicalSourceWithoutReviewingIssue(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	other := createCanonicalOwnerTestIssue(t, h)
	source, delivery := h.say("Additional owner material for the existing responsibility")
	main := startHostedReview(t, h, "main", delivery)
	principal := protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "named-main", Thread: main.ThreadID}
	associations := func() protocol.MessageAssociations {
		t.Helper()
		var a protocol.MessageAssociations
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			var err error
			a, err = store.MessageAssociations(t.Context(), tx.Q(), source)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return a
	}
	before := associations()
	p := protocol.SetIssueMessageAssociationsPayload{Disposition: "linked", ContentRevision: before.ContentRevision, IssueIDs: []string{issue.IssueID}}
	cmd := HostedCoordinationCommand{Command: SetIssueMessageAssociations{}, ExecutionID: main.RequestID, EffectSeq: 1}
	original := h.env(cmd.Operation(), protocol.Target{Type: "message", ID: source}, p, rev(before.Revision))
	op, err := h.app.Execute(t.Context(), principal, original, cmd)
	if err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal(op, err)
	}
	originalResult := op.Result
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var notices int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT count(*) FROM background_sources s JOIN background_batches b USING(batch_id) WHERE b.work_item_id=? AND s.message_id=? AND s.kind='conversation.associations_changed'`, issue.IssueID, source).Scan(&notices); err != nil {
			return err
		}
		if notices != 1 {
			t.Fatal("new source did not wake its actual manager", notices)
		}
		var pending bool
		err := tx.Q().QueryRowContext(t.Context(), `SELECT needs_review FROM message_context_obligations WHERE work_item_id=? AND message_id=?`, issue.IssueID, source).Scan(&pending)
		if err == nil && !pending {
			t.Fatal("planner classification cleared manager source duty")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// The owner's explicit correction is still authoritative under the same CAS path.
	if op := h.associate(owner, source, other.IssueID); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	current := associations()
	stale := HostedCoordinationCommand{Command: SetIssueMessageAssociations{}, ExecutionID: main.RequestID, EffectSeq: 2}
	op, err = h.app.Execute(t.Context(), principal, h.env(stale.Operation(), protocol.Target{Type: "message", ID: source}, p, rev(current.Revision)), stale)
	if err != nil {
		t.Fatal(err)
	}
	if op.Error == nil || op.Error.Code != protocol.CodeInvalidRequest {
		t.Fatal("planner overrode owner source correction", op)
	}
	commitFixtureTurn(t, h, main.RequestID)
	main.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), main); err != nil {
		t.Fatal(err)
	}
	state, err := h.app.LogicalTurn(t.Context(), main.RequestID)
	if err != nil || state.Status != "completed" {
		t.Fatal("main could not settle routed material", state, err)
	}
	counts := map[string]int{}
	for _, table := range []string{"events", "outbox", "message_association_history", "hosted_coordination_effects"} {
		counts[table] = h.count(table)
	}
	replay, err := h.app.Execute(t.Context(), principal, original, cmd)
	if err != nil || replay.Status != protocol.OpSucceeded || !bytes.Equal(replay.Result, originalResult) {
		t.Fatal("original classification replay changed", replay, err)
	}
	for table, count := range counts {
		if h.count(table) != count {
			t.Fatal("replay emitted duplicate classification/wake", table)
		}
	}

}
