package tools

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestCanonicalNoStageArtifactScopeIsBoundedEvidence(t *testing.T) {
	e := newEnv(t)
	// Explicit store-fact fixture: canonical task creation/dispatch is not enabled.
	ids := []string{"wi_00000000000000000000000001", "wi_00000000000000000000000002", "wi_00000000000000000000000003"}
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		for _, id := range ids {
			issue := protocol.Issue{IssueControl: protocol.IssueControl{IssueID: id, State: protocol.IssueActive, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: "owner"}, AssignmentEpoch: 1, AssignmentState: "active"}, Revision: 1, Plan: protocol.WorkItemPlan{Title: "Goal", Project: "default", Intent: "Result", Stages: []protocol.WorkItemStage{}}, DependsOn: []string{}, CreatedAt: 1, UpdatedAt: 1}
			if err := tx.SaveCanonicalIssue(t.Context(), issue, 0, store.IssueRevisionProvenance{Actor: protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "fixture-client"}, RequestID: "req_fixture", Reason: "read fixture"}); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO agent_conversations(conversation_id,work_item_id,created_at)VALUES(?,?,1)`, "conv:"+id, id); err != nil {
				return err
			}
		}
		if err := tx.InsertTask(t.Context(), store.TaskRow{TaskID: "task_fixture", Title: "Execution", Project: "default", Lifecycle: protocol.TaskCompleted, ControlMode: protocol.ControlManaged, ControlRevision: 1, SpecVersion: 1, Rating: protocol.RatingP1, BlockReason: protocol.BlockNone}); err != nil {
			return err
		}
		if err := tx.InsertSpec(t.Context(), "task_fixture", 1, `{}`, "", 1); err != nil {
			return err
		}
		if err := tx.InsertTaskIssueOwner(t.Context(), "task_fixture", ids[0]); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO work_item_task_links(work_item_id,stage_id,task_id)VALUES(?,'result','task_fixture')`, ids[1]); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO plan_actions(action_id,task_id,kind,effect_key,trigger_json,target_spec_version,authorization_json,generation,status,created_request_id,created_at,updated_at)VALUES('act_fixture','task_fixture','implement','fixture','{}',1,'{}',1,'completed','req_fixture',1,1)`); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO runs(run_id,task_id,action_id,generation,role,desired_state,observed_state,created_at,updated_at)VALUES('run_fixture','task_fixture','act_fixture',1,'implement','run','finished',1,1)`); err != nil {
			return err
		}
		return tx.InsertArtifact(t.Context(), "art_fixture", "task_fixture", "run_fixture", "report", `{"files":[]}`, "", 1)
	}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(evidenceArgs{ArtifactID: "art_fixture"})
	for i, id := range ids {
		result, err := e.svc.backgroundEvidence(t.Context(), Call{Principal: protocol.Principal{Kind: protocol.PrincipalBackgroundAgent, ConversationID: "conv:" + id}, Arguments: raw})
		if i < 2 {
			if err != nil || result == nil {
				t.Fatalf("primary/explicit reference lost evidence: %v", err)
			}
		} else if protocol.CodeOf(err) != protocol.CodeForbidden {
			t.Fatalf("unrelated evidence access=%v", err)
		}
	}
	raw, _ = json.Marshal(evidenceArgs{ArtifactID: "missing"})
	if _, err := e.svc.backgroundEvidence(t.Context(), Call{Principal: protocol.Principal{ConversationID: "conv:" + ids[0]}, Arguments: raw}); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatalf("missing artifact changed rejection: %v", err)
	}
	if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		responsible, err := store.TaskIssueResponsible(t.Context(), tx.Q(), "task_fixture", ids[1])
		if responsible {
			t.Fatal("readable result became execution authority")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
