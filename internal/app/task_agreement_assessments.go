package app

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func validateTaskAgreementAssessment(ctx context.Context, q store.Querier, issue protocol.Issue, p canonicalOwnerIssueUpdate) error {
	if p.ExpectedContextRevision == nil {
		return nil
	}
	if len(p.TaskDispositions) == 0 || len(p.TaskDispositions) > 100 {
		return protocol.Errorf(protocol.CodeInvalidRequest, "explicit source assessment requires 1 to 100 task dispositions")
	}
	if issue.AssignmentState != "active" {
		return protocol.Errorf(protocol.CodeInvalidTransition, "assignment handoff must settle before assessing task agreements")
	}
	var revision int64
	if err := q.QueryRowContext(ctx, `SELECT revision FROM work_item_contexts WHERE work_item_id=?`, issue.IssueID).Scan(&revision); err != nil {
		return err
	}
	if revision != *p.ExpectedContextRevision {
		return protocol.Errorf(protocol.CodeStaleRevision, "issue source context changed")
	}
	seen := map[string]bool{}
	for _, disposition := range p.TaskDispositions {
		if seen[disposition.TaskID] || strings.TrimSpace(disposition.Reason) == "" || len(disposition.Reason) > 8000 {
			return protocol.Errorf(protocol.CodeInvalidRequest, "distinct task dispositions with bounded explicit reasons are required")
		}
		seen[disposition.TaskID] = true
		if err := domain.ValidateIssueDispositionAction(disposition.Action, "task_dispositions.action"); err != nil {
			return err
		}
		primary, err := store.GetTaskIssueOwner(ctx, q, disposition.TaskID)
		if err != nil {
			return errNotFound(err, "assessment task does not exist")
		}
		if primary != issue.IssueID {
			return protocol.Errorf(protocol.CodeForbidden, "assess only this Issue's immutable primary tasks")
		}
		task, err := store.GetTask(ctx, q, disposition.TaskID)
		if err != nil {
			return err
		}
		if disposition.ExpectedControlRevision == nil || task.ControlRevision != *disposition.ExpectedControlRevision || task.SpecVersion != disposition.SpecVersion {
			return protocol.Errorf(protocol.CodeStaleRevision, "assessment requires the current task control revision and spec version")
		}
		if task.Lifecycle.Terminal() || task.Kind == protocol.TaskKindIssueManager {
			return protocol.Errorf(protocol.CodeInvalidTransition, "assess a current nonterminal execution task")
		}
		ownerCurrent, err := store.TaskOwnerContractCurrent(ctx, q, disposition.TaskID)
		if err != nil {
			return err
		}
		if !ownerCurrent {
			return protocol.Errorf(protocol.CodeStaleRevision, "task's execution responsibility changed")
		}
		if disposition.Action == protocol.WorkItemTaskUpdated {
			var updated bool
			if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE object_type='task' AND object_id=? AND kind='task.spec_updated' AND json_extract(body_json,'$.spec_version')=?)`, disposition.TaskID, disposition.SpecVersion).Scan(&updated); err != nil {
				return err
			}
			if !updated {
				return protocol.Errorf(protocol.CodeInvalidTransition, "formally update the task spec before marking its agreement updated")
			}
		}
		raw, err := store.GetSpec(ctx, q, disposition.TaskID, disposition.SpecVersion)
		if err != nil {
			return err
		}
		var frozen struct {
			Contexts []protocol.TaskWorkContext `json:"shared_context"`
		}
		if err := json.Unmarshal([]byte(raw), &frozen); err != nil {
			return err
		}
		versions := map[string]int64{issue.IssueID: *p.ExpectedContextRevision}
		for _, reviewed := range disposition.ContextRevisions {
			if _, duplicate := versions[reviewed.WorkItemID]; duplicate || reviewed.Revision < 0 {
				return protocol.Errorf(protocol.CodeInvalidRequest, "supply distinct inherited context revisions separately from this Issue")
			}
			versions[reviewed.WorkItemID] = reviewed.Revision
		}
		for _, binding := range frozen.Contexts {
			var revision int64
			if err := q.QueryRowContext(ctx, `SELECT revision FROM work_item_contexts WHERE work_item_id=?`, binding.WorkItemID).Scan(&revision); err != nil {
				return err
			}
			if reviewed, ok := versions[binding.WorkItemID]; !ok || reviewed != revision {
				return protocol.Errorf(protocol.CodeStaleRevision, "assessment requires the current revision of every inherited context")
			}
		}
		if disposition.Action == protocol.WorkItemTaskStopped && task.ControlMode != protocol.ControlPaused {
			return protocol.Errorf(protocol.CodeInvalidTransition, "pause the task before marking its agreement stopped")
		}
		if disposition.Action == protocol.WorkItemTaskStopped {
			var live bool
			if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE task_id=? AND observed_state NOT IN ('finished','failed'))`, disposition.TaskID).Scan(&live); err != nil {
				return err
			}
			if live {
				return protocol.Errorf(protocol.CodeInvalidTransition, "confirm task runs stopped before marking its agreement stopped")
			}
		}
	}
	return nil
}

