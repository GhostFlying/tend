package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/testkit"
)

func TestCoordinationQueryIndexesUpgradeHistory(t *testing.T) {
	ctx := t.Context()
	clock := testkit.NewClock()
	db, err := sql.Open("sqlite", dsn(filepath.Join(t.TempDir(), "ledger.db"), true))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	s := &Store{write: db, read: db, now: clock.Now}
	if err := s.migrate(ctx, 79); err != nil {
		t.Fatal(err)
	}
	i := canonicalIssueFixture("wi_00000000000000000000000001")
	const snapshot = `{"original":"private execution snapshot"}`
	if err := s.Write(ctx, func(tx *Tx) error {
		if err := tx.SaveCanonicalIssue(ctx, i, 0, issueProvenance()); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO runner_coordination_executions(execution_id,conversation_id,issue_id,assignment_epoch,role,configuration_revision,agent_ref,private_snapshot,created_at) VALUES('historical','conversation',?,1,'issue_manager',0,'executor',?,1)`, i.IssueID, snapshot)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(ctx, 80); err != nil {
		t.Fatal(err)
	}
	if err := s.Read(ctx, func(tx *ReadTx) error {
		var saved string
		if err := tx.Q().QueryRowContext(ctx, `SELECT private_snapshot FROM runner_coordination_executions WHERE execution_id='historical'`).Scan(&saved); err != nil {
			return err
		}
		if saved != snapshot {
			return fmt.Errorf("migration changed execution history: %q", saved)
		}
		for _, check := range []struct{ query, index string }{
			{`SELECT execution_id FROM runner_coordination_executions WHERE issue_id='issue' AND assignment_epoch<2`, "runner_coordination_executions_issue"},
			{`SELECT request_id FROM logical_turn_executions WHERE logical_turn_id='original'`, "logical_turn_executions_logical"},
		} {
			rows, err := tx.Q().QueryContext(ctx, "EXPLAIN QUERY PLAN "+check.query)
			if err != nil {
				return err
			}
			found := false
			for rows.Next() {
				var id, parent, unused int
				var detail string
				if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
					_ = rows.Close()
					return err
				}
				found = found || strings.Contains(detail, "SEARCH ") && strings.Contains(detail, check.index)
			}
			readErr := rows.Err()
			_ = rows.Close()
			if readErr != nil {
				return readErr
			}
			if !found {
				return fmt.Errorf("historical lookup stopped using %s", check.index)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
