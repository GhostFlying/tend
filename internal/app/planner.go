package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
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
	if !protocol.ValidID(protocol.Prefix("next"), x.Env.Target.ID) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "planner entry requires its stable entry identity")
	}
	old, err := store.GetPlannerEntry(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return Outcome{}, err
	}
	if err == nil {
		if v.Kind != old.Kind || v.IssueID != old.IssueID || v.AttentionID != old.AttentionID {
			return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "planner entry identity belongs to another responsibility")
		}
		if x.Env.ExpectedControlRevision == nil {
			initial, err := plannerCreationEntry(ctx, tx.Q(), old)
			if err != nil {
				return Outcome{}, err
			}
			wanted := v
			wanted.EntryID, wanted.Revision, wanted.UpdatedAt = "", 0, 0
			initial.EntryID, initial.Revision, initial.UpdatedAt = "", 0, 0
			if !reflect.DeepEqual(wanted, initial) {
				return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "planner entry identity already has another original definition; changes require its current revision")
			}
			return Outcome{Result: old, NoEffect: true}, nil
		}
		if *x.Env.ExpectedControlRevision != old.Revision {
			return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "planner entry changed")
		}
	} else if x.Env.ExpectedControlRevision != nil {
		return Outcome{}, protocol.Errorf(protocol.CodeNotFound, "planner entry does not exist")
	}
	v.EntryID = x.Env.Target.ID
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

func plannerCreationEntry(ctx context.Context, q store.Querier, current protocol.PlannerEntry) (protocol.PlannerEntry, error) {
	if current.Revision == 1 {
		return current, nil
	}
	missing := func() (protocol.PlannerEntry, error) {
		return protocol.PlannerEntry{}, protocol.Errorf(protocol.CodeInvalidRequest, "original planner entry definition is unavailable; read the entry and use its current revision for changes")
	}
	var raw string
	if err := q.QueryRowContext(ctx, `SELECT body_json FROM events WHERE object_type='planner_entry' AND object_id=? AND object_version=1 AND kind='planner.changed' ORDER BY event_seq LIMIT 1`, current.EntryID).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return missing()
		}
		return protocol.PlannerEntry{}, err
	}
	raw, err := store.ResolveJSON(ctx, q, raw)
	if err != nil {
		return protocol.PlannerEntry{}, err
	}
	var original struct {
		Entry protocol.PlannerEntry `json:"entry"`
	}
	if err := json.Unmarshal([]byte(raw), &original); err != nil {
		return protocol.PlannerEntry{}, err
	}
	if original.Entry.EntryID != current.EntryID || original.Entry.Revision != 1 {
		return missing()
	}
	return original.Entry, nil
}
