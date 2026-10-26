package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func evaluateFollowup(ctx context.Context, x *Exec, tx *store.Tx, r *protocol.FollowupRule, w protocol.WorkItem, manual bool) (bool, error) {
	if !manual && r.Status == "cancelled" {
		return false, nil
	}
	now := x.Now.UnixMilli()
	old := r.Revision
	save := func(reason string) (bool, error) {
		r.Revision++
		r.UpdatedAt = now
		r.Reason = reason
		return true, tx.SaveFollowup(ctx, *r, old, x.Env.RequestID)
	}
	if w.Status == "closed" {
		r.Status = "cancelled"
		if err := cancelPendingFollowups(ctx, x, tx, r.RuleID); err != nil {
			return false, err
		}
		return save("work item closed")
	}
	satisfied, invalid, evidence, err := followupFacts(ctx, tx.Q(), *r, w)
	if err != nil {
		return false, err
	}
	if invalid != "" {
		r.Status = "paused"
		if err := cancelPendingFollowups(ctx, x, tx, r.RuleID); err != nil {
			return false, err
		}
		return save("needs review: " + invalid)
	}
	if !manual && r.Status != "active" {
		return false, nil
	}
	reason, silent := domain.FollowupDue(*r, string(w.Status), satisfied, now)
	if manual {
		reason = "manual"
		silent = false
	}
	if reason == "" {
		// Snoozed rules retain their planned time, but avoid an overdue hot loop.
		if w.Status == "snoozed" && (r.NextAt <= now || (r.ExpiresAt > 0 && r.ExpiresAt <= now)) {
			_, err := tx.Exec(ctx, `UPDATE followup_rules SET next_due=? WHERE rule_id=?`, now+30000, r.RuleID)
			return true, err
		}
		return false, nil
	}
	if silent {
		r.Status = "finished"
		return save(reason + " (silent)")
	}
	// An unresolved occurrence is not a retry channel. Inspect all pages because
	// old unknown operations can outlive later manual checks or rule revisions.
	for after := ""; ; {
		occurrences, more, e := store.FollowupOccurrences(ctx, tx.Q(), r.RuleID, after)
		if e != nil {
			return false, e
		}
		for _, o := range occurrences {
			if o.Status == "unknown" {
				r.Status = "paused"
				return save("previous followup outcome is unknown; reconcile before resuming")
			}
			if o.Status == "queued" || o.Status == "starting" || o.Status == "processing" {
				if manual {
					return false, protocol.Errorf(protocol.CodeInvalidTransition, "a followup check is already pending or running")
				}
				if r.Kind == "every" {
					r.NextAt = now + r.EverySeconds*1000
					return save("coalesced into existing occurrence")
				}
				_, err := tx.Exec(ctx, `UPDATE followup_rules SET next_due=? WHERE rule_id=?`, now+30000, r.RuleID)
				return true, err
			}
		}
		if !more {
			break
		}
		after = occurrences[len(occurrences)-1].OccurrenceID
	}
	key := fmt.Sprintf("revision:%d", r.Revision)
	if r.Kind == "every" {
		key = fmt.Sprintf("at:%d", r.NextAt)
		if !manual {
			var exists bool
			if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM followup_occurrences WHERE rule_id=? AND occurrence_key=?)`, r.RuleID, key).Scan(&exists); err != nil {
				return false, err
			}
			if exists {
				r.NextAt = now + r.EverySeconds*1000
				return save("coalesced previously recorded planned slot after clock change")
			}
		}
	}
	if manual {
		key = "manual:" + x.Env.RequestID
	}
	message := x.IDs.New(protocol.PrefixMessage)
	body := fmt.Sprintf("工作项跟进检查：%s\n\n规则：%s（revision %d）\n原因：%s\n\n%s\n\n行动前重新读取工作项与规则；本次唤醒不扩大授权，也不证明业务责任已经完成。", r.Purpose, r.RuleID, r.Revision, reason, r.Instruction)
	seq, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: message, Author: "system", Body: body, CreatedAt: now})
	if err != nil {
		return false, err
	}
	o := protocol.FollowupOccurrence{OccurrenceID: x.IDs.New(protocol.Prefix("occurrence")), RuleID: r.RuleID, Revision: r.Revision, WorkItemID: r.WorkItemID, MessageID: message, Status: "queued", Reason: reason, Evidence: evidence, CreatedAt: now}
	if err := tx.InsertFollowupOccurrence(ctx, o, key); err != nil {
		return false, err
	}
	for _, b := range []struct{ kind, id string }{{"work_item", r.WorkItemID}, {"followup", r.RuleID}, {"followup_occurrence", o.OccurrenceID}} {
		if err := tx.BindMessage(ctx, message, b.kind, b.id, "notice"); err != nil {
			return false, err
		}
	}
	eventID := x.IDs.New(protocol.PrefixEvent)
	if _, err := tx.InsertEvent(ctx, store.EventRow{EventID: eventID, Source: "app", ObjectType: "message", ObjectID: message, ObjectVersion: seq, Kind: "conversation.message", BodyJSON: `{"author":"system","followup":true}`, RequestID: x.Env.RequestID, TraceID: x.TraceID, CreatedAt: now}); err != nil {
		return false, err
	}
	fact, err := json.Marshal(o)
	if err != nil {
		return false, err
	}
	if err := queueEventTurn(ctx, x, tx, message, eventID, "followup.triggered", fact); err != nil {
		return false, err
	}
	r.TriggerCount++
	if manual && r.TriggerCount >= r.MaxTriggers {
		r.Status = "finished"
	}
	if !manual {
		if r.Kind != "every" || r.TriggerCount >= r.MaxTriggers {
			r.Status = "finished"
		} else {
			r.NextAt = now + r.EverySeconds*1000
		}
	}
	return save(reason)
}

// followupEvents matches indexed subscriptions in the same fact transaction.
func followupEvents(ctx context.Context, x *Exec, tx *store.Tx, events []Event) (bool, error) {
	matched := map[string]bool{}
	for _, event := range events {
		// Work-item updates cover immutable delivery identity and snooze/closure.
		// Delivery observations use the containing work item as their object ID.
		ids, err := store.FollowupIDs(ctx, tx.Q(), `SELECT t.rule_id FROM followup_targets t JOIN followup_rules r ON r.rule_id=t.rule_id WHERE t.kind=? AND t.target_id=? AND r.status<>'cancelled'`, event.ObjectType, event.ObjectID)
		if err != nil {
			return false, err
		}
		for _, id := range ids {
			matched[id] = true
		}
	}
	changed := false
	for id := range matched {
		r, err := store.GetFollowup(ctx, tx.Q(), id)
		if err != nil {
			return false, err
		}
		w, err := store.GetWorkItem(ctx, tx.Q(), r.WorkItemID)
		if err != nil {
			return false, err
		}
		if w.Status == "closed" {
			if err := cancelPendingFollowups(ctx, x, tx, r.RuleID); err != nil {
				return false, err
			}
			old := r.Revision
			r.Revision++
			r.Status = "cancelled"
			r.Reason = "work item closed"
			r.UpdatedAt = x.Now.UnixMilli()
			if err := tx.SaveFollowup(ctx, r, old, x.Env.RequestID); err != nil {
				return false, err
			}
			changed = true
			continue
		}
		if w.Status == "snoozed" && r.Kind != "at" {
			if err := cancelPendingFollowups(ctx, x, tx, r.RuleID); err != nil {
				return false, err
			}
		}
		if !x.FollowupsEnabled {
			continue
		}
		yes, err := evaluateFollowup(ctx, x, tx, &r, w, false)
		if err != nil {
			return false, err
		}
		changed = changed || yes
	}
	return changed, nil
}

type SweepFollowups struct{}

func (SweepFollowups) Operation() string { return "followups.sweep" }
func (SweepFollowups) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalSystem)
}
func (SweepFollowups) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	if !x.FollowupsEnabled {
		return Outcome{NoEffect: true}, nil
	}
	ids, err := store.FollowupIDs(ctx, tx.Q(), `SELECT rule_id FROM followup_rules WHERE status='active' AND next_due<=? ORDER BY next_due,rule_id LIMIT 100`, x.Now.UnixMilli())
	if err != nil {
		return Outcome{}, err
	}
	out := Outcome{Notify: true}
	for _, id := range ids {
		r, err := store.GetFollowup(ctx, tx.Q(), id)
		if err != nil {
			return Outcome{}, err
		}
		w, err := store.GetWorkItem(ctx, tx.Q(), r.WorkItemID)
		if err != nil {
			return Outcome{}, err
		}
		changed, err := evaluateFollowup(ctx, x, tx, &r, w, false)
		if err != nil {
			return Outcome{}, err
		}
		if changed {
			out.Events = append(out.Events, followupEvent(r))
		}
	}
	if len(out.Events) == 0 {
		return Outcome{NoEffect: true}, nil
	}
	out.Result = map[string]int{"evaluated": len(out.Events)}
	return out, nil
}
func (a *App) FollowupsEnabled() bool   { return a.followupsEnabled }
func WithFollowups(enabled bool) Option { return func(a *App) { a.followupsEnabled = enabled } }

// RunFollowups polls only indexed ledger facts. It never starts a worker or model.
func (a *App) RunFollowups(ctx context.Context, log *slog.Logger) {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		cmd := SweepFollowups{}
		env := protocol.Envelope{RequestID: a.ids.New(protocol.PrefixRequest), Operation: cmd.Operation(), Target: protocol.Target{Type: "followups"}, Payload: json.RawMessage(`{}`)}
		op, err := a.Execute(ctx, protocol.Principal{Kind: protocol.PrincipalSystem}, &env, cmd)
		if err != nil && ctx.Err() == nil {
			log.WarnContext(ctx, "followup sweep failed", "request_id", env.RequestID, "err", err)
		} else if op.Error != nil {
			log.WarnContext(ctx, "followup sweep rejected", "request_id", env.RequestID, "code", op.Error.Code)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
