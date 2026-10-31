package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type IssueDeadlineCommand struct{ Cancel bool }

func (c IssueDeadlineCommand) Operation() string {
	if c.Cancel {
		return "issues.deadlines_cancel"
	}
	return "issues.deadlines_put"
}
func (IssueDeadlineCommand) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}
func (c IssueDeadlineCommand) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	if x.Env.Target.Type != "deadline" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "deadline target is required")
	}
	var previous protocol.IssueDeadline
	if x.Env.Target.ID != "" {
		var err error
		previous, err = store.GetIssueDeadline(ctx, tx.Q(), x.Env.Target.ID)
		if err != nil {
			return Outcome{}, err
		}
		if x.Env.ExpectedControlRevision == nil || *x.Env.ExpectedControlRevision != previous.Revision {
			return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "deadline changed")
		}
	} else if c.Cancel || x.Env.ExpectedControlRevision != nil {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "new deadline has no current revision")
	}
	var d protocol.IssueDeadline
	if x.Principal.Kind == protocol.PrincipalBackgroundAgent && previous.DeadlineID != "" && previous.Nature != "estimate" {
		return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "manager cannot replace an external deadline or owner commitment")
	}
	if c.Cancel {
		d = previous
		var source protocol.CancelIssueDeadlinePayload
		if err := decodePayload(x.Env, &source); err != nil {
			return Outcome{}, err
		}
		if source.SourceMessageID != "" {
			if err := validateCanonicalSourceReference(ctx, tx.Q(), canonicalOwnerIssueSource{SourceMessageID: source.SourceMessageID, ExpectedSourceRevision: source.ExpectedSourceRevision}); err != nil {
				return Outcome{}, err
			}
		}
		if x.Principal.Kind != protocol.PrincipalOwner && previous.Nature == "user_commitment" {
			if err := validateAttentionOwnerRelay(ctx, tx.Q(), x, d.IssueID, source.SourceMessageID); err != nil {
				return Outcome{}, err
			}
		}
		d.Status = "cancelled"
		d.Reason = x.Env.Reason
		if strings.TrimSpace(d.Reason) == "" {
			d.Reason = source.Reason
		}
		if strings.TrimSpace(d.Reason) == "" {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "cancellation reason is required")
		}
	} else {
		var p protocol.PutIssueDeadlinePayload
		if err := decodePayload(x.Env, &p); err != nil {
			return Outcome{}, err
		}
		if previous.DeadlineID != "" && p.IssueID != previous.IssueID {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "deadline stays with its original Issue")
		}
		issue, err := store.GetCanonicalIssue(ctx, tx.Q(), p.IssueID)
		if err != nil {
			return Outcome{}, err
		}
		if issue.State == protocol.IssueClosed || issue.State == protocol.IssueCancelled {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "terminal Issue cannot set a deadline")
		}
		if strings.TrimSpace(p.Title) == "" || strings.TrimSpace(p.Reason) == "" || !oneOf(p.Nature, "external", "user_commitment", "estimate") || !oneOf(p.Strength, "soft", "hard") || p.Timezone == "Local" || p.Timezone == "" {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "deadline needs title, reason, nature, strength and IANA timezone")
		}
		if _, err := time.LoadLocation(p.Timezone); err != nil {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "unknown IANA timezone")
		}
		if p.DueAt != nil && (*p.DueAt <= 0 || *p.DueAt > 253402300799999) || p.EstimateSeconds != nil && *p.EstimateSeconds < 0 {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "invalid due time or estimate")
		}
		if p.SourceMessageID != "" {
			if err := validateCanonicalSourceReference(ctx, tx.Q(), canonicalOwnerIssueSource{SourceMessageID: p.SourceMessageID, ExpectedSourceRevision: p.ExpectedSourceRevision}); err != nil {
				return Outcome{}, err
			}
		}
		if x.Principal.Kind != protocol.PrincipalOwner && (p.Nature == "user_commitment" || previous.Nature == "user_commitment") {
			if err := validateAttentionOwnerRelay(ctx, tx.Q(), x, p.IssueID, p.SourceMessageID); err != nil {
				return Outcome{}, err
			}
		}
		if x.Principal.Kind == protocol.PrincipalBackgroundAgent && p.Nature != "estimate" {
			return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "manager can propose an estimate, not change global commitments")
		}
		d = protocol.IssueDeadline{DeadlineID: previous.DeadlineID, IssueID: p.IssueID, Status: "active", Title: p.Title, Nature: p.Nature, Strength: p.Strength, DueAt: p.DueAt, Timezone: p.Timezone, EstimateSeconds: p.EstimateSeconds, SourceMessageID: p.SourceMessageID, Reminders: true, Reason: p.Reason, CreatedAt: previous.CreatedAt}
		if p.Reminders != nil {
			d.Reminders = *p.Reminders
		}
		if p.ExpectedSourceRevision != nil {
			d.SourceRevision = *p.ExpectedSourceRevision
		}
		same := previous
		same.Revision = 0
		same.UpdatedAt = 0
		same.NearRuleID = ""
		same.DueRuleID = ""
		if reflect.DeepEqual(d, same) {
			return Outcome{Result: previous}, nil
		}
	}
	now := x.Now.UnixMilli()
	if d.DeadlineID == "" {
		d.DeadlineID = x.IDs.New(protocol.Prefix("deadline"))
		d.CreatedAt = now
	}
	clockChanged := previous.DeadlineID == "" || previous.Status != d.Status || !reflect.DeepEqual(previous.DueAt, d.DueAt) || previous.SourceMessageID != d.SourceMessageID || previous.SourceRevision != d.SourceRevision || previous.Reminders != d.Reminders
	if clockChanged {
		for _, rule := range []string{previous.NearRuleID, previous.DueRuleID} {
			if rule == "" {
				continue
			}
			r, err := store.GetFollowup(ctx, tx.Q(), rule)
			if err != nil {
				return Outcome{}, err
			}
			old := r.Revision
			r.Revision++
			r.Status = "cancelled"
			r.Reason = "deadline revision changed"
			r.UpdatedAt = now
			if err := cancelPendingFollowups(ctx, x, tx, rule); err != nil {
				return Outcome{}, err
			}
			if err := tx.SaveFollowup(ctx, r, old, x.Env.RequestID); err != nil {
				return Outcome{}, err
			}
		}
	}
	d.Revision = previous.Revision + 1
	d.UpdatedAt = now
	d.NearRuleID = previous.NearRuleID
	d.DueRuleID = previous.DueRuleID
	if clockChanged {
		d.NearRuleID = ""
		d.DueRuleID = ""
	}
	var rules []protocol.FollowupRule
	if clockChanged && d.Status == "active" && d.Reminders && d.DueAt != nil {
		times := []struct {
			window string
			at     int64
		}{{"due", *d.DueAt}}
		if *d.DueAt > now {
			near := *d.DueAt - int64(24*time.Hour/time.Millisecond)
			if near < now {
				near = now
			}
			times = append([]struct {
				window string
				at     int64
			}{{"near", near}}, times...)
		}
		for _, slot := range times {
			source := d.SourceMessageID
			if source == "" {
				source = x.Env.RequestID
			}
			spec, err := normalizeFollowup(protocol.FollowupSpec{Purpose: d.Title, Instruction: "Review this deadline and the user's next action; do not change the commitment or execute work without authority.", SourceID: source, Kind: "at", At: slot.at, Timezone: d.Timezone}, now)
			if err != nil {
				return Outcome{}, err
			}
			r := protocol.FollowupRule{FollowupSpec: spec, RuleID: x.IDs.New(protocol.Prefix("followup")), WorkItemID: d.IssueID, Revision: 1, Status: "active", NextAt: slot.at, CreatedAt: now, UpdatedAt: now}
			if err := tx.SaveFollowup(ctx, r, 0, x.Env.RequestID); err != nil {
				return Outcome{}, err
			}
			if err := tx.FollowupTarget(ctx, r.RuleID, "deadline", d.DeadlineID); err != nil {
				return Outcome{}, err
			}
			if err := tx.FollowupTarget(ctx, r.RuleID, "issue", d.IssueID); err != nil {
				return Outcome{}, err
			}
			if d.SourceMessageID != "" {
				if err := tx.FollowupTarget(ctx, r.RuleID, "message", d.SourceMessageID); err != nil {
					return Outcome{}, err
				}
			}
			if slot.window == "near" {
				d.NearRuleID = r.RuleID
			} else {
				d.DueRuleID = r.RuleID
			}
			rules = append(rules, r)
		}
	}
	if err := tx.SaveIssueDeadline(ctx, d, previous.Revision, x.Env.RequestID); err != nil {
		return Outcome{}, err
	}
	for _, r := range rules {
		if !x.FollowupsEnabled {
			break
		}
		if _, err := evaluateDeadlineFollowup(ctx, x, tx, &r); err != nil {
			return Outcome{}, err
		}
	}
	return Outcome{Notify: true, Result: d, Events: []Event{{ObjectType: "deadline", ObjectID: d.DeadlineID, ObjectVersion: d.Revision, Kind: "deadline.changed", Body: map[string]any{"issue_id": d.IssueID}}}}, nil
}

