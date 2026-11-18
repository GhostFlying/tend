package views

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestInitialOwnerHandoffBatchKeepsLeaseReceiptAndEventBarriers(t *testing.T) {
	ctx := t.Context()
	db := newUsageResolverStore(t)
	issue, other := "wi_00000000000000000000000001", "wi_00000000000000000000000002"
	targets := []protocol.Issue{}
	for _, id := range []string{issue, other} {
		targets = append(targets, protocol.Issue{IssueControl: protocol.IssueControl{IssueID: id, State: protocol.IssueActive, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: "fixture-owner"}, AssignmentEpoch: 1, AssignmentState: "active"}, Revision: 1, Plan: protocol.IssuePlan{Title: "Handoff fixture", Project: "default", Intent: "Retain physical and receipt barriers", Stages: []protocol.IssueStage{}}, DependsOn: []string{}, CreatedAt: 1, UpdatedAt: 1})
	}
	usageResolverWrite(t, db, func(tx *store.Tx) error {
		for _, target := range targets {
			if err := tx.SaveCanonicalIssue(ctx, target, 0, store.IssueRevisionProvenance{Actor: protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "fixture-owner", Client: "fixture"}, RequestID: "req_fixture", Reason: "query fixture"}); err != nil {
				return err
			}
		}
		if err := tx.UpsertHost(ctx, store.HostRow{HostID: "fixture-host", OS: "linux", Arch: "amd64", Slots: 1}, 1); err != nil {
			return err
		}
		for _, v := range []struct {
			id, issue, observed, lease string
			epoch, events              int
		}{
			{"old-active", issue, "unknown", "", 1, 0},
			{"old-lease", issue, "finished", "lapsed", 1, 0},
			{"old-settled", issue, "finished", "released", 1, 0},
			{"old-events", issue, "finished", "", 1, 2},
			{"old-applied", issue, "finished", "", 1, 1},
			{"new-epoch", issue, "unknown", "", 2, 1},
			{"other-active", other, "unknown", "", 1, 0},
		} {
			if err := tx.InsertTask(ctx, store.TaskRow{TaskID: v.id, Kind: protocol.TaskKindIssueManager, Title: v.id, Lifecycle: protocol.TaskCompleted, ControlMode: protocol.ControlManaged, ControlRevision: 1, SpecVersion: 1, Rating: protocol.RatingP2, BlockReason: protocol.BlockNone, CreatedAt: 1, UpdatedAt: 1}); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO issue_manager_tasks(task_id,issue_id,category_id,expected_epoch,assignment_epoch,state,request_id,created_at,updated_at) VALUES(?,?,'fixture-category',?,?,'completed','req_fixture',1,1)`, v.id, v.issue, v.epoch, v.epoch); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO plan_actions(action_id,task_id,kind,effect_key,trigger_json,target_spec_version,authorization_json,generation,status,created_request_id,created_at,updated_at) VALUES(?,?,'coordinate',?,'{}',1,'{}',1,'done','req_fixture',1,1)`, v.id, v.id, v.id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO runs(run_id,task_id,action_id,generation,role,host_id,desired_state,observed_state,created_at,updated_at) VALUES(?,?,?,1,'issue_manager','fixture-host','stopped',?,1,1)`, v.id, v.id, v.id, v.observed); err != nil {
				return err
			}
			if v.lease != "" {
				if _, err := tx.Exec(ctx, `INSERT INTO leases(lease_id,run_id,host_id,expires_at,status,created_at,updated_at) VALUES(?,?,'fixture-host',1,?,1,1)`, v.id, v.id, v.lease); err != nil {
					return err
				}
			}
			for n := 1; n <= v.events; n++ {
				applied := 0
				if v.id == "old-applied" {
					applied = 1
				}
				if _, err := tx.Exec(ctx, `INSERT INTO manager_native_events(run_id,seq,body_json,applied) VALUES(?,?,'{}',?)`, v.id, n, applied); err != nil {
					return err
				}
			}
		}
		if _, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: "source-receipt", Author: "system", Body: "Retained native receipt", CreatedAt: 1}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO runner_coordination_executions(execution_id,conversation_id,issue_id,assignment_epoch,role,configuration_revision,agent_ref,private_snapshot,created_at) VALUES('native-receipt','fixture',?,1,'issue_manager',0,'fixture-agent','{}',1)`, issue); err != nil {
			return err
		}
		if err := tx.PutMainTurn(ctx, store.MainTurn{RequestID: "native-receipt", MessageID: "source-receipt", ThreadID: "native", TurnID: "turn-receipt", Status: store.TurnCompleted}, 1); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO native_turn_receipts_pending(request_id) VALUES('native-receipt')`)
		return err
	})
	if err := db.Read(ctx, func(tx *store.ReadTx) error {
		queries := &usageResolverQueries{Querier: tx.Q(), batches: map[string]int{}, singles: map[string]int{}}
		batch, err := initialOwnerHandoffBlockers(ctx, queries, targets)
		if err != nil {
			return err
		}
		calls := 0
		for _, n := range queries.batches {
			calls += n
		}
		if calls != 1 {
			t.Fatalf("candidate barriers prepared %d queries for two Issues", calls)
		}
		if _, err := initialOwnerHandoffBlockers(ctx, queries, nil); err != nil {
			return err
		}
		after := 0
		for _, n := range queries.batches {
			after += n
		}
		if after != calls {
			t.Fatal("empty candidate scan queried barriers")
		}
		for _, target := range targets {
			single, err := ManagerHandoffBlockers(ctx, tx.Q(), target.IssueID, target.AssignmentEpoch+1)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(batch[target.IssueID], single) {
				return fmt.Errorf("batch/single mismatch for %s: %+v / %+v", target.IssueID, batch[target.IssueID], single)
			}
		}
		want := []protocol.IssueBlocker{{Code: "old_manager_execution", ObjectID: "native-receipt"}, {Code: "old_manager_run", ObjectID: "old-active"}, {Code: "old_manager_run", ObjectID: "old-lease"}, {Code: "old_manager_events", ObjectID: "old-events"}}
		if !reflect.DeepEqual(batch[issue], want) || !reflect.DeepEqual(batch[other], []protocol.IssueBlocker{{Code: "old_manager_run", ObjectID: "other-active"}}) {
			t.Fatalf("lost or invented receipt/lease/event/epoch barriers: %+v", batch)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	for seq := 1; seq <= 2; seq++ {
		usageResolverWrite(t, db, func(tx *store.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE manager_native_events SET applied=1 WHERE run_id='old-events' AND seq=?`, seq)
			return err
		})
		if err := db.Read(ctx, func(tx *store.ReadTx) error {
			blockers, err := ManagerHandoffBlockers(ctx, tx.Q(), issue, 2)
			if err != nil {
				return err
			}
			pending := slices.Contains(blockers, protocol.IssueBlocker{Code: "old_manager_events", ObjectID: "old-events"})
			if pending != (seq < 2) {
				t.Fatalf("old Run event barrier after applying sequence %d: %v", seq, blockers)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestManagerHandoffLooksUpOriginalRunsUnappliedEvents(t *testing.T) {
	ctx := t.Context()
	db := newUsageResolverStore(t)
	if err := db.Read(ctx, func(tx *store.ReadTx) error {
		rows, err := tx.Q().QueryContext(ctx, "EXPLAIN QUERY PLAN "+managerHandoffBatchSQL, `{"new-issue":2}`)
		if err != nil {
			return err
		}
		defer rows.Close()
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				return err
			}
			plan = append(plan, detail)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		// A new Issue must not walk unrelated manager tasks or native event history.
		for _, lookup := range []string{"issue_manager_tasks_handoff (issue_id=?", "manager_native_events_unapplied (run_id=?)"} {
			if !slices.ContainsFunc(plan, func(detail string) bool {
				return strings.HasPrefix(detail, "SEARCH ") && strings.Contains(detail, lookup)
			}) {
				t.Fatalf("missing scoped lookup %q in:\n%s", lookup, strings.Join(plan, "\n"))
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
