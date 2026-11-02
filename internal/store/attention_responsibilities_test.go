package store

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestResponsibilityMigrationBackfillsOpenIssuesWithoutRewritingHistory(t *testing.T) {
	ctx := t.Context()
	db, err := sql.Open("sqlite", dsn(filepath.Join(t.TempDir(), "old.db"), true))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	old := &Store{write: db, read: db, now: testkit.NewClock().Now}
	t.Cleanup(func() { old.Close() })
	if err = old.migrate(ctx, 74); err != nil {
		t.Fatal(err)
	}
	issue := canonicalIssueFixture("wi_00000000000000000000000008")
	issue.Assignee = protocol.IssueAssignee{Kind: protocol.IssueAssigneeAgent, Ref: "fixture-agent"}
	original := protocol.Attention{AttentionID: "attention-original", IssueID: issue.IssueID, Kind: "action", Status: "open", Revision: 4, Title: "Retain original evidence", Resolution: "", CreatedAt: 1, UpdatedAt: 2}
	if err = old.Write(ctx, func(tx *Tx) error {
		if err := tx.SaveCanonicalIssue(ctx, issue, 0, issueProvenance()); err != nil {
			return err
		}
		return tx.SaveAttention(ctx, original, 0, "req_original")
	}); err != nil {
		t.Fatal(err)
	}
	review := canonicalIssueFixture("wi_00000000000000000000000009")
	review.ParentIssueID = issue.IssueID
	review.Assignee = protocol.IssueAssignee{Kind: protocol.IssueAssigneeAgent, Ref: "fixture-reviewer"}
	saveCanonicalFixture(t, old, review, 0)
	insertTask(t, old, "task_historical_review")
	if err := insertAction(t, old, "action_historical_review", "task_historical_review", "review"); err != nil {
		t.Fatal(err)
	}
	mustExec(t, old, `INSERT INTO runs(run_id,task_id,action_id,generation,role,desired_state,observed_state,created_at,updated_at) VALUES('run_historical_review','task_historical_review','action_historical_review',1,'accept','run','running',1,1)`)
	mustExec(t, old, `INSERT INTO run_issue_responsibilities(run_id,issue_id,assignment_epoch,issue_revision,agent_ref,created_at) VALUES('run_historical_review',?,1,1,?,1)`, review.IssueID, review.Assignee.Ref)
	mustExec(t, old, `INSERT INTO task_owner_contracts(task_id,issue_id,assignment_epoch,agent_ref,agent_revision,runtime_id,runtime_revision,model,effort,review_issue_id,review_plan_json,created_at) VALUES('task_historical_review',?,1,?,1,'fixture-runtime',1,'fixture-model','high',?,'{}',1)`, issue.IssueID, issue.Assignee.Ref, review.IssueID)
	if err := old.Write(ctx, func(tx *Tx) error {
		for _, id := range []string{"decision_historical_worker", "decision_historical_environment"} {
			effects := "{}"
			if id == "decision_historical_environment" {
				effects = `{"retry the acceptance":"accept"}`
			}
			if err := tx.InsertDecision(ctx, id, "task_historical_review", "Original review problem", `[]`, effects, nil, 1); err != nil {
				return err
			}
			a := protocol.Attention{AttentionID: "attention_" + id, IssueID: issue.IssueID, TaskID: "task_historical_review", DecisionID: id, Kind: "decision", Status: "open", Revision: 1, Title: "Original review problem"}
			if err := tx.SaveAttention(ctx, a, 0, "req_historical"); err != nil {
				return err
			}
		}
		return tx.InsertWorkerQuestion(ctx, WorkerQuestionRow{DecisionID: "decision_historical_worker", RunID: "run_historical_review", ActionID: "action_historical_review", QuestionKey: "original", PayloadHash: "fixture", SpecVersion: 1})
	}); err != nil {
		t.Fatal(err)
	}
	var before string
	if err = db.QueryRowContext(ctx, `SELECT body_json FROM attention_revisions WHERE attention_id=?`, original.AttentionID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err = old.migrate(ctx, 75); err != nil {
		t.Fatal(err)
	}
	if err = old.Read(ctx, func(tx *ReadTx) error {
		a, err := GetAttention(ctx, tx.Q(), original.AttentionID)
		if err != nil {
			return err
		}
		if a.Responsibility == nil || a.Responsibility.IssueID != issue.IssueID || a.Responsibility.RecipientKind != "agent" || a.Responsibility.Revision != 1 || a.Revision != 4 {
			t.Fatal(a)
		}
		for _, id := range []string{"attention_decision_historical_worker", "attention_decision_historical_environment"} {
			a, err := GetAttention(ctx, tx.Q(), id)
			if err != nil {
				return err
			}
			if a.Responsibility == nil || a.Responsibility.OriginIssueID != review.IssueID || a.Responsibility.IssueID != review.IssueID || a.Responsibility.RecipientRef != review.Assignee.Ref || a.IssueID != issue.IssueID {
				t.Fatal("historical review problem was routed to the implementation owner or original history changed", a)
			}
		}
		var allowed bool
		if err := tx.Q().QueryRowContext(ctx, `SELECT supervisor_allowed FROM run_question_contracts WHERE run_id='run_historical_review'`).Scan(&allowed); err != nil {
			return err
		}
		if allowed {
			t.Fatal("migration silently expanded historical run answer authority")
		}
		var after string
		if err = tx.Q().QueryRowContext(ctx, `SELECT body_json FROM attention_revisions WHERE attention_id=?`, original.AttentionID).Scan(&after); err != nil {
			return err
		}
		if before != after {
			t.Fatal("migration rewrote immutable original problem")
		}
		var raw string
		if err = tx.Q().QueryRowContext(ctx, `SELECT body_json FROM attention_responsibility_history WHERE attention_id=? AND revision=1`, original.AttentionID).Scan(&raw); err != nil {
			return err
		}
		var r protocol.AttentionResponsibility
		if err = json.Unmarshal([]byte(raw), &r); err != nil {
			return err
		}
		if r.OriginIssueID != original.IssueID {
			t.Fatal(r)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `UPDATE attention_responsibility_history SET request_id='forged'`); err == nil {
		t.Fatal("responsibility history can be rewritten")
	}
	if err = old.migrate(ctx, 75); err != nil {
		t.Fatal("reopen is not idempotent", err)
	}
}
