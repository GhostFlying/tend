package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestIssueDeliveryMigrationPreservesClosedAcceptedHistoryWithoutApprovals(t *testing.T) {
	clock := testkit.NewClock()
	ctx := t.Context()
	db, err := sql.Open("sqlite", dsn(filepath.Join(t.TempDir(), "old.db"), true))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	old := &Store{write: db, read: db, now: clock.Now}
	t.Cleanup(func() { old.Close() })
	if err := old.migrate(ctx, 76); err != nil {
		t.Fatal(err)
	}
	i := canonicalIssueFixture("wi_00000000000000000000000001")
	i.State = protocol.IssueClosed
	i.Closure = &protocol.IssueClosure{Kind: protocol.WorkItemClosureCompleted, Reason: "Historical accepted implementation", Evidence: []protocol.IssueEvidence{{Kind: protocol.WorkItemEvidenceAcceptance, Reference: "acc_old", Summary: "Original independent verdict"}}}
	err = old.Write(ctx, func(tx *Tx) error {
		if err := tx.SaveCanonicalIssue(ctx, i, 0, issueProvenance()); err != nil {
			return err
		}
		task := TaskRow{TaskID: "task_old", Title: "Original implementation", Project: "default", Lifecycle: protocol.TaskCompleted, ControlMode: protocol.ControlManaged, ControlRevision: 1, SpecVersion: 1, CreatedAt: 1, UpdatedAt: 1}
		if err := tx.InsertTask(ctx, task); err != nil {
			return err
		}
		if err := tx.InsertSpec(ctx, task.TaskID, 1, `{"goal":"Original implementation","delivery":"commit"}`, "", 1); err != nil {
			return err
		}
		if err := tx.InsertTaskIssueOwner(ctx, task.TaskID, i.IssueID); err != nil {
			return err
		}
		if err := tx.InsertAction(ctx, ActionRow{ActionID: "act_old", TaskID: task.TaskID, Kind: protocol.ActionImplement, EffectKey: "original", TriggerJSON: `{}`, TargetSpecVersion: 1, AuthorizationJSON: `{}`, Generation: 1, Status: protocol.ActionDone, CreatedRequestID: "req_old", CreatedAt: 1}); err != nil {
			return err
		}
		if err := tx.InsertRun(ctx, RunRow{RunID: "run_old", TaskID: task.TaskID, ActionID: "act_old", Generation: 1, Role: protocol.RoleImplement, DesiredState: protocol.DesiredStopped, ObservedState: protocol.RunFinished, CreatedAt: 1}); err != nil {
			return err
		}
		if err := tx.InsertArtifact(ctx, "art_old", task.TaskID, "run_old", "commit", `{}`, "digest", 1); err != nil {
			return err
		}
		if err := tx.InsertAcceptance(ctx, "acc_old", task.TaskID, "run_old", 1, "art_old", protocol.VerdictPassed, `[]`, `[]`, 1); err != nil {
			return err
		}
		// Seed the original schema directly: current Task writers may include
		// columns that did not exist when this accepted history was recorded.
		if _, err := tx.Exec(ctx, `UPDATE tasks SET current_artifact_id=? WHERE task_id=?`, "art_old", task.TaskID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO events(event_id,source,object_type,object_id,kind,body_json,actor_json,created_at) VALUES('evt_old','app','task',?,'task.completed','{}','{}',1)`, task.TaskID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := old.migrate(ctx, 77); err != nil {
		t.Fatal(err)
	}
	if err := old.Read(ctx, func(tx *ReadTx) error {
		saved, err := GetCanonicalIssueRevision(ctx, tx.Q(), i.IssueID, i.Revision)
		if err != nil {
			return err
		}
		d, err := GetIssueDeliveryDuty(ctx, tx.Q(), i.IssueID, "task_old")
		if err != nil {
			return err
		}
		// This test intentionally stops at schema 77, so verify the retained
		// original fields without invoking the current-schema Task reader.
		var task TaskRow
		if err := tx.Q().QueryRowContext(ctx, `SELECT lifecycle,spec_version,control_revision,current_artifact_id FROM tasks WHERE task_id=?`, "task_old").Scan(&task.Lifecycle, &task.SpecVersion, &task.ControlRevision, &task.CurrentArtifactID); err != nil {
			return err
		}
		if saved.State != protocol.IssueClosed || saved.Closure.Reason != i.Closure.Reason || task.Lifecycle != protocol.TaskCompleted || task.SpecVersion != 1 || task.ControlRevision != 1 || task.CurrentArtifactID != "art_old" || d.Status != "preparing" || d.Candidate != nil || d.AttentionID != "" || d.AcceptanceID != "acc_old" {
			t.Fatal(saved, task, d)
		}
		for _, table := range []string{"attentions", "decisions", "issue_delivery_executions", "issue_delivery_answers"} {
			var count int
			if err := tx.Q().QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				t.Fatal("migration fabricated authority", table, count)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
