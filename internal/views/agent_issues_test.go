package views

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestAgentIssuesFrozenIdentityAndPagination(t *testing.T) {
	ctx := t.Context()
	clock := testkit.NewClock()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "ledger.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	identity := protocol.AgentIdentity{ExecutionEnvironmentID: "runner-one", LocalID: "cedar"}
	ids := []string{"wi_00000000000000000000000001", "wi_00000000000000000000000002", "wi_00000000000000000000000003"}
	if err := db.Write(ctx, func(tx *store.Tx) error {
		for _, id := range ids {
			issue := protocol.Issue{IssueControl: protocol.IssueControl{IssueID: id, State: protocol.IssueActive, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: "owner"}, AssignmentEpoch: 1, AssignmentState: "active"}, Revision: 1, Plan: protocol.IssuePlan{Title: "Frozen " + id[len(id)-1:], Project: "default", Intent: "Preserve execution evidence", Stages: []protocol.IssueStage{}}, DependsOn: []string{}, CreatedAt: 1, UpdatedAt: 1}
			if err := tx.SaveCanonicalIssue(ctx, issue, 0, store.IssueRevisionProvenance{Actor: protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "fixture"}, RequestID: "req_fixture", Reason: "fixture"}); err != nil {
				return err
			}
		}
		for _, v := range []struct {
			id, issue, host, observed string
			revision                  int
			decision                  bool
		}{{"one", ids[0], "runner-one", "running", 1, false}, {"two", ids[0], "runner-one", "unknown", 2, false}, {"history", ids[1], "runner-one", "finished", 1, false}, {"question", ids[2], "runner-one", "finished", 1, true}, {"other-host", ids[1], "runner-two", "running", 1, false}} {
			if _, err := tx.Exec(ctx, `INSERT INTO tasks(task_id,title,lifecycle,control_mode,control_revision,spec_version,priority,block_reason,created_at,updated_at) VALUES(?,'task','running','managed',1,1,0,'none',1,1)`, v.id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO task_specs(task_id,spec_version,body_json,created_at) VALUES(?,1,'{}',1)`, v.id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO task_work_item_owners(task_id,work_item_id,source_spec_version) VALUES(?,?,1)`, v.id, v.issue); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO plan_actions(action_id,task_id,kind,effect_key,trigger_json,target_spec_version,authorization_json,generation,status,created_request_id,created_at,updated_at) VALUES(?,?,'implement',?,'{}',1,'{}',1,'done','req_fixture',1,1)`, v.id, v.id, v.id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO runs(run_id,task_id,action_id,generation,role,host_id,desired_state,observed_state,created_at,updated_at) VALUES(?,?,?,1,'implement',?,'run',?,1,1)`, v.id, v.id, v.id, v.host, v.observed); err != nil {
				return err
			}
			pin := protocol.RunExecutionPin{Identity: protocol.AgentIdentity{ExecutionEnvironmentID: v.host, LocalID: "cedar"}, AgentRevision: uint64(v.revision), InventoryRevision: int64(v.revision), SnapshotID: "frozen-snapshot", Runtime: protocol.AgentRuntimeReference{RuntimeID: "original-worker", Revision: 1}, PublicSHA256: "sha256:" + strings.Repeat("a", 64), Profile: protocol.ExecutionProfile{Model: "original-model", Effort: "high"}, Worker: protocol.WorkerAppServer, BackendID: "git"}
			raw, _ := json.Marshal(pin)
			if _, err := tx.Exec(ctx, `INSERT INTO run_agent_executions(run_id,pin_json) VALUES(?,?)`, v.id, string(raw)); err != nil {
				return err
			}
			if v.decision {
				if _, err := tx.Exec(ctx, `INSERT INTO decisions(decision_id,task_id,question,options_json,scope,version,status,created_at,updated_at) VALUES('decision',?,'question','[]','task',1,'open',1,1)`, v.id); err != nil {
					return err
				}
			}
		}
		// A later mutable link must never invent execution provenance.
		_, err := tx.Exec(ctx, `INSERT INTO work_item_task_links(work_item_id,stage_id,task_id) VALUES(?,'later-link','one')`, ids[1])
		return err
	}); err != nil {
		t.Fatal(err)
	}
	read := func(f AgentIssueFilter) protocol.AgentIssuePage {
		t.Helper()
		var out protocol.AgentIssuePage
		if err := db.Read(ctx, func(tx *store.ReadTx) error { var err error; out, err = AgentIssues(ctx, tx.Q(), f); return err }); err != nil {
			t.Fatal(err)
		}
		return out
	}
	first := read(AgentIssueFilter{Identity: identity, Limit: 1})
	if !first.More || first.Next != ids[0] || len(first.Items) != 1 || len(first.Items[0].Runs) != 2 || !first.Items[0].Current {
		t.Fatalf("first=%+v", first)
	}
	if first.Items[0].Runs[1].Run.Observed != protocol.RunUnknown || first.Items[0].Runs[1].Pin.AgentRevision != 2 {
		t.Fatal("lost unknown or historical revision")
	}
	next := read(AgentIssueFilter{Identity: identity, After: first.Next, Limit: 1})
	if len(next.Items) != 1 || next.Items[0].IssueID != ids[1] || next.Items[0].Current || len(next.Items[0].Runs) != 1 {
		t.Fatalf("history=%+v", next)
	}
	current := read(AgentIssueFilter{Identity: identity, Scope: "current"})
	if len(current.Items) != 2 || !current.Items[1].Runs[0].PendingDecision {
		t.Fatalf("current=%+v", current)
	}
	revision := read(AgentIssueFilter{Identity: identity, AgentRevision: 2})
	if len(revision.Items) != 1 || len(revision.Items[0].Runs) != 1 || revision.Items[0].Runs[0].Pin.Profile.Model != "original-model" {
		t.Fatalf("revision=%+v", revision)
	}
	if got := read(AgentIssueFilter{Identity: identity, Search: "Frozen 2", Scope: "history"}); len(got.Items) != 1 || got.Items[0].IssueID != ids[1] {
		t.Fatalf("search=%+v", got)
	}
	if got := read(AgentIssueFilter{Identity: protocol.AgentIdentity{ExecutionEnvironmentID: "hosted", LocalID: "main"}}); got.Coverage != "recorded_run_pins" || len(got.Items) != 0 {
		t.Fatalf("missing is not invented hosted history: %+v", got)
	}
	if err := db.Write(ctx, func(tx *store.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO run_issue_responsibilities(run_id,issue_id,assignment_epoch,issue_revision,agent_ref,created_at) VALUES('history',?,1,1,'runner-one/cedar',1)`, ids[0]); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE tasks SET lifecycle='completed' WHERE task_id='question'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	authoritative := read(AgentIssueFilter{Identity: identity})
	if len(authoritative.Items) != 2 || len(authoritative.Items[0].Runs) != 3 || authoritative.Items[0].Runs[0].Source != "run_responsibility" {
		t.Fatalf("frozen Run responsibility must outrank task owner: %+v", authoritative)
	}
	if authoritative.Items[1].Current || authoritative.Items[1].Runs[0].PendingDecision {
		t.Fatal("a stale open decision on a terminal task is not current work")
	}

}
