package app

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// UpdateIssueProgress uses the same aggregate CAS as definition changes.
type UpdateIssueProgress struct{}

func (UpdateIssueProgress) Operation() string { return "issues.update_progress" }
func (UpdateIssueProgress) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}
func (UpdateIssueProgress) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	if x.Env.Target.Type != "issue" || x.Env.ParentRequestID != "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "progress requires its own issue request")
	}
	expected, err := requireRevision(x.Env)
	if err != nil {
		return Outcome{}, err
	}
	issue, err := store.GetCanonicalIssue(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, errNotFound(err, "issue does not exist")
	}
	var p protocol.UpdateIssueProgressPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	var present struct {
		Summary  *string `json:"summary"`
		NextStep *string `json:"next_step"`
	}
	if err := json.Unmarshal(x.Env.Payload, &present); err != nil {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "invalid progress payload")
	}
	if present.Summary == nil || present.NextStep == nil {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "summary and next_step must both be supplied; use two empty strings to clear")
	}
	if issue.Revision != expected || p.ExpectedAssignmentEpoch != issue.AssignmentEpoch {
		return Outcome{}, commandDiagnostic(protocol.CodeStaleRevision, "issue revision or assignment changed; read the original Issue before updating progress", map[string]any{"issue_id": issue.IssueID, "current_control_revision": issue.Revision, "expected_control_revision": expected, "current_assignment_epoch": issue.AssignmentEpoch, "expected_assignment_epoch": p.ExpectedAssignmentEpoch, "recovery_tool": "issues_read"})
	}
	if issue.AssignmentState != "active" || (issue.State != protocol.IssueActive && issue.State != protocol.IssuePaused) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "issue is not editable")
	}
	if err := p.ValidateInput(); err != nil {
		return Outcome{}, err
	}
	author := protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: x.Principal.Subject}
	if x.coordinationExecution != nil {
		author = issue.Assignee
	} else if x.Actor != nil && x.actorAgent != nil {
		author = protocol.IssueAssignee{Kind: protocol.IssueAssigneeAgent, Ref: x.actorAgent.ExecutionEnvironmentID + "/" + x.actorAgent.LocalID}
	}
	prior, err := store.IssueProgress(ctx, tx.Q(), issue.IssueID, issue.Revision)
	if err != nil {
		return Outcome{}, err
	}
	if prior != nil && prior.Summary == strings.TrimSpace(p.Summary) && prior.NextStep == strings.TrimSpace(p.NextStep) && prior.UpdatedBy == author {
		return Outcome{NoEffect: true, Result: *prior}, nil
	}
	issue.Revision++
	issue.UpdatedAt = x.Now.UnixMilli()
	progress := protocol.IssueProgress{Summary: strings.TrimSpace(p.Summary), NextStep: strings.TrimSpace(p.NextStep), IssueRevision: issue.Revision, UpdatedAt: issue.UpdatedAt, UpdatedBy: author}
	// The deferred snapshot FK keeps this assessment atomic with the aggregate
	// save, and lets its context trigger distinguish assessment-only revisions.
	if err := tx.InsertIssueProgress(ctx, issue.IssueID, progress); err != nil {
		return Outcome{}, err
	}
	out, err := saveCanonicalOwnerDefinition(ctx, x, tx, issue, expected, canonicalOwnerIssueSource{}, p, "issue.progress_updated")
	if err != nil {
		return Outcome{}, err
	}
	out.Result = progress
	return out, nil
}
