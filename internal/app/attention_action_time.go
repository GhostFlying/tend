package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func setAttentionActionTime(a *protocol.Attention, at *int64, zone string, reminders *bool) error {
	if at != nil {
		if *at < 0 || *at > 253402300799999 {
			return protocol.Errorf(protocol.CodeInvalidRequest, "invalid latest action time")
		}
		if *at == 0 {
			a.LatestActionAt = nil
		} else {
			if a.LatestActionAt == nil && reminders == nil {
				a.LatestActionReminders = true
			}
			a.LatestActionAt = at
		}
	}
	if zone != "" {
		a.LatestActionTimezone = zone
	}
	if a.LatestActionAt != nil && a.LatestActionTimezone == "" {
		a.LatestActionTimezone = "UTC"
	}
	if a.LatestActionTimezone != "" {
		if a.LatestActionTimezone == "Local" {
			return protocol.Errorf(protocol.CodeInvalidRequest, "use an IANA timezone")
		}
		if _, err := time.LoadLocation(a.LatestActionTimezone); err != nil {
			return protocol.Errorf(protocol.CodeInvalidRequest, "unknown IANA timezone")
		}
	}
	if reminders != nil {
		a.LatestActionReminders = *reminders
	}
	return nil
}
func attentionActionTimeEqual(a, b protocol.Attention) bool {
	return reflect.DeepEqual(a.LatestActionAt, b.LatestActionAt) && a.LatestActionReminders == b.LatestActionReminders
}

