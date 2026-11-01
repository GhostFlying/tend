package app

import (
	"context"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"strings"
)

type PutPlannerEntry struct{}

func (PutPlannerEntry) Operation() string { return "planner.entries_put" }
func (PutPlannerEntry) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}
func (PutPlannerEntry) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	if x.Env.Target.Type != "planner_entry" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "planner entry target required")
	}
	var p protocol.PutPlannerEntryPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	v := p.Entry
	if !oneOf(v.Kind, "user_action", "issue_priority") {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "entry.kind must be user_action or issue_priority")
	}
	if !oneOf(v.Status, "open", "blocked", "done", "cancelled") {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "entry.status must be open, blocked, done or cancelled")
	}
	if strings.TrimSpace(v.Title) == "" || strings.TrimSpace(v.Responsible) == "" || strings.TrimSpace(v.Reason) == "" || v.Rank < 0 || v.EstimateSeconds != nil && *v.EstimateSeconds < 0 {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "planner entry needs concrete kind/action/responsibility/status/rank and reason")
	}
	var old protocol.PlannerEntry
	if x.Env.Target.ID != "" {
		var err error
		old, err = store.GetPlannerEntry(ctx, tx.Q(), x.Env.Target.ID)
		if err != nil {
			return Outcome{}, err
		}
		if x.Env.ExpectedControlRevision == nil || *x.Env.ExpectedControlRevision != old.Revision {
			return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "planner entry changed")
		}
		if v.Kind != old.Kind || v.IssueID != old.IssueID || v.AttentionID != old.AttentionID {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "planner target identity cannot change")
		}
		v.EntryID = old.EntryID
	} else {
		if x.Env.ExpectedControlRevision != nil || v.EntryID != "" {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "new entry has no current identity or revision")
		}
		v.EntryID = x.IDs.New(protocol.Prefix("next"))
	}
	if v.IssueID != "" {
		if _, err := store.GetCanonicalIssue(ctx, tx.Q(), v.IssueID); err != nil {
			return Outcome{}, err
		}
	} else if v.Kind == "issue_priority" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "Issue priority requires an Issue")
	}
	if v.AttentionID != "" {
		a, err := store.GetAttention(ctx, tx.Q(), v.AttentionID)
		if err != nil {
			return Outcome{}, err
		}
		if a.IssueID != v.IssueID {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "next action and attention scope differ")
		}
		if a.Status == "open" && oneOf(v.Status, "done", "cancelled") {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "resolve or withdraw the original attention before completing its next action")
		}
	}
	if v.SourceMessageID != "" {
		if _, err := store.GetMessage(ctx, tx.Q(), v.SourceMessageID); err != nil {
			return Outcome{}, err
		}
	}
	if x.Principal.Kind == protocol.PrincipalBackgroundAgent {
		if v.Kind != "user_action" || v.Rank != old.Rank || !oneOf(v.Status, "open", "blocked") {
			return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "manager proposes local actions, not global order or owner completion")
		}
	}
	if x.Principal.Kind != protocol.PrincipalOwner && v.Kind == "user_action" && oneOf(v.Status, "done", "cancelled") {
		if err := validateAttentionOwnerRelay(ctx, tx.Q(), x, v.IssueID, v.SourceMessageID); err != nil {
			return Outcome{}, err
		}
	}
	v.Revision = old.Revision + 1
	v.UpdatedAt = x.Now.UnixMilli()
	if err := tx.SavePlannerEntry(ctx, v, old.Revision); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: v, Events: []Event{{ObjectType: "planner_entry", ObjectID: v.EntryID, ObjectVersion: v.Revision, Kind: "planner.changed", Body: map[string]any{"entry": v}}}}, nil
}
