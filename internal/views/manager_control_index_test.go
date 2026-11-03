package views

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

// The production relation stays unchanged; the index must preserve its
// cross-domain EXISTS semantics as well as local/runner history and ordering.
func TestManagerControlIndexPreservesBlockersAndRequestHistory(t *testing.T) {
	ctx := t.Context()
	clock := testkit.NewClock()
	path := filepath.Join(t.TempDir(), "ledger.db")
	db, err := store.Open(ctx, path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if db != nil {
			_ = db.Close()
		}
	})
	issue, other := "wi_00000000000000000000000001", "wi_00000000000000000000000002"
	if err := db.Write(ctx, func(tx *store.Tx) error {
		for _, q := range []string{`DROP INDEX request_records_identity_status`} {
			if _, err := tx.Exec(ctx, q); err != nil {
				return err
			}
		}
		for _, id := range []string{issue, other} {
			i := protocol.Issue{IssueControl: protocol.IssueControl{IssueID: id, State: protocol.IssueActive, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: "fixture-owner"}, AssignmentEpoch: 1, AssignmentState: "active"}, Revision: 1, Plan: protocol.IssuePlan{Title: "Query fixture", Project: "default", Intent: "Preserve request facts", Stages: []protocol.IssueStage{}}, DependsOn: []string{}, CreatedAt: 1, UpdatedAt: 1}
			if err := tx.SaveCanonicalIssue(ctx, i, 0, store.IssueRevisionProvenance{Actor: protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "fixture-owner", Client: "fixture"}, RequestID: "req_fixture", Reason: "query fixture"}); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO hosted_configuration_versions(revision,environment_id,private_hash,private_json,public_json,main_agent_id,request_id,accepted_at) VALUES(1,'fixture','hash','{}','{}','fixture','req_config',1)`); err != nil {
			return err
		}
		for _, v := range []struct {
			id, issue, source string
			epoch             int
		}{{"local", issue, "local", 1}, {"runner", issue, "runner", 1}, {"other", other, "runner", 1}, {"next", issue, "runner", 2}} {
			table, revision := "runner_coordination_executions", 0
			if v.source == "local" {
				table, revision = "local_hosted_executions", 1
			}
			if _, err := tx.Exec(ctx, `INSERT INTO `+table+`(execution_id,conversation_id,issue_id,assignment_epoch,role,configuration_revision,agent_ref,private_snapshot,created_at) VALUES(?,'fixture',?,?,'issue_manager',?,'fixture','{}',1)`, v.id, v.issue, v.epoch, revision); err != nil {
				return err
			}
		}
		for _, v := range []struct {
			execution, request, source string
			seq                        int
		}{{"local", "accepted", "local", 1}, {"local", "accepted", "local", 2}, {"runner", "unknown", "runner", 1}, {"runner", "cross-domain", "runner", 2}, {"runner", "succeeded", "runner", 3}, {"runner", "missing", "runner", 4}, {"other", "other", "runner", 1}, {"next", "next", "runner", 1}} {
			table := "runner_coordination_effects"
			if v.source == "local" {
				table = "local_hosted_coordination_effects"
			}
			if _, err := tx.Exec(ctx, `INSERT INTO `+table+`(execution_id,effect_seq,request_id) VALUES(?,?,?)`, v.execution, v.seq, v.request); err != nil {
				return err
			}
		}
		for _, v := range []struct{ domain, id, status string }{{"task", "accepted", "accepted"}, {"task", "unknown", "unknown"}, {"task", "cross-domain", "succeeded"}, {"other-domain", "cross-domain", "unknown"}, {"task", "succeeded", "succeeded"}, {"task", "other", "unknown"}, {"task", "next", "unknown"}} {
			if _, err := tx.Exec(ctx, `INSERT INTO request_records(domain,request_id,operation,payload_hash,principal_json,trace_id,status,created_at,updated_at) VALUES(?,?,'fixture','fixture','{}','fixture',?,1,1)`, v.domain, v.id, v.status); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, facts := managerControlIndexState(t, ctx, db, issue)
	if !reflect.DeepEqual(before, []protocol.IssueBlocker{{Code: "old_manager_control", ObjectID: "accepted"}, {Code: "old_manager_control", ObjectID: "cross-domain"}, {Code: "old_manager_control", ObjectID: "unknown"}}) {
		t.Fatal("unexpected predecessor blockers", before)
	}
	migration, err := os.ReadFile(filepath.Join("..", "store", "migrations", "0084_request_record_identity_index.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Write(ctx, func(tx *store.Tx) error { _, err := tx.Exec(ctx, string(migration)); return err }); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(ctx, path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	after, afterFacts := managerControlIndexState(t, ctx, db, issue)
	if !reflect.DeepEqual(before, after) || facts != afterFacts {
		t.Fatal("index migration changed blockers or request facts", before, after)
	}
	if err := db.Read(ctx, func(tx *store.ReadTx) error {
		var applied int
		if err := tx.Q().QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version=84`).Scan(&applied); err != nil {
			return err
		}
		if applied != 1 {
			t.Fatal("migration84 not recorded once", applied)
		}
		rows, err := tx.Q().QueryContext(ctx, `EXPLAIN QUERY PLAN SELECT DISTINCT e.request_id FROM hosted_coordination_effects e JOIN hosted_executions h ON h.execution_id=e.execution_id WHERE h.issue_id=? AND h.assignment_epoch<? AND EXISTS(SELECT 1 FROM request_records r WHERE r.request_id=e.request_id AND r.status IN ('accepted','unknown')) ORDER BY e.request_id`, issue, 2)
		if err != nil {
			return err
		}
		defer rows.Close()
		var plan []string
		for rows.Next() {
			var a, b, c int
			var detail string
			if err := rows.Scan(&a, &b, &c, &detail); err != nil {
				return err
			}
			plan = append(plan, detail)
		}
		text := strings.Join(plan, "\n")
		if !strings.Contains(text, "COVERING INDEX request_records_identity_status") || strings.Contains(text, "SCAN r EXISTS") {
			t.Fatal("unbounded request lookup", text)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
}

func managerControlIndexState(t *testing.T, ctx context.Context, db *store.Store, issue string) ([]protocol.IssueBlocker, string) {
	t.Helper()
	var blockers []protocol.IssueBlocker
	var facts string
	if err := db.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		blockers, err = ManagerHandoffBlockers(ctx, tx.Q(), issue, 2)
		if err != nil {
			return err
		}
		return tx.Q().QueryRowContext(ctx, `SELECT json_group_array(json_object('domain',domain,'request_id',request_id,'status',status,'payload_hash',payload_hash,'result_json',result_json,'error_code',error_code,'updated_at',updated_at)) FROM (SELECT * FROM request_records ORDER BY domain,request_id)`).Scan(&facts)
	}); err != nil {
		t.Fatal(err)
	}
	return blockers, facts
}