// Only the person's actual deadline or business resolution changes its clock.
// Reading, archiving, wording and assignment do not create another schedule.
func syncAttentionActionClock(ctx context.Context, x *Exec, tx *store.Tx, a *protocol.Attention, previous protocol.Attention) error {
	changed := previous.AttentionID == "" || !attentionActionTimeEqual(previous, *a) || (previous.Status == "open") != (a.Status == "open")
	if !changed {
		return nil
	}
	for _, id := range []string{previous.LatestActionNearRuleID, previous.LatestActionDueRuleID} {
		if id == "" {
			continue
		}
		r, err := store.GetFollowup(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		if err := cancelPendingFollowups(ctx, x, tx, id); err != nil {
			return err
		}
		if r.Status != "cancelled" {
			old := r.Revision
			r.Revision++
			r.Status = "cancelled"
			r.Reason = "Attention action time or business status changed"
			r.UpdatedAt = x.Now.UnixMilli()
			if err := tx.SaveFollowup(ctx, r, old, x.Env.RequestID); err != nil {
				return err
			}
		}
	}
	a.LatestActionNearRuleID, a.LatestActionDueRuleID = "", ""
	if a.Status != "open" || a.LatestActionAt == nil || !a.LatestActionReminders {
		return nil
	}
	now := x.Now.UnixMilli()
	windows := []struct {
		name string
		at   int64
	}{{"due", *a.LatestActionAt}}
	if *a.LatestActionAt > now {
		near := *a.LatestActionAt - int64(24*time.Hour/time.Millisecond)
		if near < now {
			near = now
		}
		windows = append([]struct {
			name string
			at   int64
		}{{"near", near}}, windows...)
	}
	for _, window := range windows {
		spec, err := normalizeFollowup(protocol.FollowupSpec{Purpose: a.Title, Instruction: "Review this Attention's latest action time and the user's next action; the reminder neither answers it nor authorizes execution.", SourceID: x.Env.RequestID, Kind: "at", At: window.at, Timezone: a.LatestActionTimezone}, now)
		if err != nil {
			return err
		}
		r := protocol.FollowupRule{FollowupSpec: spec, RuleID: x.IDs.New(protocol.Prefix("followup")), WorkItemID: a.IssueID, Revision: 1, Status: "active", NextAt: window.at, CreatedAt: now, UpdatedAt: now}
		if err := tx.SaveFollowup(ctx, r, 0, x.Env.RequestID); err != nil {
			return err
		}
		if err := tx.FollowupTarget(ctx, r.RuleID, "attention", a.AttentionID); err != nil {
			return err
		}
		if window.name == "near" {
			a.LatestActionNearRuleID = r.RuleID
		} else {
			a.LatestActionDueRuleID = r.RuleID
		}
	}
	return nil
}

// The existing rule/occurrence engine also handles an Attention with no Issue.
// The caller branches here before trying to load a containing work item.
func evaluateAttentionFollowup(ctx context.Context, x *Exec, tx *store.Tx, r *protocol.FollowupRule) (handled, deferred bool, err error) {
	var id string
	err = tx.Q().QueryRowContext(ctx, `SELECT target_id FROM followup_targets WHERE rule_id=? AND kind='attention'`, r.RuleID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return true, false, err
	}
	a, err := store.GetAttention(ctx, tx.Q(), id)
	if err != nil {
		return true, false, err
	}
	now, old := x.Now.UnixMilli(), r.Revision
	save := func(reason, status string) error {
		r.Revision++
		r.UpdatedAt = now
		r.Reason = reason
		r.Status = status
		return tx.SaveFollowup(ctx, *r, old, x.Env.RequestID)
	}
	if a.Status != "open" || a.LatestActionAt == nil || !a.LatestActionReminders || (r.RuleID != a.LatestActionNearRuleID && r.RuleID != a.LatestActionDueRuleID) {
		if r.Status == "cancelled" {
			return true, false, nil
		}
		if err := cancelPendingFollowups(ctx, x, tx, r.RuleID); err != nil {
			return true, false, err
		}
		return true, false, save("Attention action time no longer active", "cancelled")
	}
	if !x.FollowupsEnabled {
		return true, false, nil
	}
	if prepared, err := archivePrepared(ctx, tx.Q()); err != nil || prepared {
		return true, false, err
	}
	if now >= *a.LatestActionAt && a.LatestActionNearRuleID != "" {
		if err := cancelPendingFollowups(ctx, x, tx, a.LatestActionNearRuleID); err != nil {
			return true, false, err
		}
		if r.RuleID == a.LatestActionNearRuleID && r.Status == "active" {
			return true, false, save("due window superseded unsent near reminder", "finished")
		}
	}
	if r.Status != "active" || r.At > now {
		return true, false, nil
	}
	uncertain, err := ownedReminderHasUnknownOccurrence(ctx, tx.Q(), "attention", id)
	if err != nil {
		return true, false, err
	}
	if uncertain {
		// Defer only the existing indexed wake time. No replacement occurrence or
		// business revision is created while the original receipt is unresolved.
		_, err := tx.Exec(ctx, `UPDATE followup_rules SET next_due=? WHERE rule_id=?`, now+30000, r.RuleID)
		return true, true, err
	}
	var exists bool
	if err = tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM followup_occurrences WHERE rule_id=? AND occurrence_key='revision:1')`, r.RuleID).Scan(&exists); err != nil {
		return true, false, err
	}
	if exists {
		return true, false, save("Attention reminder already recorded", "finished")
	}
	window := "due"
	if r.RuleID == a.LatestActionNearRuleID {
		window = "near"
	}
	message := x.IDs.New(protocol.PrefixMessage)
	seq, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: message, Author: "system", Body: "Attention " + window + ": " + a.Title + ". Review the latest action time; reading or archiving does not answer this request.", CreatedAt: now})
	if err != nil {
		return true, false, err
	}
	occurrence := protocol.FollowupOccurrence{OccurrenceID: x.IDs.New(protocol.Prefix("occurrence")), RuleID: r.RuleID, Revision: r.Revision, WorkItemID: a.IssueID, MessageID: message, Status: "queued", Reason: "attention." + window, CreatedAt: now}
	if err := recordFollowupOccurrence(ctx, x, tx, *r, occurrence, "revision:1"); err != nil {
		return true, false, err
	}
	for _, binding := range []struct{ kind, id string }{{"attention", id}, {"followup", r.RuleID}, {"followup_occurrence", occurrence.OccurrenceID}} {
		if err := tx.BindMessage(ctx, message, binding.kind, binding.id, "notice"); err != nil {
			return true, false, err
		}
	}
	event := x.IDs.New(protocol.PrefixEvent)
	if err := registerSystemCommunication(ctx, x, tx, message, "attention_reminder:"+a.AttentionID+":"+window); err != nil {
		return true, false, err
	}
	if _, err := tx.InsertEvent(ctx, store.EventRow{EventID: event, Source: "app", ObjectType: "message", ObjectID: message, ObjectVersion: seq, Kind: "conversation.message", BodyJSON: `{"author":"system","attention_reminder":true}`, RequestID: x.Env.RequestID, CreatedAt: now}); err != nil {
		return true, false, err
	}
	raw, _ := json.Marshal(occurrence)
	if err := appendBackgroundSource(ctx, x, tx, "attention:"+id, "", message, event, "followup.triggered", raw); err != nil {
		return true, false, err
	}
	x.FollowupQueued = true
	r.TriggerCount++
	return true, false, save("attention."+window, "finished")
}
