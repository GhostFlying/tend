package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type FollowupCommand struct{ Action string }

func (c FollowupCommand) Operation() string { return "work_items.followups_" + c.Action }
func (c FollowupCommand) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	if err := allowKinds(x.Principal, planners...); err != nil {
		return err
	}
	if !x.FollowupsEnabled && c.Action != "pause" && c.Action != "cancel" {
		return protocol.Errorf(protocol.CodeInvalidRequest, "active followups require the hosted main agent")
	}
	return nil
}
func (c FollowupCommand) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	now := x.Now.UnixMilli()
	if c.Action == "create" {
		if x.Env.Target.Type != "work_item" {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "followup creation targets a work item")
		}
		w, err := issueForWrite(ctx, x, tx.Q())
		if err != nil {
			return Outcome{}, err
		}
		var p protocol.FollowupSpec
		if err := decodePayload(x.Env, &p); err != nil {
			return Outcome{}, err
		}
		if x.Principal.Kind == protocol.PrincipalOwner {
			p.SourceID = x.Env.RequestID
		}
		// A model may repeat the same planning call with a fresh call/request ID.
		// Preserve the original registration across retries and clock changes.
		identity, _ := json.Marshal(p)
		key := fmt.Sprintf("%x", sha256.Sum256(append([]byte(w.WorkItemID+"\x00"), identity...)))
		ids, err := store.FollowupIDs(ctx, tx.Q(), `SELECT rule_id FROM followup_registrations WHERE identity=?`, key)
		if err != nil {
			return Outcome{}, err
		}
		if len(ids) > 0 {
			existing, err := store.GetFollowup(ctx, tx.Q(), ids[0])
			return Outcome{Result: existing}, err
		}
		p, err = normalizeFollowup(p, now)
		if err != nil {
			return Outcome{}, err
		}
		if w.Status == "closed" {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "closed work item cannot create followups")
		}
		r := protocol.FollowupRule{FollowupSpec: p, RuleID: x.IDs.New(protocol.Prefix("followup")), WorkItemID: w.WorkItemID, Revision: 1, Status: "active", CreatedAt: now, UpdatedAt: now}
		scheduleFollowup(&r, now)
		if err := validateFollowup(ctx, x, tx, r, w); err != nil {
			return Outcome{}, err
		}
		if err := tx.SaveFollowup(ctx, r, 0, x.Env.RequestID); err != nil {
			return Outcome{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO followup_registrations(identity,rule_id) VALUES(?,?)`, key, r.RuleID); err != nil {
			return Outcome{}, err
		}
		if err := bindFollowupTargets(ctx, tx, r); err != nil {
			return Outcome{}, err
		}
		if _, err := evaluateFollowup(ctx, x, tx, &r, w, false); err != nil {
			return Outcome{}, err
		}
		return Outcome{Notify: true, Result: r, Events: []Event{followupEvent(r)}}, nil
	}
	if x.Env.Target.Type != "followup" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "followup control targets a rule")
	}
	r, err := store.GetFollowup(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, errNotFound(err, "followup does not exist")
	}
	if err := store.RejectCanonicalLegacyIssueWrite(ctx, tx.Q(), r.WorkItemID); err != nil {
		return Outcome{}, err
	}
	if x.Env.ExpectedControlRevision == nil || *x.Env.ExpectedControlRevision != r.Revision {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "followup revision changed")
	}
	w, err := store.GetIssue(ctx, tx.Q(), r.WorkItemID)
	if err != nil {
		return Outcome{}, err
	}
	if w.Status == "closed" || r.Status == "cancelled" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "closed or cancelled followup cannot be controlled")
	}
	if strings.TrimSpace(x.Env.Reason) == "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "followup control requires a reason")
	}
	if c.Action == "trigger" {
		if _, err := evaluateFollowup(ctx, x, tx, &r, w, true); err != nil {
			return Outcome{}, err
		}
		return Outcome{Notify: true, Result: r, Events: []Event{followupEvent(r)}}, nil
	}
	old := r.Revision
	r.Revision++
	r.UpdatedAt = now
	r.Reason = x.Env.Reason
	switch c.Action {
	case "update":
		var p protocol.UpdateFollowupPayload
		if err := decodePayload(x.Env, &p); err != nil {
			return Outcome{}, err
		}
		if p.Spec == nil {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "replacement spec is required")
		}
		spec := *p.Spec
		if x.Principal.Kind == protocol.PrincipalOwner {
			spec.SourceID = x.Env.RequestID
		}
		r.FollowupSpec, err = normalizeFollowup(spec, now)
		if err != nil {
			return Outcome{}, err
		}
		if r.Kind == "every" && r.MaxTriggers <= r.TriggerCount {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "new limit must exceed accumulated trigger count")
		}
		if err := validateFollowup(ctx, x, tx, r, w); err != nil {
			return Outcome{}, err
		}
		if r.Status == "finished" {
			r.Status = "active"
		}
		scheduleFollowup(&r, now)
	case "pause":
		if r.Status != "active" {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "only active followups can pause")
		}
		r.Status = "paused"
	case "resume":
		if r.Status != "paused" {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "only paused followups can resume")
		}
		if r.Kind == "at" && r.At <= now {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "set a new time or trigger this overdue rule explicitly")
		}
		if r.ExpiresAt > 0 && r.ExpiresAt <= now {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "expired followup cannot resume")
		}
		r.Status = "active"
		scheduleFollowup(&r, now)
	case "cancel":
		r.Status = "cancelled"
	default:
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "unknown followup action")
	}
	if err := cancelPendingFollowups(ctx, x, tx, r.RuleID); err != nil {
		return Outcome{}, err
	}
	if err := tx.SaveFollowup(ctx, r, old, x.Env.RequestID); err != nil {
		return Outcome{}, err
	}
	if c.Action == "update" {
		if _, err := tx.Exec(ctx, `DELETE FROM followup_targets WHERE rule_id=?`, r.RuleID); err != nil {
			return Outcome{}, err
		}
		if err := bindFollowupTargets(ctx, tx, r); err != nil {
			return Outcome{}, err
		}
	}
	if _, err := evaluateFollowup(ctx, x, tx, &r, w, false); err != nil {
		return Outcome{}, err
	}
	return Outcome{Notify: true, Result: r, Events: []Event{followupEvent(r)}}, nil
}
func scheduleFollowup(r *protocol.FollowupRule, now int64) {
	switch r.Kind {
	case "at":
		r.NextAt = r.At
	case "every":
		r.NextAt = now + r.EverySeconds*1000
	case "condition":
		r.NextAt = r.ExpiresAt
	}
}
func followupEvent(r protocol.FollowupRule) Event {
	return Event{ObjectType: "followup", ObjectID: r.RuleID, ObjectVersion: r.Revision, Kind: "followup.changed", Body: map[string]any{"work_item_id": r.WorkItemID, "status": r.Status}}
}
func validateFollowup(ctx context.Context, x *Exec, tx *store.Tx, r protocol.FollowupRule, w protocol.WorkItem) error {
	if x.Principal.Kind == protocol.PrincipalMainAgent {
		m, err := store.GetMessage(ctx, tx.Q(), r.SourceID)
		if err != nil {
			return errNotFound(err, "followup needs a persisted source message")
		}
		if m.Author != "owner" && m.Author != "system" {
			return protocol.Errorf(protocol.CodeInvalidRequest, "followup source must be owner input or a background notice")
		}
	}
	if r.FollowOnID != "" && !slices.ContainsFunc(w.FollowOns, func(f protocol.WorkItemFollowOn) bool { return f.FollowOnID == r.FollowOnID }) {
		return protocol.Errorf(protocol.CodeInvalidRequest, "follow-on must belong to the work item")
	}
	_, invalid, _, err := followupFacts(ctx, tx.Q(), r, w)
	if err != nil {
		return err
	}
	if invalid != "" {
		return protocol.Errorf(protocol.CodeInvalidRequest, "%s", invalid)
	}
	return nil
}
func bindFollowupTargets(ctx context.Context, tx *store.Tx, r protocol.FollowupRule) error {
	if err := tx.FollowupTarget(ctx, r.RuleID, "work_item", r.WorkItemID); err != nil {
		return err
	}
	c := r.Condition
	if c == nil {
		return nil
	}
	for _, id := range c.TaskIDs {
		if err := tx.FollowupTarget(ctx, r.RuleID, "task", id); err != nil {
			return err
		}
	}
	for _, target := range []struct{ kind, id string }{{"delivery", c.DeliveryID}, {"decision", c.DecisionID}, {"work_item_choice", c.ChoiceID}} {
		if target.id != "" {
			if err := tx.FollowupTarget(ctx, r.RuleID, target.kind, target.id); err != nil {
				return err
			}
		}
	}
	return nil
}
func linkedFollowupTask(ctx context.Context, q store.Querier, item, id string) (bool, error) {
	var linked bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_item_task_links WHERE work_item_id=? AND task_id=?)`, item, id).Scan(&linked)
	return linked, err
}
func followupFacts(ctx context.Context, q store.Querier, r protocol.FollowupRule, w protocol.WorkItem) (satisfied bool, invalid, evidence string, err error) {
	c := r.Condition
	if c == nil {
		return false, "", "", nil
	}
	switch c.Kind {
	case "tasks_completed":
		satisfied = true
		for _, id := range c.TaskIDs {
			linked, e := linkedFollowupTask(ctx, q, w.WorkItemID, id)
			if e != nil {
				return false, "", "", e
			}
			if !linked {
				return false, "target task no longer belongs to work item", "", nil
			}
			t, e := store.GetTask(ctx, q, id)
			if e != nil {
				return false, "", "", e
			}
			satisfied = satisfied && t.Lifecycle == protocol.TaskCompleted
			evidence += fmt.Sprintf("%s: %s (revision %d); ", id, t.Lifecycle, t.ControlRevision)
		}
	case "delivery_state":
		if !slices.ContainsFunc(w.Deliveries, func(d protocol.WorkItemDelivery) bool { return d.DeliveryID == c.DeliveryID }) {
			return false, "target delivery was removed or replaced", "", nil
		}
		o, e := store.GetDeliveryObservation(ctx, q, w.WorkItemID, c.DeliveryID)
		if errors.Is(e, store.ErrNotFound) {
			return false, "", "no observation", nil
		}
		if e != nil {
			return false, "", "", e
		}
		raw, e := json.Marshal(o.Observation)
		if e != nil {
			return false, "", "", e
		}
		evidence = string(raw)
		satisfied = o.Observation.ErrorKind == "" && o.Observation.CheckedAt > 0 && o.Observation.State == c.DeliveryState
	case "decision_answered":
		d, e := store.GetDecision(ctx, q, c.DecisionID)
		if e != nil {
			return false, "", "", errNotFound(e, "decision does not exist")
		}
		linked, e := linkedFollowupTask(ctx, q, w.WorkItemID, d.TaskID)
		if e != nil {
			return false, "", "", e
		}
		if !linked || d.Status == store.DecisionCancelled {
			return false, "decision is cancelled or does not belong to this work item", "", nil
		}
		satisfied = d.Status == store.DecisionAnswered
		evidence = fmt.Sprintf("%s: %s; answer=%s", d.DecisionID, d.Status, d.AnswerJSON)
	case "choice_answered":
		c, e := store.GetWorkItemChoice(ctx, q, c.ChoiceID)
		if e != nil {
			return false, "", "", errNotFound(e, "choice does not exist")
		}
		if c.WorkItemID != w.WorkItemID || c.Status == "withdrawn" || c.Status == "superseded" {
			return false, "choice was withdrawn, replaced or belongs to another work item", "", nil
		}
		satisfied = c.Status == "answered"
		raw, e := json.Marshal(c)
		err = e
		evidence = string(raw)
	}
	return
}
func cancelPendingFollowups(ctx context.Context, x *Exec, tx *store.Tx, id string) error {
	if err := cancelBackgroundFollowups(ctx, x, tx, id); err != nil {
		return err
	}
	ids, err := store.FollowupIDs(ctx, tx.Q(), `SELECT f.message_id FROM followup_occurrences f WHERE f.rule_id=? AND f.status='queued' AND EXISTS(SELECT 1 FROM outbox o WHERE o.destination='main_thread' AND json_extract(o.payload_json,'$.message_id')=f.message_id AND o.status='pending') AND NOT EXISTS(SELECT 1 FROM main_turns t WHERE t.message_id=f.message_id AND t.status<>'rejected')`, id)
	if err != nil {
		return err
	}
	for _, message := range ids {
		if _, err := tx.Exec(ctx, `UPDATE outbox SET status='cancelled' WHERE destination='main_thread' AND json_extract(payload_json,'$.message_id')=? AND status='pending'`, message); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE followup_occurrences SET status='cancelled' WHERE message_id=?`, message); err != nil {
			return err
		}
		if _, err := tx.InsertEvent(ctx, store.EventRow{EventID: x.IDs.New(protocol.PrefixEvent), Source: "app", ObjectType: "message", ObjectID: message, Kind: "conversation.message_state", BodyJSON: `{"state":"interrupted","followup_cancelled":true}`, RequestID: x.Env.RequestID, CreatedAt: x.Now.UnixMilli()}); err != nil {
			return err
		}
	}
	return nil
}

func normalizeFollowup(p protocol.FollowupSpec, now int64) (protocol.FollowupSpec, error) {
	if _, err := time.LoadLocation(p.Timezone); err != nil {
		return p, protocol.Errorf(protocol.CodeInvalidRequest, "unknown IANA timezone")
	}
	return domain.NormalizeFollowup(p, now)
}
