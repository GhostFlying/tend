package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func issueReuseStatus(ctx context.Context, q store.Querier, id string) (protocol.IssueReuseStatus, error) {
	out := protocol.IssueReuseStatus{OriginObservation: "observation_unavailable", NextStep: "Reconcile the original creation request; never create a replacement merely because its outcome is unknown."}
	var err error
	out.Issue, err = store.GetCanonicalIssue(ctx, q, id)
	if err != nil {
		return out, errNotFound(err, "original Issue does not exist")
	}
	var origin, actor string
	err = q.QueryRowContext(ctx, `SELECT request_id,actor_json FROM issue_control_revisions WHERE issue_id=? AND revision=1`, id).Scan(&origin, &actor)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	var principal protocol.Principal
	if json.Unmarshal([]byte(actor), &principal) != nil || principal.Validate() != nil || origin == "" {
		return out, nil
	}
	record, err := store.GetRequest(ctx, q, principal.DedupDomain(), origin)
	if errors.Is(err, store.ErrNotFound) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	var created protocol.Issue
	if record.Status != protocol.OpSucceeded || (record.Operation != "issues.create" && record.Operation != "conversation.promote") || json.Unmarshal([]byte(record.ResultJSON), &created) != nil || created.IssueID != id || created.Revision != 1 {
		return out, nil
	}
	out.OriginObservation = "recorded"
	out.OriginalRequestID = origin
	out.NextStep = "Reuse only this exact active/paused responsibility with its current Issue revision/epoch and the new owner source content/association revisions. New material still requires its responsible review."
	return out, nil
}

func (a *App) IssueReuseStatus(ctx context.Context, id string) (protocol.IssueReuseStatus, error) {
	var out protocol.IssueReuseStatus
	err := a.store.Read(ctx, func(tx *store.ReadTx) error { var err error; out, err = issueReuseStatus(ctx, tx.Q(), id); return err })
	return out, err
}

// ReuseIssue adds a new owner input to an explicitly selected original Issue.
type ReuseIssue struct{}

func (ReuseIssue) Operation() string { return "issues.reuse" }
func (ReuseIssue) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner, protocol.PrincipalMainAgent)
}
func (ReuseIssue) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	if x.Env.Target.Type != "issue" || x.Env.ParentRequestID != "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "independent original Issue request required")
	}
	expected, err := requireRevision(x.Env)
	if err != nil {
		return Outcome{}, err
	}
	var p protocol.ReuseIssuePayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if p.SourceMessageID == "" || p.OriginalRequestID == "" || (p.ExpectedAssociationRevision == nil || *p.ExpectedAssociationRevision < 0) || strings.TrimSpace(p.Reason) == "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "original creation receipt, owner source, source association revision and reason required")
	}
	status, err := issueReuseStatus(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, err
	}
	issue := status.Issue
	if issue.Revision != expected || issue.AssignmentEpoch != p.ExpectedAssignmentEpoch || issue.AssignmentState != "active" {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "original Issue responsibility changed")
	}
	if issue.State != protocol.IssueActive && issue.State != protocol.IssuePaused {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "reuse cannot reopen or revive a terminal responsibility")
	}
	if status.OriginObservation != "recorded" {
		return Outcome{}, protocol.Errorf(protocol.CodeUnknownOutcome, "original Issue creation is not confirmed; reconcile its original request")
	}
	if status.OriginalRequestID != p.OriginalRequestID {
		return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "creation receipt belongs to a different responsibility")
	}
	if err := validateCanonicalSourceReference(ctx, tx.Q(), canonicalOwnerIssueSource(p.IssueSourceReference)); err != nil {
		return Outcome{}, err
	}
	message, err := store.GetMessage(ctx, tx.Q(), p.SourceMessageID)
	if err != nil {
		return Outcome{}, errNotFound(err, "owner source does not exist")
	}
	if message.Author != "owner" {
		return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "reuse requires an actual owner input")
	}
	if x.Principal.Kind != protocol.PrincipalOwner {
		if x.coordinationExecution == nil || x.coordinationExecution.Selection.Purpose != agentconfig.Main {
			return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "only an admitted Main execution can relay responsibility reuse")
		}
		if err := validateAttentionOwnerRelay(ctx, tx.Q(), x, issue.IssueID, p.SourceMessageID); err != nil {
			return Outcome{}, err
		}
	}
	source, err := store.MessageAssociations(ctx, tx.Q(), p.SourceMessageID)
	if err != nil {
		return Outcome{}, err
	}
	if source.Revision != *p.ExpectedAssociationRevision || source.ContentRevision != *p.ExpectedSourceRevision {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "owner source association changed")
	}
	ids := make([]string, 0, len(source.Items)+1)
	contains := false
	for _, item := range source.Items {
		ids = append(ids, item.WorkItemID)
		if item.WorkItemID == issue.IssueID {
			contains = true
		}
	}
	if !contains {
		ids = append(ids, issue.IssueID)
	}
	inner := *x
	env := *x.Env
	inner.Env = &env
	env.Target = protocol.Target{Type: "message", ID: p.SourceMessageID}
	env.ExpectedControlRevision = p.ExpectedAssociationRevision
	association, err := applyMessageAssociationsWithRetainedLinks(ctx, &inner, tx, protocol.SetMessageAssociationsPayload{Disposition: "linked", ContentRevision: *p.ExpectedSourceRevision, WorkItemIDs: ids}, true, true)
	if err != nil {
		return Outcome{}, err
	}
	updated, err := store.MessageAssociations(ctx, tx.Q(), p.SourceMessageID)
	if err != nil {
		return Outcome{}, err
	}
	issue.Revision++
	issue.UpdatedAt = x.Now.UnixMilli()
	if err := tx.SaveCanonicalIssue(ctx, issue, expected, store.IssueRevisionProvenance{Actor: x.Principal, RequestID: x.Env.RequestID, SourceMessageID: p.SourceMessageID, CoordinationExecutionID: x.coordinationID, EffectSeq: x.coordinationEffectSeq, Reason: p}); err != nil {
		return Outcome{}, err
	}
	association.Result = protocol.IssueReuseResult{Issue: issue, OriginalRequestID: p.OriginalRequestID, Source: updated, Reused: true, NextStep: "The original responsibility now owns this source; its existing context review and execution fences still apply."}
	association.Events = append(association.Events, Event{ObjectType: "issue", ObjectID: issue.IssueID, ObjectVersion: issue.Revision, Kind: "issue.reused", Body: map[string]any{"source_message_id": p.SourceMessageID, "source_revision": p.ExpectedSourceRevision, "original_request_id": p.OriginalRequestID}})
	return association, nil
}
