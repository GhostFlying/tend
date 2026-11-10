package store

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestResponsibilityAdoptionMigrationPreservesFrozenContracts(t *testing.T) {
	s := observationMigrationStore(t)
	if err := s.migrate(t.Context(), 107); err != nil {
		t.Fatal(err)
	}
	issue := canonicalIssueFixture("wi_00000000000000000000000001")
	if err := s.Write(t.Context(), func(tx *Tx) error {
		if err := tx.SaveCanonicalIssue(t.Context(), issue, 0, issueProvenance()); err != nil {
			return err
		}
		for _, entry := range []struct{ task, table string }{{"legacy", "task_owner_contracts"}, {"creator", "creator_task_owner_contracts"}} {
			if err := tx.InsertTask(t.Context(), TaskRow{TaskID: entry.task, Title: "Preserved frozen contract", Project: "default", Lifecycle: protocol.TaskRunning, ControlMode: protocol.ControlManaged, ControlRevision: 3, SpecVersion: 1}); err != nil {
				return err
			}
			fields := `task_id,issue_id,assignment_epoch,agent_ref,agent_revision,runtime_id,runtime_revision,model,effort,created_at,execution_pin_json`
			values := `?, ?, 2,'environment/agent',7,'native',3,'model','high',1,'{"purpose":"original"}'`
			if entry.task == "legacy" {
				fields += `,review_issue_id,review_plan_json`
				values += `,?,'{"intent":"original review"}'`
			}
			args := []any{entry.task, issue.IssueID}
			if entry.task == "legacy" {
				args = append(args, issue.IssueID)
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO `+entry.table+` (`+fields+`) VALUES (`+values+`)`, args...); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	read := func() []TaskOwnerContract {
		t.Helper()
		var result []TaskOwnerContract
		for _, id := range []string{"legacy", "creator"} {
			var c TaskOwnerContract
			// Full contract decoding uses the preserved production view before/after.
			if err := s.Read(t.Context(), func(tx *ReadTx) error {
				// The migration fixture deliberately stops before admission fields exist.
				// Read the exact historical production view without querying later columns.
				var raw string
				err := tx.Q().QueryRowContext(t.Context(), `SELECT task_id,issue_id,assignment_epoch,agent_ref,agent_revision,runtime_id,runtime_revision,model,effort,review_issue_id,review_plan_json,COALESCE(execution_pin_json,'') FROM task_execution_contracts WHERE task_id=?`, id).Scan(&c.TaskID, &c.IssueID, &c.AssignmentEpoch, &c.AgentRef, &c.AgentRevision, &c.RuntimeID, &c.RuntimeRevision, &c.Model, &c.Effort, &c.ReviewIssueID, &c.ReviewPlanJSON, &raw)
				if err != nil {
					return err
				}
				if raw != "" {
					c.Execution = new(protocol.RunExecutionPin)
					return json.Unmarshal([]byte(raw), c.Execution)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			result = append(result, c)
		}
		return result
	}
	before := read()
	if err := s.migrate(t.Context(), 108); err != nil {
		t.Fatal(err)
	}
	if got := read(); !reflect.DeepEqual(got, before) {
		t.Fatal("migration changed existing execution contracts", before, got)
	}
	if err := s.Read(t.Context(), func(tx *ReadTx) error {
		var n int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM task_execution_responsibility_adoptions`).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("migration invented an adoption", n)
		}
		for _, id := range []string{"legacy", "creator"} {
			var epoch int64
			if err := tx.Q().QueryRowContext(t.Context(), `SELECT assignment_epoch FROM frozen_task_execution_contracts WHERE task_id=?`, id).Scan(&epoch); err != nil {
				return err
			}
			if epoch != 2 {
				t.Fatal("frozen epoch changed", epoch)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
