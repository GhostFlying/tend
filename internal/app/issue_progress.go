package app

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

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
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "issue revision or assignment changed")
	}
	if issue.AssignmentState != "active" || (issue.State != protocol.IssueActive && issue.State != protocol.IssuePaused) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "issue is not editable")
	}
	if !utf8.ValidString(p.Summary) || !utf8.ValidString(p.NextStep) || utf8.RuneCountInString(p.Summary) > 500 || utf8.RuneCountInString(p.NextStep) > 240 {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "summary or next step exceeds its character limit")
	}
	author := protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: x.Principal.Subject}
	if x.coordinationExecution != nil {
		author = issue.Assignee
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
