package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/testkit"
)

func TestWorkerTextMigrationRetainsOriginalCursorAndBusinessEvents(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "tend.db")
	clock := testkit.NewClock()
	db, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &Store{write: db, read: db, now: clock.Now}
	if err := s.migrate(ctx, 139); err != nil {
		t.Fatal(err)
	}
	for _, conversation := range []string{"main", "issue:worker"} {
		issue, task, action, run := "wi-"+conversation, "task-"+conversation, "act-"+conversation, "run-"+conversation
		mustExec(t, s, `INSERT INTO work_items(work_item_id,project,revision,status,body_json,created_at,updated_at) VALUES(?,'default',1,'open','{}',0,0)`, issue)
		mustExec(t, s, `INSERT INTO issue_controls(issue_id,state,assignee_kind,assignee_ref,assignment_epoch,assignment_state,updated_at) VALUES(?,'active','agent','agent',1,'active',0)`, issue)
		insertTask(t, s, task)
		if err := insertAction(t, s, action, task, action); err != nil {
			t.Fatal(err)
		}
		mustExec(t, s, `INSERT INTO issue_manager_tasks(task_id,issue_id,category_id,expected_epoch,conversation_id,request_id,created_at,updated_at) VALUES(?,?,'manager',1,?,'original',0,0)`, task, issue, conversation)
		mustExec(t, s, `INSERT INTO runs(run_id,task_id,action_id,generation,role,desired_state,observed_state,created_at,updated_at) VALUES(?,?,?,1,'issue_manager','run','running',0,0)`, run, task, action)
	}
	const fragment = `{"thread_id":"thread","native":{"Kind":"agent_message_delta","TurnID":"turn","ItemID":"original","Text":"片"}}`
	business := []string{
		`{"thread_id":"thread","native":{"Kind":"item_completed","TurnID":"turn","ItemID":"original","ItemType":"agentMessage","Text":"全文"}}`,
		`{"thread_id":"thread","call":{"CallID":"unsettled"},"native":{"Kind":"agent_message_delta","TurnID":"turn","ItemID":"original","Text":"片"}}`,
		`{"thread_id":"thread","native":{"Kind":"agent_message_delta","TurnID":"turn","ItemID":"original","Text":"片","RequestUsage":{}}}`,
	}
	for i, body := range business {
		mustExec(t, s, `INSERT INTO manager_native_events(run_id,seq,body_json) VALUES('run-issue:worker',?,?)`, 44+i, body)
	}
	for seq := 47; seq <= 60; seq++ {
		mustExec(t, s, `INSERT INTO manager_native_events(run_id,seq,body_json) VALUES('run-issue:worker',?,?)`, seq, fragment)
	}
	mustExec(t, s, `INSERT INTO manager_native_events(run_id,seq,body_json) VALUES('run-main',1,?)`, fragment)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(ctx, path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	if err := upgraded.Read(ctx, func(tx *ReadTx) error {
		var texts, pending, last int
		if err := tx.Q().QueryRowContext(ctx, `SELECT SUM(applied=1 AND body_json=?),SUM(applied=0),MAX(seq) FROM manager_native_events WHERE run_id='run-issue:worker'`, fragment).Scan(&texts, &pending, &last); err != nil {
			return err
		}
		if texts != 14 || pending != len(business) || last != 60 {
			return fmt.Errorf("retained texts=%d pending business events=%d sequence=%d", texts, pending, last)
		}
		var main int
		if err := tx.Q().QueryRowContext(ctx, `SELECT applied FROM manager_native_events WHERE run_id='run-main' AND body_json=?`, fragment).Scan(&main); err != nil {
			return err
		}
		if main != 0 {
			return fmt.Errorf("Main still requires the original streaming ACK")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