// assessTaskAgreements persists a judgement separately from the immutable spec.
// Existing plan-only callers retain their original validation contract.
func assessTaskAgreements(ctx context.Context, x *Exec, tx *store.Tx, issue protocol.Issue, p canonicalOwnerIssueUpdate) ([]Event, error) {
	if p.ExpectedContextRevision == nil {
		return nil, nil
	}
	for _, disposition := range p.TaskDispositions {
		raw, err := store.GetSpec(ctx, tx.Q(), disposition.TaskID, disposition.SpecVersion)
		if err != nil {
			return nil, err
		}
		var frozen struct {
			Contexts []protocol.TaskWorkContext `json:"shared_context"`
		}
		if err := json.Unmarshal([]byte(raw), &frozen); err != nil {
			return nil, err
		}
		for _, binding := range frozen.Contexts {
			contextIssue, err := store.GetCanonicalIssue(ctx, tx.Q(), binding.WorkItemID)
			if err != nil {
				return nil, err
			}
			// Only the current inherited chain is eligible, never a result reference
			// or a source removed by a reparenting operation.
			inherited, err := issueInCurrentAncestry(ctx, tx.Q(), issue.IssueID, binding.WorkItemID)
			if err != nil {
				return nil, err
			}
			if !inherited {
				return nil, protocol.Errorf(protocol.CodeStaleRevision, "task's inherited responsibility changed")
			}
			var pending bool
			if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM message_context_obligations WHERE work_item_id=? AND needs_review=1) OR EXISTS(SELECT 1 FROM work_item_context_facts WHERE superseded=0 AND needs_review=1 AND (work_item_id=? OR scope='global' OR (scope='project' AND project=?)))`, binding.WorkItemID, binding.WorkItemID, contextIssue.Plan.Project).Scan(&pending); err != nil {
				return nil, err
			}
			if pending {
				return nil, protocol.Errorf(protocol.CodeInvalidTransition, "review outstanding sources before assessing the task agreement")
			}
			digest, _, err := store.EffectiveContext(ctx, tx.Q(), binding.WorkItemID)
			if err != nil {
				return nil, err
			}
			if disposition.Action == protocol.WorkItemTaskUpdated && binding.ContextDigest != digest {
				return nil, protocol.Errorf(protocol.CodeInvalidTransition, "formally update the task spec before acknowledging a revised agreement")
			}
			definition, err := store.IssueAgreementDefinitionDigest(contextIssue.Plan)
			if err != nil {
				return nil, err
			}
			sources, err := store.ContextSourceDigest(ctx, tx.Q(), binding.WorkItemID)
			if err != nil {
				return nil, err
			}
			var revision int64
			if err := tx.Q().QueryRowContext(ctx, `SELECT revision FROM work_item_contexts WHERE work_item_id=?`, binding.WorkItemID).Scan(&revision); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO task_agreement_assessments(request_id,task_id,spec_version,task_control_revision,issue_id,assignment_epoch,assignee_ref,context_issue_id,frozen_digest,current_digest,source_digest,context_revision,issue_revision,definition_digest,action,reason,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, x.Env.RequestID, disposition.TaskID, disposition.SpecVersion, *disposition.ExpectedControlRevision, issue.IssueID, issue.AssignmentEpoch, issue.Assignee.Ref, binding.WorkItemID, binding.ContextDigest, digest, sources, revision, contextIssue.Revision, definition, disposition.Action, disposition.Reason, x.Now.UnixMilli()); err != nil {
				return nil, err
			}
		}
	}
	return []Event{{ObjectType: "issue", ObjectID: issue.IssueID, ObjectVersion: issue.Revision, Kind: "issue.task_agreements_assessed", Body: map[string]any{"context_revision": p.ExpectedContextRevision, "task_dispositions": p.TaskDispositions}}}, nil
}

func issueInCurrentAncestry(ctx context.Context, q store.Querier, issue, ancestor string) (bool, error) {
	var inherited bool
	err := q.QueryRowContext(ctx, `WITH RECURSIVE chain(issue_id,parent_issue_id) AS (SELECT issue_id,parent_issue_id FROM issue_controls WHERE issue_id=? UNION ALL SELECT i.issue_id,i.parent_issue_id FROM issue_controls i JOIN chain c ON i.issue_id=c.parent_issue_id) SELECT EXISTS(SELECT 1 FROM chain WHERE issue_id=?)`, issue, ancestor).Scan(&inherited)
	return inherited, err
}
