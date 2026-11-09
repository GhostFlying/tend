package store

import (
	"context"
	"encoding/json"

	"github.com/GhostFlying/tend/internal/protocol"
)

// ResponsibilityAdoption is an explicit continuation of one stopped execution,
// not a rewrite of its publishing contract or permission for a live old Run.
type ResponsibilityAdoption struct {
	RequestDomain, RequestID, TaskID, DecisionID, SourceRunID, IssueID string
	AgentRef, Reason                                                   string
	SpecVersion, TaskControlRevision, ContinuationRevision             int64
	FromAssignmentEpoch, AssignmentEpoch, CreatedAt                    int64
	Execution                                                          protocol.RunExecutionPin
}

func (tx *Tx) InsertResponsibilityAdoption(ctx context.Context, a ResponsibilityAdoption) error {
	raw, err := json.Marshal(a.Execution)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO task_execution_responsibility_adoptions(request_domain,request_id,task_id,spec_version,task_control_revision,decision_id,continuation_revision,source_run_id,issue_id,from_assignment_epoch,assignment_epoch,agent_ref,execution_pin_json,reason,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, a.RequestDomain, a.RequestID, a.TaskID, a.SpecVersion, a.TaskControlRevision, a.DecisionID, a.ContinuationRevision, a.SourceRunID, a.IssueID, a.FromAssignmentEpoch, a.AssignmentEpoch, a.AgentRef, string(raw), a.Reason, a.CreatedAt)
	return err
}

// AdoptedQuestionResponsibilityCurrent applies only to the exact stopped
// predecessor named in a committed adoption, never to other old workers.
func AdoptedQuestionResponsibilityCurrent(ctx context.Context, q Querier, run string) (bool, error) {
	var valid bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_execution_responsibility_adoptions a
 JOIN run_question_handoffs h ON h.run_id=a.source_run_id AND h.decision_id=a.decision_id
 JOIN runs r ON r.run_id=h.run_id AND r.task_id=a.task_id
 JOIN tasks t ON t.task_id=a.task_id JOIN issue_controls i ON i.issue_id=a.issue_id
 WHERE a.source_run_id=? AND h.stopped=1 AND r.observed_state='finished'
 AND a.spec_version=t.spec_version AND i.assignment_epoch=a.assignment_epoch
 AND i.assignee_ref=a.agent_ref AND i.assignment_state='active' AND i.state IN ('active','paused'))`, run).Scan(&valid)
	return valid, err
}
