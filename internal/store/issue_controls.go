package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

// RejectCanonicalLegacyIssueWrite fences the legacy plan mutation path. It does
// not reject independent task results or decision answers associated with an issue.
func RejectCanonicalLegacyIssueWrite(ctx context.Context, q Querier, id string) error {
	var canonical bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issue_controls WHERE issue_id=?)`, id).Scan(&canonical); err != nil {
		return fmt.Errorf("store: inspect canonical issue: %w", err)
	}
	if canonical {
		return protocol.Errorf(protocol.CodeInvalidTransition, "issue uses canonical controls; legacy mutation is unavailable")
	}
	return nil
}

// GetCanonicalIssue reads a canonical aggregate without adopting legacy rows.
func GetCanonicalIssue(ctx context.Context, q Querier, id string) (protocol.Issue, error) {
	var current protocol.Issue
	var parent, pendingKind, pendingRef, handoff sql.NullString
	err := q.QueryRowContext(ctx, `SELECT issue_id,parent_issue_id,state,assignee_kind,assignee_ref,assignment_epoch,assignment_state,pending_assignee_kind,pending_assignee_ref,pending_handoff_id,work_status FROM issue_controls WHERE issue_id=?`, id).Scan(&current.IssueID, &parent, &current.State, &current.Assignee.Kind, &current.Assignee.Ref, &current.AssignmentEpoch, &current.AssignmentState, &pendingKind, &pendingRef, &handoff, &current.WorkStatus)
	if err != nil {
		return current, notFound(err, "canonical issue "+id)
	}
	current.ParentIssueID, current.PendingHandoffID = parent.String, handoff.String
	if pendingKind.Valid {
		current.PendingAssignee = &protocol.IssueAssignee{Kind: protocol.IssueAssigneeKind(pendingKind.String), Ref: pendingRef.String}
	}
	plan, err := GetIssue(ctx, q, id)
	if err != nil {
		return current, err
	}
	current.Revision, current.Plan, current.Closure = plan.Revision, plan.IssuePlan, plan.Closure
	current.CreatedAt, current.UpdatedAt = plan.CreatedAt, plan.UpdatedAt
	current.DependsOn = []string{}
	rows, err := q.QueryContext(ctx, `SELECT depends_on FROM issue_dependencies WHERE issue_id=? ORDER BY depends_on`, id)
	if err != nil {
		return current, fmt.Errorf("store: read issue dependencies: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var dep string
		if err := rows.Scan(&dep); err != nil {
			return current, err
		}
		current.DependsOn = append(current.DependsOn, dep)
	}
	if err := rows.Err(); err != nil {
		return current, err
	}
	if err := rows.Close(); err != nil {
		return current, err
	}
	historical, err := GetCanonicalIssueRevision(ctx, q, id, current.Revision)
	if err != nil {
		return current, err
	}
	if !reflect.DeepEqual(current, historical) {
		return current, fmt.Errorf("store: canonical issue current and revision disagree")
	}
	return current, nil
}

// GetCanonicalIssueRevision composes the two immutable snapshots at one revision.
// Missing canonical history is an error, not permission to infer responsibility.
func GetCanonicalIssueRevision(ctx context.Context, q Querier, id string, revision int64) (protocol.Issue, error) {
	var raw string
	if err := q.QueryRowContext(ctx, `SELECT snapshot_json FROM issue_control_revisions WHERE issue_id=? AND revision=?`, id, revision).Scan(&raw); err != nil {
		return protocol.Issue{}, notFound(err, "canonical issue revision")
	}
	var snapshot protocol.IssueControlSnapshot
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		return protocol.Issue{}, fmt.Errorf("store: decode issue control revision: %w", err)
	}
	if snapshot.Control.IssueID != id || snapshot.Revision != revision {
		return protocol.Issue{}, fmt.Errorf("store: issue control revision identity mismatch")
	}
	plan, err := GetIssueRevision(ctx, q, id, revision)
	if err != nil {
		return protocol.Issue{}, err
	}
	out := protocol.Issue{IssueControl: snapshot.Control, Revision: revision, Plan: plan.IssuePlan, Closure: plan.Closure, CreatedAt: plan.CreatedAt, UpdatedAt: plan.UpdatedAt, DependsOn: []string{}}
	for _, edge := range snapshot.Dependencies {
		if edge.IssueID != id {
			return protocol.Issue{}, fmt.Errorf("store: issue dependency snapshot identity mismatch")
		}
		out.DependsOn = append(out.DependsOn, edge.DependsOn)
	}
	return out, nil
}

// IssueRevisionProvenance carries app-verified authority. It does not itself
// authenticate an owner or establish a coordination execution's permission.
type IssueRevisionProvenance struct {
	Actor                   protocol.Principal
	RequestID               string
	SourceMessageID         string
	CoordinationExecutionID string
	EffectSeq               int64
	Reason                  any
}

// SaveCanonicalIssue writes current control, plan and both immutable snapshots.
// App must validate authorization, lifecycle and complete graph facts in this same
// transaction. This storage primitive is not registered as a business entry point.
func (t *Tx) SaveCanonicalIssue(ctx context.Context, next protocol.Issue, expected int64, p IssueRevisionProvenance) error {
	if expected < 0 || next.Revision < 1 || next.Revision != expected+1 {
		return protocol.Errorf(protocol.CodeStaleRevision, "canonical issue revision must advance once")
	}
	if !protocol.ValidID(protocol.PrefixWorkItem, next.IssueID) || strings.TrimSpace(p.RequestID) == "" {
		return protocol.Errorf(protocol.CodeInvalidRequest, "canonical issue identity and request provenance are required")
	}
	if err := p.Actor.Validate(); err != nil {
		return protocol.Errorf(protocol.CodeInvalidRequest, "canonical issue actor provenance is invalid")
	}
	if (p.CoordinationExecutionID == "" && p.EffectSeq != 0) || (p.CoordinationExecutionID != "" && p.EffectSeq < 1) {
		return protocol.Errorf(protocol.CodeInvalidRequest, "coordination provenance requires execution and sequence together")
	}
	if expected > 0 {
		current, err := GetCanonicalIssue(ctx, t.Q(), next.IssueID)
		if err != nil {
			return err
		}
		if current.Revision != expected {
			return protocol.Errorf(protocol.CodeStaleRevision, "canonical issue revision changed")
		}
		if current.CreatedAt != next.CreatedAt {
			return protocol.Errorf(protocol.CodeInvalidRequest, "issue creation time is immutable")
		}
	}
	status := protocol.WorkItemOpen
	switch next.State {
	case protocol.IssuePaused, protocol.IssueCancelling:
		status = protocol.WorkItemSnoozed
	case protocol.IssueCancelled, protocol.IssueClosed:
		status = protocol.WorkItemClosed
	case protocol.IssueActive:
	default:
		return protocol.Errorf(protocol.CodeInvalidRequest, "invalid canonical issue state")
	}
	deps := slices.Clone(next.DependsOn)
	slices.Sort(deps)
	edges := make([]protocol.IssueDependency, 0, len(deps))
	for _, dep := range deps {
		edges = append(edges, protocol.IssueDependency{IssueID: next.IssueID, DependsOn: dep})
	}
	snapshot, err := json.Marshal(protocol.IssueControlSnapshot{Revision: next.Revision, Control: next.IssueControl, Dependencies: edges})
	if err != nil {
		return err
	}
	reason, err := json.Marshal(p.Reason)
	if err != nil {
		return err
	}
	actor, err := json.Marshal(p.Actor)
	if err != nil {
		return err
	}
	projection := protocol.WorkItem{IssuePlan: next.Plan, WorkItemID: next.IssueID, Revision: next.Revision, Status: status, Closure: next.Closure, CreatedAt: next.CreatedAt, UpdatedAt: next.UpdatedAt}
	if err := t.saveIssueProjection(ctx, projection, expected, p.Reason); err != nil {
		return err
	}
	var pendingKind, pendingRef any
	if next.PendingAssignee != nil {
		pendingKind, pendingRef = next.PendingAssignee.Kind, next.PendingAssignee.Ref
	}
	_, err = t.Exec(ctx, `INSERT INTO issue_controls(issue_id,parent_issue_id,state,assignee_kind,assignee_ref,assignment_epoch,assignment_state,pending_assignee_kind,pending_assignee_ref,pending_handoff_id,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(issue_id) DO UPDATE SET parent_issue_id=excluded.parent_issue_id,state=excluded.state,assignee_kind=excluded.assignee_kind,assignee_ref=excluded.assignee_ref,assignment_epoch=excluded.assignment_epoch,assignment_state=excluded.assignment_state,pending_assignee_kind=excluded.pending_assignee_kind,pending_assignee_ref=excluded.pending_assignee_ref,pending_handoff_id=excluded.pending_handoff_id,updated_at=excluded.updated_at`, next.IssueID, nullString(next.ParentIssueID), next.State, next.Assignee.Kind, next.Assignee.Ref, next.AssignmentEpoch, next.AssignmentState, pendingKind, pendingRef, nullString(next.PendingHandoffID), next.UpdatedAt)
	if err != nil {
		return fmt.Errorf("store: save issue controls: %w", err)
	}
	// A new empty progress uses the column default. This also keeps historical
	// fixture creation independent of fields added by later migrations.
	if next.WorkStatus != "" || expected > 0 {
		if _, err := t.Exec(ctx, `UPDATE issue_controls SET work_status=? WHERE issue_id=?`, next.WorkStatus, next.IssueID); err != nil {
			return fmt.Errorf("store: save issue work status: %w", err)
		}
	}
	if _, err := t.Exec(ctx, `DELETE FROM issue_dependencies WHERE issue_id=?`, next.IssueID); err != nil {
		return fmt.Errorf("store: replace issue dependencies: %w", err)
	}
	for _, dep := range deps {
		if _, err := t.Exec(ctx, `INSERT INTO issue_dependencies(issue_id,depends_on) VALUES(?,?)`, next.IssueID, dep); err != nil {
			return fmt.Errorf("store: save issue dependency: %w", err)
		}
	}
	var effect any
	if p.CoordinationExecutionID != "" {
		effect = p.EffectSeq
	}
	_, err = t.Exec(ctx, `INSERT INTO issue_control_revisions(issue_id,revision,snapshot_json,reason_json,actor_json,request_id,source_message_id,coordination_execution_id,effect_seq,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, next.IssueID, next.Revision, string(snapshot), string(reason), string(actor), p.RequestID, nullString(p.SourceMessageID), nullString(p.CoordinationExecutionID), effect, next.UpdatedAt)
	if err != nil {
		return fmt.Errorf("store: record issue control revision: %w", err)
	}
	return nil
}
