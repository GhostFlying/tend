package views

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

type schedulingQueries struct {
	store.Querier
	queries, builds int
}

func (q *schedulingQueries) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	q.queries++
	if strings.Contains(query, "FROM issue_manager_tasks m JOIN plan_actions a USING(task_id)") {
		q.builds++
	}
	return q.Querier.QueryContext(ctx, query, args...)
}

func (q *schedulingQueries) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	q.queries++
	return q.Querier.QueryRowContext(ctx, query, args...)
}

func schedulingIssue(id string) protocol.Issue {
	return protocol.Issue{IssueControl: protocol.IssueControl{IssueID: id, State: protocol.IssueActive, WorkStatus: protocol.IssueTodo, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: "fixture-owner"}, AssignmentEpoch: 1, AssignmentState: "active"}, Revision: 1, Plan: protocol.IssuePlan{Title: "Scheduling fixture", Project: "default", Intent: "Preserve snapshot facts", Stages: []protocol.IssueStage{}}, DependsOn: []string{}, CreatedAt: 1, UpdatedAt: 1}
}

func saveSchedulingIssue(ctx context.Context, tx *store.Tx, issue protocol.Issue, previous int64) error {
	return tx.SaveCanonicalIssue(ctx, issue, previous, store.IssueRevisionProvenance{Actor: protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "fixture-owner", Client: "fixture"}, RequestID: "req_fixture", Reason: "scheduling fixture"})
}

func TestCanonicalIssueSharesFactsWithinSnapshot(t *testing.T) {
	ctx := t.Context()
	db, err := viewFixture.Open(ctx, filepath.Join(t.TempDir(), "ledger.db"), testkit.NewClock().Now)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	issue := schedulingIssue("wi_00000000000000000000000001")
	if err := db.Write(ctx, func(tx *store.Tx) error { return saveSchedulingIssue(ctx, tx, issue, 0) }); err != nil {
		t.Fatal(err)
	}
	if err := db.Read(ctx, func(tx *store.ReadTx) error {
		q := &schedulingQueries{Querier: tx.Q()}
		view, err := CanonicalIssue(ctx, q, issue.IssueID, 0)
		if err != nil {
			return err
		}
		if q.builds != 1 || view.Revision != 1 || view.Conversation == nil || view.WorkStatus != protocol.IssueTodo {
			t.Fatalf("detail facts=%d view=%+v", q.builds, view)
		}
		updated := issue
		updated.Revision, updated.Plan.Title = 2, "New committed definition"
		if err := db.Write(ctx, func(tx *store.Tx) error { return saveSchedulingIssue(ctx, tx, updated, 1) }); err != nil {
			return err
		}
		retained, err := CanonicalIssue(ctx, tx.Q(), issue.IssueID, 0)
		if err != nil {
			return err
		}
		if retained.Revision != 1 || retained.Plan.Title != issue.Plan.Title {
			t.Fatal("read escaped its snapshot", retained)
		}
		q.builds = 0
		historical, err := CanonicalIssue(ctx, q, issue.IssueID, 1)
		if err != nil {
			return err
		}
		if !historical.Historical || q.builds != 0 || historical.Conversation != nil {
			t.Fatal("historical view acquired live facts", historical, q.builds)
		}
		q.builds = 0
		empty, err := CanonicalIssues(ctx, q, CanonicalIssueFilter{Project: "absent", Limit: 10})
		if err != nil {
			return err
		}
		if len(empty.Items) != 0 || q.builds != 0 {
			t.Fatal("empty listing built scheduling facts", empty, q.builds)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Read(ctx, func(tx *store.ReadTx) error {
		fresh, err := CanonicalIssue(ctx, tx.Q(), issue.IssueID, 0)
		if err == nil && (fresh.Revision != 2 || fresh.Plan.Title != "New committed definition") {
			t.Fatal("facts escaped into another transaction", fresh)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestInitialOwnerCandidatesBatchIndependentOfCandidateCount(t *testing.T) {
	ctx := t.Context()
	db, err := viewFixture.Open(ctx, filepath.Join(t.TempDir(), "ledger.db"), testkit.NewClock().Now)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Write(ctx, func(tx *store.Tx) error {
		if err := tx.InsertAgentCategory(ctx, protocol.AgentCategory{CategoryID: "fixture-manager", Revision: 1, Title: "Fixture", State: "active"}, "req_fixture"); err != nil {
			return err
		}
		for i := 0; i < 32; i++ {
			issue := schedulingIssue(fmt.Sprintf("wi_%026d", i+1))
			issue.OwnerSelection = &protocol.IssueOwnerSelection{CategoryID: "fixture-manager"}
			if i == 1 {
				issue.ParentIssueID = fmt.Sprintf("wi_%026d", 1)
			}
			if err := saveSchedulingIssue(ctx, tx, issue, 0); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Read(ctx, func(tx *store.ReadTx) error {
		q := &schedulingQueries{Querier: tx.Q()}
		candidates, err := initialIssueOwnerCandidates(ctx, q)
		if err != nil {
			return err
		}
		if len(candidates) != 32 || q.queries > 3 {
			t.Fatalf("candidate count=%d queries=%d", len(candidates), q.queries)
		}
		for _, c := range candidates {
			if !c.InitialIssue || !c.DependenciesDone || c.AdmissionMessage != "" || c.Manager.CategoryID != "fixture-manager" || c.Manager.ExpectedAssignmentEpoch != 1 {
				t.Fatal("batch changed admission facts", c)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
