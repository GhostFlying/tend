package views

import (
	"context"
	"database/sql"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func CanonicalIssueContext(ctx context.Context, q store.Querier, id string, after int64) (protocol.IssueContextView, error) {
	var out protocol.IssueContextView
	if after < 0 {
		return out, protocol.Errorf(protocol.CodeInvalidRequest, "invalid source cursor")
	}
	if _, err := store.GetCanonicalIssue(ctx, q, id); err != nil {
		return out, err
	}
	v, err := IssueContext(ctx, q, id, after)
	if err != nil {
		return out, err
	}
	sources, err := OwnerIssueMessages(ctx, q, v.Sources)
	if err != nil {
		return out, err
	}
	out = protocol.IssueContextView{IssueID: id, Revision: v.Revision, Prepared: v.Prepared, Coverage: v.Coverage, Gaps: v.Gaps, DispatchReady: v.DispatchReady, CoordinationState: v.CoordinationState, DispatchBlockers: v.DispatchBlockers, SyncStatus: v.SyncStatus, Watermark: v.Watermark, Facts: []protocol.IssueContextFact{}, Sources: sources, More: v.More, Next: v.Next}
	for _, f := range v.Facts {
		scope := f.Scope
		if scope == "work_item" {
			scope = "issue"
		}
		out.Facts = append(out.Facts, protocol.IssueContextFact{ID: f.ID, IssueID: f.WorkItemID, Scope: scope, Project: f.Project, Kind: f.Kind, Body: f.Body, SourceMessageID: f.SourceMessageID, Attribution: f.Attribution, Supersedes: f.Supersedes, NeedsReview: f.NeedsReview})
	}
	return out, nil
}

func followupDeadline(ctx context.Context, q store.Querier, id string) (string, error) {
	var deadline string
	err := q.QueryRowContext(ctx, `SELECT target_id FROM followup_targets WHERE rule_id=? AND kind='deadline'`, id).Scan(&deadline)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return deadline, err
}
func CanonicalIssueFollowups(ctx context.Context, q store.Querier, id, after string, enabled bool) (protocol.IssueFollowupPage, error) {
	out := protocol.IssueFollowupPage{IssueID: id, Items: []protocol.IssueFollowupRule{}, Latest: map[string]protocol.IssueFollowupOccurrence{}, Enabled: enabled}
	if _, err := store.GetCanonicalIssue(ctx, q, id); err != nil {
		return out, err
	}
	p, err := store.ListFollowups(ctx, q, id, after)
	if err != nil {
		return out, err
	}
	out.More, out.Next, out.EventSeq = p.More, p.Next, p.EventSeq
	for _, r := range p.Items {
		deadline, err := followupDeadline(ctx, q, r.RuleID)
		if err != nil {
			return out, err
		}
		out.Items = append(out.Items, protocol.CanonicalFollowupRule(r, deadline))
	}
	for id, o := range p.Latest {
		out.Latest[id] = protocol.CanonicalFollowupOccurrence(o)
	}
	return out, nil
}
func CanonicalIssueFollowupHistory(ctx context.Context, q store.Querier, id, rule, afterOccurrence string, afterRevision int64) (protocol.IssueFollowupHistory, error) {
	out := protocol.IssueFollowupHistory{}
	if afterRevision < 0 {
		return out, protocol.Errorf(protocol.CodeInvalidRequest, "invalid revision cursor")
	}
	if _, err := store.GetCanonicalIssue(ctx, q, id); err != nil {
		return out, err
	}
	r, err := store.GetFollowup(ctx, q, rule)
	if err != nil {
		return out, err
	}
	if r.WorkItemID != id {
		return out, protocol.Errorf(protocol.CodeNotFound, "followup does not belong to this Issue")
	}
	p, err := store.FollowupHistory(ctx, q, rule, afterOccurrence, afterRevision)
	if err != nil {
		return out, err
	}
	deadline, err := followupDeadline(ctx, q, rule)
	if err != nil {
		return out, err
	}
	out.Rule = protocol.CanonicalFollowupRule(p.Rule, deadline)
	out.More, out.NextRevision, out.NextOccurrence = p.More, p.NextRevision, p.NextOccurrence
	for _, r := range p.Revisions {
		out.Revisions = append(out.Revisions, protocol.CanonicalFollowupRule(r, deadline))
	}
	for _, o := range p.Occurrences {
		out.Occurrences = append(out.Occurrences, protocol.CanonicalFollowupOccurrence(o))
	}
	return out, nil
}
func CanonicalIssueDeliveries(ctx context.Context, q store.Querier, id string) (protocol.IssueDeliveryPage, error) {
	out := protocol.IssueDeliveryPage{IssueID: id, Items: []protocol.IssueDeliveryView{}}
	issue, err := store.GetCanonicalIssue(ctx, q, id)
	if err != nil {
		return out, err
	}
	for _, d := range issue.Plan.Deliveries {
		o := protocol.DeliveryObservation{WorkItemID: id, DeliveryID: d.DeliveryID, DeliverySnapshot: protocol.DeliverySnapshot{State: "unknown"}}
		row, err := store.GetDeliveryObservation(ctx, q, id, d.DeliveryID)
		if err == nil {
			o = row.Observation
		} else if !errors.Is(err, store.ErrNotFound) {
			return out, err
		}
		out.Items = append(out.Items, protocol.IssueDeliveryView{Delivery: d, Observation: protocol.CanonicalDeliveryObservation(o)})
	}
	return out, nil
}
