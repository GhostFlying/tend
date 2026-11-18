package views

import (
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"testing"
)

func TestTerminalReadProjectionRetainsRawHistoryAndUnresolvedAttention(t *testing.T) {
	ctx := t.Context()
	db := newUsageResolverStore(t)
	issue := schedulingIssue("wi_00000000000000000000000001")
	issue.WorkStatus = protocol.IssueInProgress
	if err := db.Write(ctx, func(tx *store.Tx) error {
		if err := saveSchedulingIssue(ctx, tx, issue, 0); err != nil {
			return err
		}
		if err := tx.InsertIssueProgress(ctx, issue.IssueID, protocol.IssueProgress{Summary: "Earlier incomplete summary", NextStep: "Finish installation", IssueRevision: 1, UpdatedBy: issue.Assignee}); err != nil {
			return err
		}
		// Retained input models an existing closed ledger with a stale work status
		// and unresolved original action. Normal closure/reopen is tested in app.
		issue.State = protocol.IssueClosed
		issue.Revision = 2
		if err := saveSchedulingIssue(ctx, tx, issue, 1); err != nil {
			return err
		}
		for _, kind := range []string{"information", "action"} {
			if err := tx.SaveAttention(ctx, protocol.Attention{AttentionID: kind, IssueID: issue.IssueID, Kind: kind, Title: "Retain original " + kind, Revision: 1, Status: "open", BlocksClose: kind == "action", RaisedBy: protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "fixture-owner"}}, 0, "req_fixture"); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Read(ctx, func(tx *store.ReadTx) error {
		view, err := CanonicalIssue(ctx, tx.Q(), issue.IssueID, 0)
		if err != nil {
			return err
		}
		if view.State != protocol.IssueClosed || view.WorkStatus != protocol.IssueInProgress || view.EffectiveWorkStatus != protocol.IssueDone || !view.Progress.Historical {
			t.Fatalf("effective endpoint erased raw/current facts: %+v progress=%+v", view, view.Progress)
		}
		if view.Attentions == nil || len(view.Attentions.Items) != 1 || view.Attentions.Items[0].AttentionID != "action" || view.Attentions.Items[0].Historical {
			t.Fatalf("terminal display hid unresolved original action: %+v", view.Attentions)
		}
		info, err := Attention(ctx, tx.Q(), "information")
		if err != nil {
			return err
		}
		if !info.Historical || info.Status != "open" || info.HistoricalReason != "issue_ended" {
			t.Fatalf("information history lost: %+v", info)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestProgressApplicabilityTracksAssignmentWithoutHidingActions(t *testing.T) {
	ctx := t.Context()
	db := newUsageResolverStore(t)
	issue := schedulingIssue("wi_00000000000000000000000001")
	if err := db.Write(ctx, func(tx *store.Tx) error {
		if err := saveSchedulingIssue(ctx, tx, issue, 0); err != nil {
			return err
		}
		if err := tx.InsertIssueProgress(ctx, issue.IssueID, protocol.IssueProgress{Summary: "Earlier owner assessment", IssueRevision: 1, UpdatedBy: issue.Assignee}); err != nil {
			return err
		}
		issue.AssignmentEpoch = 2
		issue.Assignee.Ref = "successor"
		issue.Revision = 2
		return saveSchedulingIssue(ctx, tx, issue, 1)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Read(ctx, func(tx *store.ReadTx) error {
		p, err := issueProgressProjection(ctx, tx.Q(), issue, true)
		if err != nil {
			return err
		}
		if !p.Historical || p.HistoricalReason != "responsibility_changed" {
			t.Fatal("old owner assessment remained current", p)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
