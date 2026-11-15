package views

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type issueExecutionQueries struct {
	store.Querier
	builds int
}

func (q *issueExecutionQueries) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	// This global candidate scan is paid once per executionFacts construction.
	if strings.Contains(query, "i.owner_selection_json<>''") {
		q.builds++
	}
	return q.Querier.QueryContext(ctx, query, args...)
}

func TestIssueDetailSharesExecutionFactsWithoutRetainingThem(t *testing.T) {
	ctx := t.Context()
	db := newUsageResolverStore(t)
	id := "wi_00000000000000000000000001"
	usageResolverWrite(t, db, func(tx *store.Tx) error {
		issue := protocol.Issue{IssueControl: protocol.IssueControl{IssueID: id, State: protocol.IssueActive, WorkStatus: protocol.IssueInProgress, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: "fixture-owner"}, AssignmentEpoch: 1, AssignmentState: "active"}, Revision: 1, Plan: protocol.IssuePlan{Title: "Shared execution snapshot", Project: "default", Intent: "Keep task and attention consistent", Stages: []protocol.IssueStage{}}, DependsOn: []string{}, CreatedAt: 1, UpdatedAt: 1}
		if err := tx.SaveCanonicalIssue(ctx, issue, 0, store.IssueRevisionProvenance{Actor: protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "fixture-owner", Client: "fixture"}, RequestID: "req_fixture", Reason: "projection fixture"}); err != nil {
			return err
		}
		if err := tx.InsertTask(ctx, store.TaskRow{TaskID: "task-shared", Title: "Execution", Lifecycle: protocol.TaskCompleted, ControlMode: protocol.ControlManaged, ControlRevision: 1, SpecVersion: 1, Rating: protocol.RatingP2, BlockReason: protocol.BlockNone, CreatedAt: 1, UpdatedAt: 1}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO task_specs(task_id,spec_version,body_json,created_at) VALUES('task-shared',1,'{}',1)`); err != nil {
			return err
		}
		if err := tx.InsertTaskIssueOwner(ctx, "task-shared", id); err != nil {
			return err
		}
		return tx.SaveAttention(ctx, protocol.Attention{AttentionID: "attn_shared", IssueID: id, TaskID: "task-shared", Kind: "action", Title: "Continue settlement", Revision: 1, Status: "open", RaisedBy: protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "fixture-owner", Client: "fixture"}, CreatedAt: 1, UpdatedAt: 1}, 0, "req_fixture")
	})
	read := func(want protocol.ExecutionState) {
		t.Helper()
		if err := db.Read(ctx, func(tx *store.ReadTx) error {
			q := &issueExecutionQueries{Querier: tx.Q()}
			out, err := CanonicalIssue(ctx, q, id, 0)
			if err != nil {
				return err
			}
			if q.builds != 1 {
				t.Fatalf("detail rebuilt global candidates %d times", q.builds)
			}
			if len(out.Tasks) != 1 || out.Tasks[0].Execution.State != want || len(out.Attentions.Items) != 1 {
				t.Fatalf("detail execution = %+v", out)
			}
			attention := out.Attentions.Items[0].TaskContext.Execution
			if !reflect.DeepEqual(attention, out.Tasks[0].Execution) {
				t.Fatalf("attention and task disagree: %+v / %+v", attention, out.Tasks[0].Execution)
			}
			standalone, err := Issue(ctx, tx.Q(), id)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(standalone.Tasks[0].Execution, out.Tasks[0].Execution) {
				t.Fatal("standalone task execution changed")
			}
			history, err := CanonicalIssue(ctx, q, id, 1)
			if err != nil {
				return err
			}
			if q.builds != 1 || !history.Historical || len(history.Tasks) != 0 || history.Attentions != nil || history.Children != nil {
				t.Fatalf("history loaded current execution: %+v (builds %d)", history, q.builds)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	read(protocol.ExecutionCompleted)
	// A new read after a write must observe the new physical uncertainty, even
	// though the task's logical lifecycle is still completed.
	usageResolverWrite(t, db, func(tx *store.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO plan_actions(action_id,task_id,kind,effect_key,trigger_json,target_spec_version,authorization_json,generation,status,created_request_id,created_at,updated_at) VALUES('action-shared','task-shared','implement','effect-shared','{}',1,'{}',1,'done','req_fixture',1,1)`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO runs(run_id,task_id,action_id,generation,role,host_id,desired_state,observed_state,created_at,updated_at) VALUES('run-shared','task-shared','action-shared',1,'implement','fixture-runner','stopped','unknown',1,1)`)
		return err
	})
	read(protocol.ExecutionUnknown)
}
