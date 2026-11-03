package store

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestCreatorPolicyMigrationPreservesAcceptedDeliveryAndItsExecution(t *testing.T) {
	clock := testkit.NewClock()
	ctx := t.Context()
	db, err := sql.Open("sqlite", dsn(filepath.Join(t.TempDir(), "prior.db"), true))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	s := &Store{write: db, read: db, now: clock.Now}
	t.Cleanup(func() { s.Close() })
	if err := s.migrate(ctx, 84); err != nil {
		t.Fatal(err)
	}
	issue := canonicalIssueFixture("wi_00000000000000000000000001")
	if err := s.Write(ctx, func(tx *Tx) error {
		if err := tx.SaveCanonicalIssue(ctx, issue, 0, issueProvenance()); err != nil {
			return err
		}
		task := TaskRow{TaskID: "task_original", Title: "Original delivery", Project: "default", Lifecycle: protocol.TaskCompleted, ControlMode: protocol.ControlManaged, ControlRevision: 1, SpecVersion: 1, CreatedAt: 1, UpdatedAt: 1}
		if err := tx.InsertTask(ctx, task); err != nil {
			return err
		}
		if err := tx.InsertSpec(ctx, task.TaskID, 1, `{"goal":"Original","delivery":"commit"}`, "", 1); err != nil {
			return err
		}
		if err := tx.InsertAction(ctx, ActionRow{ActionID: "act_original", TaskID: task.TaskID, Kind: protocol.ActionImplement, EffectKey: "original", TriggerJSON: `{}`, TargetSpecVersion: 1, AuthorizationJSON: `{}`, Generation: 1, Status: protocol.ActionDone, CreatedRequestID: "req_original", CreatedAt: 1}); err != nil {
			return err
		}
		if err := tx.InsertRun(ctx, RunRow{RunID: "run_original", TaskID: task.TaskID, ActionID: "act_original", Generation: 1, Role: protocol.RoleImplement, DesiredState: protocol.DesiredStopped, ObservedState: protocol.RunFinished, CreatedAt: 1}); err != nil {
			return err
		}
		if err := tx.InsertArtifact(ctx, "art_original", task.TaskID, "run_original", "commit", `{}`, "digest", 1); err != nil {
			return err
		}
		if err := tx.InsertAcceptance(ctx, "acc_original", task.TaskID, "run_original", 1, "art_original", protocol.VerdictPassed, `[]`, `[]`, 1); err != nil {
			return err
		}
		if _, err := tx.InsertEvent(ctx, EventRow{EventID: "evt_original", Source: "app", ObjectType: "task", ObjectID: task.TaskID, Kind: "task.completed", BodyJSON: `{}`, ActorJSON: `{}`, CreatedAt: 1}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO issue_delivery_duties(issue_id,task_id,event_id,acceptance_id,revision,status,body_json) VALUES(?,?,?,?,7,'deferred','{"reason":"Original explicit deferral"}')`, issue.IssueID, task.TaskID, "evt_original", "acc_original"); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO issue_delivery_executions(task_id,issue_id,source_task_id,duty_revision,candidate_json,upgrade_id) VALUES(?,?,?,7,'{"head_sha":"original"}','upg_original')`, task.TaskID, issue.IssueID, task.TaskID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	before, err := GetIssueDeliveryDuty(ctx, db, issue.IssueID, "task_original")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(ctx, 87); err != nil {
		t.Fatal(err)
	}
	after, err := GetIssueDeliveryDuty(ctx, db, issue.IssueID, "task_original")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) || after.ArtifactID != "" {
		t.Fatal("migration rewrote original acceptance or delivery", before, after)
	}
	var source, candidate, upgrade string
	var revision int64
	if err := db.QueryRowContext(ctx, `SELECT source_task_id,duty_revision,candidate_json,upgrade_id FROM issue_delivery_executions WHERE task_id='task_original'`).Scan(&source, &revision, &candidate, &upgrade); err != nil {
		t.Fatal(err)
	}
	if source != "task_original" || revision != 7 || candidate != `{"head_sha":"original"}` || upgrade != "upg_original" {
		t.Fatal(source, revision, candidate, upgrade)
	}
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("migration broke a historical foreign key")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	policy, err := TaskAcceptancePolicy(ctx, db, "task_original")
	if err != nil || policy != nil {
		t.Fatal("migration invented an old task waiver", policy, err)
	}
}
