package views

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestIssueResponsibilityProjectionFollowsCurrentRouteAndKeepsOrigin(t *testing.T) {
	ctx := t.Context()
	db, err := viewFixture.Open(ctx, filepath.Join(t.TempDir(), "ledger.db"), testkit.NewClock().Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	parent, child := "wi_00000000000000000000000001", "wi_00000000000000000000000002"
	if err := db.Write(ctx, func(tx *store.Tx) error {
		for _, id := range []string{parent, child} {
			issue := protocol.Issue{IssueControl: protocol.IssueControl{IssueID: id, State: protocol.IssueActive, WorkStatus: protocol.IssueInProgress, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: "fixture-owner"}, AssignmentEpoch: 1, AssignmentState: "active"}, Revision: 1, Plan: protocol.IssuePlan{Title: "Continuing responsibility", Project: "default", Intent: "Keep current work visible", Stages: []protocol.IssueStage{}}, DependsOn: []string{}, CreatedAt: 1, UpdatedAt: 1}
			if id == child {
				issue.ParentIssueID = parent
			}
			if err := tx.SaveCanonicalIssue(ctx, issue, 0, store.IssueRevisionProvenance{Actor: protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "fixture-owner", Client: "fixture"}, RequestID: "req_fixture", Reason: "projection fixture"}); err != nil {
				return err
			}
		}
		if err := tx.InsertIssueProgress(ctx, parent, protocol.IssueProgress{Summary: "Implementation finished; settlement remains", NextStep: "Resolve the original deployment settlement", IssueRevision: 1, UpdatedAt: 1, UpdatedBy: protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: "fixture-owner"}}); err != nil {
			return err
		}
		if err := tx.InsertTask(ctx, store.TaskRow{TaskID: "task-history", Title: "Finished implementation", Lifecycle: protocol.TaskCompleted, ControlMode: protocol.ControlManaged, ControlRevision: 1, SpecVersion: 1, Rating: protocol.RatingP2, BlockReason: protocol.BlockNone, CreatedAt: 1, UpdatedAt: 1}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO task_specs(task_id,spec_version,body_json,created_at) VALUES('task-history',1,'{}',1)`); err != nil {
			return err
		}
		if err := tx.InsertTaskIssueOwner(ctx, "task-history", parent); err != nil {
			return err
		}
		add := func(id, origin, responsible, recipient, status string, routed bool) error {
			a := protocol.Attention{AttentionID: id, IssueID: origin, Kind: "action", Title: id, Revision: 1, Status: status, RaisedBy: protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "fixture-owner"}, CreatedAt: 1, UpdatedAt: 1}
			if err := tx.SaveAttention(ctx, a, 0, "req_fixture"); err != nil {
				return err
			}
			if !routed {
				return nil
			}
			return tx.SaveAttentionResponsibility(ctx, id, &protocol.AttentionResponsibility{OriginIssueID: origin, OriginAssignmentEpoch: 1, IssueID: responsible, RecipientKind: recipient, RecipientRef: "fixture-agent", AssignmentEpoch: 1}, 0, "req_fixture")
		}
		for n := 0; n < 53; n++ {
			origin := child
			if n == 0 {
				origin = parent // Matching origin and route must still appear once.
			}
			if err := add(fmt.Sprintf("attn_%03d", n), origin, parent, "agent", "open", true); err != nil {
				return err
			}
		}
		for _, a := range []struct {
			id, origin, responsible, recipient, status string
			routed                                     bool
		}{
			{"attn_legacy", parent, parent, "owner", "open", false},
			{"attn_owner", child, parent, "owner", "open", true},
			{"attn_moved", parent, child, "agent", "open", true},
			{"attn_main", child, "", "main", "open", true},
			{"attn_resolved", parent, parent, "owner", "resolved", true},
		} {
			if err := add(a.id, a.origin, a.responsible, a.recipient, a.status, a.routed); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Read(ctx, func(tx *store.ReadTx) error {
		page, err := CanonicalIssues(ctx, tx.Q(), CanonicalIssueFilter{Board: true, Limit: 1})
		if err != nil {
			return err
		}
		if len(page.Items) != 1 || page.Items[0].IssueID != parent {
			t.Fatalf("root card = %+v", page)
		}
		card := page.Items[0].Board
		if card.AttentionCount != 55 || card.OwnerAttentionCount != 2 || card.Execution.Completed != 1 || card.Status != protocol.IssueInProgress || card.Progress == nil || card.Progress.NextStep != "Resolve the original deployment settlement" {
			t.Fatalf("current responsibility hidden behind historical execution: %+v", card)
		}
		detail, err := CanonicalIssue(ctx, tx.Q(), parent, 0)
		if err != nil {
			return err
		}
		if detail.Attentions == nil || len(detail.Attentions.Items) != 50 || !detail.Attentions.More || detail.Attentions.Next == "" {
			t.Fatalf("current responsibility preview = %+v", detail.Attentions)
		}
		seen := map[string]bool{}
		filter := AttentionFilter{ResponsibleIssueID: parent, Status: "open", ActionableOnly: true, IncludeArchived: true, Limit: 7}
		for {
			page, err := Attentions(ctx, tx.Q(), filter)
			if err != nil {
				return err
			}
			for _, a := range page.Items {
				if seen[a.AttentionID] {
					t.Fatalf("duplicate original Attention %s", a.AttentionID)
				}
				seen[a.AttentionID] = true
				if a.AttentionID == "attn_001" && (a.IssueID != child || a.Responsibility.IssueID != parent) {
					t.Fatalf("origin or current responsibility rewritten: %+v", a)
				}
			}
			if !page.More {
				break
			}
			if page.Next <= filter.After {
				t.Fatal("responsibility cursor did not advance")
			}
			filter.After = page.Next
		}
		if len(seen) != card.AttentionCount || seen["attn_moved"] || seen["attn_main"] || seen["attn_resolved"] {
			t.Fatalf("current route count disagrees: %v", seen)
		}
		childDetail, err := CanonicalIssue(ctx, tx.Q(), child, 0)
		if err != nil {
			return err
		}
		if len(childDetail.Attentions.Items) != 1 || childDetail.Attentions.Items[0].AttentionID != "attn_moved" {
			t.Fatalf("origin was confused with current responsibility: %+v", childDetail.Attentions)
		}
		history, err := Attentions(ctx, tx.Q(), AttentionFilter{IssueID: child, IncludeArchived: true, Limit: 100})
		if err != nil {
			return err
		}
		if len(history.Items) != 54 {
			t.Fatalf("origin history was lost: %d", len(history.Items))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