// evaluateDeadlineFollowup uses the existing occurrence/outbox engine, but the
// recipient is always the global planner. Assignment does not change its clock.
func evaluateDeadlineFollowup(ctx context.Context, x *Exec, tx *store.Tx, r *protocol.FollowupRule) (bool, error) {
	var id string
	err := tx.Q().QueryRowContext(ctx, `SELECT target_id FROM followup_targets WHERE rule_id=? AND kind='deadline'`, r.RuleID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	d, err := store.GetIssueDeadline(ctx, tx.Q(), id)
	if err != nil {
		return true, err
	}
	old := r.Revision
	now := x.Now.UnixMilli()
	save := func(reason, status string) error {
		r.Revision++
		r.Reason = reason
		r.Status = status
		r.UpdatedAt = now
		return tx.SaveFollowup(ctx, *r, old, x.Env.RequestID)
	}
	current, err := store.GetCanonicalIssue(ctx, tx.Q(), d.IssueID)
	if err != nil {
		return true, err
	}
	if d.Status == "active" && (current.State == protocol.IssueClosed || current.State == protocol.IssueCancelled) {
		previous := d.Revision
		d.Revision++
		d.Status = "cancelled"
		d.Reason = "Issue responsibility ended; pending reminders cancelled"
		d.UpdatedAt = now
		if err := tx.SaveIssueDeadline(ctx, d, previous, x.Env.RequestID); err != nil {
			return true, err
		}
	}
	if d.Status != "active" || current.State == protocol.IssueClosed || current.State == protocol.IssueCancelled || !d.Reminders || (r.RuleID != d.NearRuleID && r.RuleID != d.DueRuleID) {
		if r.Status == "cancelled" {
			return true, nil
		}
		if err := cancelPendingFollowups(ctx, x, tx, r.RuleID); err != nil {
			return true, err
		}
		return true, save("deadline is no longer active", "cancelled")
	}
	if d.SourceMessageID != "" {
		changed, err := deadlineSourceChanged(ctx, tx.Q(), d)
		if err != nil {
			return true, err
		}
		if changed {
			if err := cancelPendingFollowups(ctx, x, tx, r.RuleID); err != nil {
				return true, err
			}
			if r.Status == "paused" {
				return true, nil
			}
			return true, save("deadline source changed; explicit review is required", "paused")
		}
	}
	if d.DueAt != nil && now >= *d.DueAt && d.NearRuleID != "" {
		// Cancel only never-claimed delivery; existing native/unknown receipts
		// remain facts under the original occurrence identity.
		if err := cancelPendingFollowups(ctx, x, tx, d.NearRuleID); err != nil {
			return true, err
		}
		if r.RuleID == d.NearRuleID && r.Status == "active" {
			return true, save("due window superseded the unsent near reminder", "finished")
		}
	}
	if r.Status != "active" || r.At > now {
		return true, nil
	}
	uncertain, err := deadlineHasUnknownOccurrence(ctx, tx.Q(), id)
	if err != nil {
		return true, err
	}
	if uncertain {
		// Keep this new window eligible for the existing sweep after the
		// original occurrence is reconciled. Never replay that occurrence.
		r.NextAt = now + int64(30*time.Second/time.Millisecond)
		return true, save("previous deadline notification is unknown; reconcile before a new attempt", "active")
	}
	var exists bool
	if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM followup_occurrences WHERE rule_id=? AND occurrence_key='revision:1')`, r.RuleID).Scan(&exists); err != nil {
		return true, err
	}
	if exists {
		return true, save("deadline window already recorded", "finished")
	}
	window := "due"
	if r.RuleID == d.NearRuleID {
		window = "near"
	}
	message := x.IDs.New(protocol.PrefixMessage)
	body := "Deadline " + window + ": " + d.Title + ". Review the user's next action; this reminder does not authorize changes or execution."
	seq, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: message, Author: "system", Body: body, CreatedAt: now})
	if err != nil {
		return true, err
	}
	o := protocol.FollowupOccurrence{OccurrenceID: x.IDs.New(protocol.Prefix("occurrence")), RuleID: r.RuleID, Revision: r.Revision, WorkItemID: d.IssueID, MessageID: message, Status: "queued", Reason: "deadline." + window, CreatedAt: now}
	if err := tx.InsertFollowupOccurrence(ctx, o, "revision:1"); err != nil {
		return true, err
	}
	for _, b := range []struct{ kind, id string }{{"deadline", id}, {"followup", r.RuleID}, {"followup_occurrence", o.OccurrenceID}} {
		if err := tx.BindMessage(ctx, message, b.kind, b.id, "notice"); err != nil {
			return true, err
		}
	}
	if err := registerSystemCommunication(ctx, x, tx, message, "deadline_reminder:"+id+":"+window); err != nil {
		return true, err
	}
	eid := x.IDs.New(protocol.PrefixEvent)
	if _, err := tx.InsertEvent(ctx, store.EventRow{EventID: eid, Source: "app", ObjectType: "message", ObjectID: message, ObjectVersion: seq, Kind: "conversation.message", BodyJSON: `{"author":"system","deadline":true}`, RequestID: x.Env.RequestID, CreatedAt: now}); err != nil {
		return true, err
	}
	raw, _ := json.Marshal(o)
	if err := appendBackgroundSource(ctx, x, tx, "deadline:"+id, "", message, eid, "followup.triggered", raw); err != nil {
		return true, err
	}
	x.FollowupQueued = true
	r.TriggerCount++
	return true, save("deadline."+window, "finished")
}

func deadlineSourceChanged(ctx context.Context, q store.Querier, d protocol.IssueDeadline) (bool, error) {
	if d.SourceMessageID == "" {
		return false, nil
	}
	err := validateCanonicalSourceReference(ctx, q, canonicalOwnerIssueSource{SourceMessageID: d.SourceMessageID, ExpectedSourceRevision: &d.SourceRevision})
	if protocol.CodeOf(err) == protocol.CodeStaleRevision || protocol.CodeOf(err) == protocol.CodeInvalidTransition {
		return true, nil
	}
	return false, err
}

func deadlineHasUnknownOccurrence(ctx context.Context, q store.Querier, id string) (bool, error) {
	return ownedReminderHasUnknownOccurrence(ctx, q, "deadline", id)
}
func ownedReminderHasUnknownOccurrence(ctx context.Context, q store.Querier, kind, id string) (bool, error) {
	rules, err := store.FollowupIDs(ctx, q, `SELECT rule_id FROM followup_targets WHERE kind=? AND target_id=?`, kind, id)
	if err != nil {
		return false, err
	}
	for _, rule := range rules {
		for after := ""; ; {
			occurrences, more, err := store.FollowupOccurrences(ctx, q, rule, after)
			if err != nil {
				return false, err
			}
			for _, o := range occurrences {
				if o.Status == "unknown" {
					return true, nil
				}
			}
			if !more {
				break
			}
			after = occurrences[len(occurrences)-1].OccurrenceID
		}
	}
	return false, nil
}

// cancelExpiredDeadlineNear runs in the claim transaction, before a pending
// batch is frozen. The shared cancellation path preserves claimed input and
// unrelated sources in the same batch.
func cancelExpiredDeadlineNear(ctx context.Context, x *Exec, tx *store.Tx, payload string) (bool, error) {
	var p HandleMessage
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return false, err
	}
	rules, err := store.FollowupIDs(ctx, tx.Q(), `SELECT DISTINCT s.rule_id FROM background_batches b JOIN background_sources s ON s.batch_id=b.batch_id JOIN followup_targets t ON t.rule_id=s.rule_id AND t.kind='deadline' JOIN issue_deadlines d ON d.deadline_id=t.target_id WHERE b.message_id=? AND b.status='pending' AND s.cancelled_at=0 AND d.status='active' AND s.rule_id=json_extract(d.body_json,'$.near_rule_id') AND json_extract(d.body_json,'$.due_at')<=?`, p.MessageID, x.Now.UnixMilli())
	if err != nil {
		return false, err
	}
	for _, id := range rules {
		if err := cancelPendingFollowups(ctx, x, tx, id); err != nil {
			return false, err
		}
	}
	return len(rules) > 0, nil
}
