package store

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestLiveDeliveryHistoryMigrationPreservesResultsAndAbsentProvenance(t *testing.T) {
	ctx := t.Context()
	db, err := sql.Open("sqlite", dsn(filepath.Join(t.TempDir(), "schema124.db"), true))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	s := &Store{write: db, read: db, now: testkit.NewClock().Now}
	t.Cleanup(func() { s.Close() })
	if err := s.migrate(ctx, 124); err != nil {
		t.Fatal(err)
	}
	i := canonicalIssueFixture("wi_00000000000000000000000001")
	if err := s.Write(ctx, func(tx *Tx) error {
		if err := tx.SaveCanonicalIssue(ctx, i, 0, issueProvenance()); err != nil {
			return err
		}
		for _, id := range []string{"task_result", "task_not_started"} {
			if err := tx.InsertTask(ctx, TaskRow{TaskID: id, Title: "Actual delivery responsibility", Lifecycle: protocol.TaskQueued, ControlMode: protocol.ControlManaged, ControlRevision: 1, SpecVersion: 1}); err != nil {
				return err
			}
		}
		if err := tx.InsertAction(ctx, ActionRow{ActionID: "act_original", TaskID: "task_result", Kind: protocol.ActionImplement, EffectKey: "original", TriggerJSON: `{}`, TargetSpecVersion: 1, AuthorizationJSON: `{}`, Generation: 1, Status: protocol.ActionDone, CreatedRequestID: "req_original"}); err != nil {
			return err
		}
		if err := tx.InsertRun(ctx, RunRow{RunID: "run_original", TaskID: "task_result", ActionID: "act_original", Generation: 1, Role: protocol.RoleImplement, DesiredState: protocol.DesiredStopped, ObservedState: protocol.RunFinished}); err != nil {
			return err
		}
		if err := tx.InsertArtifact(ctx, "art_original", "task_result", "run_original", "result", `{"report":{"body":"Original result"}}`, "original-digest", 1); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO issue_delivery_execution_history VALUES('task_result',1,6,'run_original',?,'task_result','{"head_sha":"original"}','{"status":"complete","reason":"Original accepted result"}','art_original','upgrade_original','req_original',123)`, i.IssueID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	read := func() []any {
		t.Helper()
		values := make([]any, 12)
		pointers := make([]any, len(values))
		for j := range values {
			pointers[j] = &values[j]
		}
		if err := db.QueryRowContext(ctx, `SELECT * FROM issue_delivery_execution_history WHERE task_id='task_result'`).Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		return values
	}
	before := read()
	if err := s.migrate(ctx, 125); err != nil {
		t.Fatal(err)
	}
	if after := read(); !reflect.DeepEqual(after, before) {
		t.Fatal("migration changed original result, authority or source bytes", before, after)
	}
	if err := s.Write(ctx, func(tx *Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO issue_delivery_execution_history VALUES('task_not_started',0,2,NULL,?,'task_not_started','{}','{"status":"executing"}',NULL,'','req_supersede',124)`, i.IssueID)
		return err
	}); err != nil {
		t.Fatal("unexecuted binding required a synthetic Run or artifact", err)
	}
	var runs, artifacts, missing int
	if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM runs),(SELECT count(*) FROM artifacts),(SELECT count(*) FROM issue_delivery_execution_history WHERE run_id IS NULL AND artifact_id IS NULL)`).Scan(&runs, &artifacts, &missing); err != nil || runs != 1 || artifacts != 1 || missing != 1 {
		t.Fatal("migration manufactured provenance", runs, artifacts, missing, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE issue_delivery_execution_history SET artifact_id='art_missing' WHERE task_id='task_not_started'`); err == nil {
		t.Fatal("migration dropped the actual artifact foreign key")
	}
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() || rows.Err() != nil {
		t.Fatal("migration left an invalid historical reference", rows.Err())
	}
}
