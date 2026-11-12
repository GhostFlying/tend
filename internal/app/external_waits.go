package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

const externalWaitReminder = 20 * time.Minute

// WaitExternal records intent; only YieldExternalWait proves physical exit.
type WaitExternal struct{}

func (WaitExternal) Operation() string { return protocol.OpRunsWaitExternal }
func (WaitExternal) Authorize(ctx context.Context, x *Exec, q store.Querier) error {
	if x.Principal.Kind != protocol.PrincipalRun || x.Principal.Subject != x.Env.Target.ID {
		return protocol.Errorf(protocol.CodeForbidden, "only the execution may yield for an external condition")
	}
	rc, err := loadRun(ctx, q, x.Env.Target.ID)
	if err != nil {
		return err
	}
	if v, e := store.GetExternalWait(ctx, q, rc.run.RunID); e != nil {
		return e
	} else if v != nil && v.WaitID == x.Env.RequestID {
		return nil
	}
	if rc.run.Role != x.Principal.Role || desiredFor(rc.run, rc.action, rc.task) != protocol.DesiredRun {
		return protocol.Errorf(protocol.CodeInvalidTransition, "external wait requires the current execution")
	}
	if rc.run.Protocol < 3 {
		return protocol.Errorf(protocol.CodeInvalidRequest, "external wait requires runner protocol 3")
	}
	return nil
}
func (WaitExternal) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.WaitExternalPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if strings.TrimSpace(p.WaitKey) == "" || len(p.WaitKey) > 128 || strings.TrimSpace(p.Reason) == "" || strings.TrimSpace(p.Recovery) == "" || p.TargetID == "" || !utf8.ValidString(p.WaitKey+p.Reason+p.Recovery+p.TargetID+p.IssueID) || p.DeadlineAtMillis < 0 {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "wait_key, target, reason and recovery are required")
	}
	run, err := store.GetRun(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, err
	}
	prior, err := store.GetExternalWait(ctx, tx.Q(), run.RunID)
	if err != nil {
		return Outcome{}, err
	}
	if prior != nil {
		if prior.WaitExternalPayload != p {
			return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "this execution already has a different external wait")
		}
		return Outcome{Result: prior}, nil
	}
	var questionHandoff bool
	if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM run_question_handoffs WHERE run_id=?)`, run.RunID).Scan(&questionHandoff); err != nil {
		return Outcome{}, err
	}
	if questionHandoff {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "the existing question handoff owns this execution continuation")
	}
	if err := externalWaitScope(ctx, tx.Q(), run, p); err != nil {
		return Outcome{}, err
	}
	v := protocol.ExternalWait{WaitExternalPayload: p, WaitID: x.Env.RequestID, RunID: run.RunID, TaskID: run.TaskID, Status: "stopping", CreatedAtMillis: x.Now.UnixMilli(), NextReminderAtMillis: x.Now.Add(externalWaitReminder).UnixMilli()}
	if run.Role == protocol.RoleIssueManager {
		// This records the observed event head, not owner input consumption.
		if err := tx.Q().QueryRowContext(ctx, `SELECT COALESCE(MAX(event_seq),0) FROM events`).Scan(&v.InputEventWatermark); err != nil {
			return Outcome{}, err
		}
	}
	if p.DeadlineAtMillis > 0 {
		v.NextReminderAtMillis = p.DeadlineAtMillis
	}
	_, _, err = observeExternalWait(ctx, tx.Q(), &v)
	if err != nil {
		return Outcome{}, err
	}
	if err := tx.PutExternalWait(ctx, v, x.Now.UnixMilli()); err != nil {
		return Outcome{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE runs SET desired_state='paused' WHERE run_id=?`, run.RunID); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: v, Events: []Event{{ObjectType: "task", ObjectID: run.TaskID, Kind: "run.external_wait_requested", Body: map[string]any{"wait": v}}}}, nil
}

func externalWaitScope(ctx context.Context, q store.Querier, run store.RunRow, p protocol.WaitExternalPayload) error {
	responsibilities, err := store.TaskIssueResponsibilities(ctx, q, run.TaskID)
	if err != nil {
		return err
	}
	allowed := map[string]bool{}
	for _, i := range responsibilities {
		allowed[i.WorkItemID] = true
	}
	if run.Role == protocol.RoleIssueManager {
		m, err := store.GetIssueManagerTask(ctx, q, run.TaskID)
		if err != nil {
			return err
		}
		allowed[m.IssueID] = true
	}
	allowedTask := func(task string) error {
		if task == run.TaskID {
			return nil
		}
		is, err := store.TaskIssueResponsibilities(ctx, q, task)
		if err != nil {
			return err
		}
		for _, i := range is {
			if allowed[i.WorkItemID] {
				return nil
			}
		}
		return protocol.Errorf(protocol.CodeForbidden, "wait target is outside this Issue")
	}
	switch p.Kind {
	case "checks":
		if !allowed[p.IssueID] {
			return protocol.Errorf(protocol.CodeForbidden, "checks require an associated Issue")
		}
		_, err := store.GetDeliveryObservation(ctx, q, p.IssueID, p.TargetID)
		return err
	case "review":
		var linked bool
		err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM shared_delivery_review_links WHERE review_id=? AND issue_id IN (SELECT work_item_id FROM task_work_item_owners WHERE task_id=? UNION SELECT issue_id FROM issue_manager_tasks WHERE task_id=?))`, p.TargetID, run.TaskID, run.TaskID).Scan(&linked)
		if err != nil {
			return err
		}
		if !linked {
			return protocol.Errorf(protocol.CodeForbidden, "review is outside this Issue")
		}
		return nil
	case "answer":
		d, err := store.GetDecision(ctx, q, p.TargetID)
		if err != nil {
			return err
		}
		return allowedTask(d.TaskID)
	case "task":
		if p.TargetID == run.TaskID {
			return protocol.Errorf(protocol.CodeInvalidRequest, "execution cannot wait for its own Task")
		}
		return allowedTask(p.TargetID)
	case "upgrade":
		_, err := store.GetUpgrade(ctx, q, p.TargetID)
		if err != nil {
			return err
		}
		if run.Role == protocol.RoleIssueManager {
			return nil
		}
		action, err := store.GetAction(ctx, q, run.ActionID)
		if err != nil {
			return err
		}
		body, err := store.GetSpec(ctx, q, run.TaskID, action.TargetSpecVersion)
		if err != nil {
			return err
		}
		var spec domain.Spec
		if err := json.Unmarshal([]byte(body), &spec); err != nil {
			return err
		}
		if spec.Observation == nil || !spec.Observation.Deployment {
			return protocol.Errorf(protocol.CodeForbidden, "Task does not observe deployment")
		}
		return nil
	default:
		return protocol.Errorf(protocol.CodeInvalidRequest, "wait kind must be checks, review, answer, task or upgrade")
	}
}

// observeExternalWait returns completion, state and durable provider facts. A
// failure wakes the owner to decide, never restarts the external operation.
func observeExternalWait(ctx context.Context, q store.Querier, v *protocol.ExternalWait) (bool, string, error) {
	switch v.Kind {
	case "checks":
		row, err := store.GetDeliveryObservation(ctx, q, v.IssueID, v.TargetID)
		if err != nil {
			return false, "", err
		}
		o := row.Observation
		oldHead := v.ObservedHead
		v.ObservedRevision = row.FactRevision
		v.ObservedHead = o.HeadSHA
		v.ObservedState = o.State
		if oldHead != "" && o.HeadSHA != "" && oldHead != o.HeadSHA {
			return true, "candidate_changed", nil
		}
		if o.State == "closed" || o.State == "merged" {
			return true, "pull_request_" + o.State, nil
		}
		if o.CI != nil && o.CI.HeadSHA == o.HeadSHA && !o.RefreshPending && (o.CI.State == "success" || o.CI.State == "failure" || o.CI.State == "cancelled" || o.CI.State == "skipped") {
			v.ObservedState = o.CI.State
			return true, "checks_" + o.CI.State, nil
		}
	case "review":
		var raw string
		if err := q.QueryRowContext(ctx, `SELECT body_json FROM shared_delivery_reviews WHERE review_id=?`, v.TargetID).Scan(&raw); err != nil {
			return false, "", err
		}
		var r protocol.SharedDeliveryReview
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			return false, "", err
		}
		v.ObservedRevision = r.Revision
		v.ObservedState = r.Status
		if len(r.Rounds) > 0 {
			last := r.Rounds[len(r.Rounds)-1]
			if last.Result != nil {
				return true, "review_result", nil
			}
			// Recording the formal verdict is the owner's next useful action.
			// Its availability cannot depend on that same owner already waking
			// and recording it while this external wait holds ordinary notices.
			taskID, version, generation := last.TaskID, last.SpecVersion, last.ImplementGeneration
			if last.SupplementTaskID != "" {
				taskID, version, generation = last.SupplementTaskID, last.SupplementSpecVersion, last.SupplementGeneration
			}
			if taskID != "" {
				task, err := store.GetTask(ctx, q, taskID)
				if err != nil {
					return false, "", err
				}
				if task.SpecVersion == version && (generation == 0 || task.ImplementGeneration == generation) && task.Lifecycle.Terminal() {
					return true, "review_task_" + string(task.Lifecycle), nil
				}
			}
		}
	case "answer":
		d, err := store.GetDecision(ctx, q, v.TargetID)
		if err != nil {
			return false, "", err
		}
		v.ObservedState = d.Status
		v.ObservedRevision = d.Version
		return d.Status != store.DecisionOpen, "answer_" + d.Status, nil
	case "task":
		t, err := store.GetTask(ctx, q, v.TargetID)
		if err != nil {
			return false, "", err
		}
		v.ObservedState = string(t.Lifecycle)
		v.ObservedRevision = t.ControlRevision
		return t.Lifecycle.Terminal(), "task_" + string(t.Lifecycle), nil
	case "upgrade":
		u, err := store.GetUpgrade(ctx, q, v.TargetID)
		if err != nil {
			return false, "", err
		}
		v.ObservedRevision = u.View.Revision
		v.ObservedState = u.View.Status
		if v.DeadlineAtMillis == 0 && u.View.OverallDeadlineMillis > 0 {
			v.DeadlineAtMillis = u.View.OverallDeadlineMillis
			v.NextReminderAtMillis = v.DeadlineAtMillis
		}
		return u.View.Status == "completed" || u.View.Status == "failed" || u.View.Status == "cancelled" || u.View.Status == "attention", "upgrade_" + u.View.Status, nil
	}
	return false, "", nil
}

type YieldExternalWait struct{}

func (YieldExternalWait) Operation() string { return protocol.OpRunsYieldExternalWait }
func (YieldExternalWait) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	if x.Principal.Kind != protocol.PrincipalRun || x.Principal.Subject != x.Env.Target.ID {
		return protocol.Errorf(protocol.CodeForbidden, "only the execution may confirm stop")
	}
	return nil
}
func (YieldExternalWait) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.YieldExternalWaitPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	v, err := store.GetExternalWait(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, err
	}
	if v == nil || v.WaitID != p.WaitID {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "no matching external wait")
	}
	r, err := store.GetRun(ctx, tx.Q(), v.RunID)
	if err != nil {
		return Outcome{}, err
	}
	if r.Role != x.Principal.Role {
		return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "execution role changed")
	}
	return settleExternalWaitStopped(ctx, x, tx, r, v, p.ThreadID)
}

func settleExternalWaitStopped(ctx context.Context, x *Exec, tx *store.Tx, r store.RunRow, v *protocol.ExternalWait, thread string) (Outcome, error) {
	if !v.Stopped {
		if err := endRun(ctx, x, tx, r, protocol.RunFinished); err != nil {
			return Outcome{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE run_pause_handoffs SET stopped=1,thread_id=? WHERE run_id=?`, thread, r.RunID); err != nil {
			return Outcome{}, err
		}
		v.Stopped = true
		v.ThreadID = thread
		v.Status = "waiting"
		if err := tx.PutExternalWait(ctx, *v, x.Now.UnixMilli()); err != nil {
			return Outcome{}, err
		}
	}
	if r.Role == protocol.RoleIssueManager {
		if err := settleStoppedManagerRequests(ctx, tx, r.RunID); err != nil {
			return Outcome{}, err
		}
	}
	if err := refreshExternalWaits(ctx, x, tx); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: v, Events: []Event{{ObjectType: "task", ObjectID: v.TaskID, Kind: "run.external_wait_yielded", Body: map[string]any{"wait": v}}}}, nil
}

func refreshExternalWaits(ctx context.Context, x *Exec, tx *store.Tx) error {
	_, err := refreshExternalWaitsChanged(ctx, x, tx)
	return err
}

func refreshExternalWaitsChanged(ctx context.Context, x *Exec, tx *store.Tx) (bool, error) {
	changed := false
	ids, err := store.FollowupIDs(ctx, tx.Q(), `SELECT run_id FROM run_external_waits WHERE status='waiting' AND next_check_at<=? ORDER BY next_check_at,run_id LIMIT 100`, x.Now.UnixMilli())
	if err != nil {
		return false, err
	}
	for _, id := range ids {
		v, err := store.GetExternalWait(ctx, tx.Q(), id)
		if err != nil {
			return false, err
		}
		if !v.Stopped {
			continue
		}
		r, err := store.GetRun(ctx, tx.Q(), id)
		if err != nil {
			return false, err
		}
		a, err := store.GetAction(ctx, tx.Q(), r.ActionID)
		if err != nil {
			return false, err
		}
		t, err := store.GetTask(ctx, tx.Q(), r.TaskID)
		if err != nil {
			return false, err
		}
		current, err := store.RunIssueResponsibilityCurrent(ctx, tx.Q(), id)
		if err != nil {
			return false, err
		}
		if !current || t.Lifecycle.Terminal() || a.TargetSpecVersion != t.SpecVersion || a.Status != protocol.ActionDispatched || a.Kind == protocol.ActionImplement && a.Generation != t.ImplementGeneration || a.Kind == protocol.ActionAccept && a.TargetArtifactID != t.CurrentArtifactID {
			v.Status = "cancelled"
			changed = true
			if err := tx.PutExternalWait(ctx, *v, x.Now.UnixMilli()); err != nil {
				return false, err
			}
			if a.Status == protocol.ActionDispatched {
				if err := tx.SetActionStatus(ctx, a.ActionID, protocol.ActionDone, x.Now.UnixMilli()); err != nil {
					return false, err
				}
			}
			continue
		}
		if t.ControlMode != protocol.ControlManaged {
			if err := tx.PutExternalWait(ctx, *v, x.Now.Add(5*time.Second).UnixMilli()); err != nil {
				return false, err
			}
			continue
		}
		ready, reason := false, ""
		if v.ResumeReason != "" {
			ready, reason = true, "requested"
		} else if r.Role == protocol.RoleIssueManager {
			ready, err = externalWaitOwnerInputReady(ctx, tx.Q(), r.TaskID)
			if err != nil {
				return false, err
			}
			if ready {
				reason = "owner_input"
			}
		}
		if !ready {
			ready, reason, err = observeExternalWait(ctx, tx.Q(), v)
			if errors.Is(err, store.ErrNotFound) || errors.Is(err, sql.ErrNoRows) {
				ready, reason, err = true, "target_unavailable", nil
			}
			if err != nil {
				return false, err
			}
		}
		now := x.Now.UnixMilli()
		if !ready && now >= v.NextReminderAtMillis {
			ready = true
			reason = "reminder"
			if v.DeadlineAtMillis > 0 && now >= v.DeadlineAtMillis {
				reason = "deadline"
			}
		}
		if !ready {
			if err := tx.PutExternalWait(ctx, *v, now+5000); err != nil {
				return false, err
			}
			continue
		}
		original := a.ActionID
		a.ActionID = x.IDs.New(protocol.PrefixAction)
		a.EffectKey = "external_wait|" + v.WaitID
		a.Status = protocol.ActionPending
		a.CreatedAt = now
		a.CreatedRequestID = x.Env.RequestID
		if err := tx.InsertAction(ctx, a); err != nil {
			return false, err
		}
		v.Status = "continued"
		changed = true
		v.WakeReason = reason
		if err := tx.PutExternalWait(ctx, *v, now); err != nil {
			return false, err
		}
		if _, err := tx.Exec(ctx, `UPDATE run_external_waits SET continuation_action_id=? WHERE run_id=?`, a.ActionID, id); err != nil {
			return false, err
		}
		if err := tx.SetActionStatus(ctx, original, protocol.ActionDone, now); err != nil {
			return false, err
		}
		if err := recordExternalWaitWake(ctx, x, tx, r, *v); err != nil {
			return false, err
		}
	}
	return changed, nil
}

func externalWaitOwnerInputReady(ctx context.Context, q store.Querier, task string) (bool, error) {
	m, err := store.GetIssueManagerTask(ctx, q, task)
	if err != nil {
		return false, err
	}
	var ready bool
	// A queued owner input can predate this wait. Inspect its actual admission
	// facts instead of treating the registration event head as a consumed cursor.
	// Uncertain native work retains its existing receipt fence, not a new send.
	err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM outbox o
 JOIN conversation_messages input ON input.message_id=json_extract(o.payload_json,'$.message_id')
 JOIN owner_authority_messages owner_input ON owner_input.message_id=input.message_id
 LEFT JOIN conversation_message_controls controls USING(message_id)
 WHERE o.status='pending' AND o.operation='main_thread.handle_message'
 AND (o.destination='main_thread' OR o.destination LIKE 'background_thread:%')
 AND COALESCE(controls.withdrawn_at,0)=0
 AND (input.conversation_id=? OR EXISTS(SELECT 1 FROM message_work_items linked WHERE linked.message_id=input.message_id AND linked.work_item_id=? AND linked.active=1))
 AND NOT EXISTS(SELECT 1 FROM main_turns t WHERE t.request_id=o.request_id AND t.status<>'rejected')
 AND NOT EXISTS(SELECT 1 FROM native_turn_receipts_pending p WHERE p.request_id=o.request_id)
 AND NOT EXISTS(SELECT 1 FROM main_steers s WHERE s.request_id=o.request_id AND (s.native_item_id<>'' OR s.status<>'waiting')))`, m.ConversationID, m.IssueID).Scan(&ready)
	return ready, err
}

func (a *App) ExternalWaits(ctx context.Context, task string) ([]protocol.ExternalWait, error) {
	var out []protocol.ExternalWait
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		out, err = store.TaskExternalWaits(ctx, tx.Q(), task)
		return err
	})
	return out, err
}

// SweepExternalWaits shares the existing server maintenance clock. It observes
// ledger facts only, creates no provider polling or extra scheduler goroutine.
func (a *App) SweepExternalWaits(ctx context.Context) error {
	now := a.now()
	var due bool
	if err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM run_external_waits WHERE status='waiting' AND next_check_at<=?)`, now.UnixMilli()).Scan(&due)
	}); err != nil || !due {
		return err
	}
	changed := false
	err := a.write(ctx, func(tx *store.Tx) error {
		var exists bool
		if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM run_external_waits WHERE status='waiting')`).Scan(&exists); err != nil || !exists {
			return err
		}
		x := &Exec{Env: &protocol.Envelope{RequestID: a.ids.New(protocol.PrefixRequest)}, Now: now, IDs: a.ids}
		var err error
		changed, err = refreshExternalWaitsChanged(ctx, x, tx)
		return err
	})
	if err == nil && changed {
		a.notifyChange()
		if a.notifier != nil {
			a.notifier.Notify()
		}
	}
	return err
}

// An identical committed terminal receipt remains replayable after lease release.
func (a *App) ExternalWaitReplayCredential(ctx context.Context, run string, env *protocol.Envelope) (leaseID, hostID string, role protocol.RunRole, err error) {
	if (env.Operation != protocol.OpRunsYieldExternalWait && env.Operation != protocol.OpRunsRecordSessionStop) || env.Target.Type != "run" || env.Target.ID != run {
		return "", "", "", store.ErrNotFound
	}
	err = a.store.Read(ctx, func(tx *store.ReadTx) error {
		if !committedRunReceipt(ctx, tx.Q(), run, env) {
			return store.ErrNotFound
		}

		r, e := store.GetRun(ctx, tx.Q(), run)
		if e != nil {
			return e
		}

		if env.Operation == protocol.OpRunsYieldExternalWait {
			v, e := store.GetExternalWait(ctx, tx.Q(), run)
			if e != nil {
				return e
			}
			if v == nil || !v.Stopped {
				return store.ErrNotFound
			}
		}
		lease, e := store.GetLeaseByRun(ctx, tx.Q(), run)
		if e != nil {
			return e
		}
		leaseID, hostID, role = lease.LeaseID, r.HostID, r.Role
		return nil
	})
	return
}

// RecordSessionStop retains a reusable history locator after actual process
// closure. It never ends a Run or changes an unresolved business operation.
type RecordSessionStop struct{}

func (RecordSessionStop) Operation() string { return protocol.OpRunsRecordSessionStop }
func (RecordSessionStop) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	if x.Principal.Kind != protocol.PrincipalRun || x.Principal.Subject != x.Env.Target.ID {
		return protocol.Errorf(protocol.CodeForbidden, "only the execution records its close")
	}
	return nil
}
func (RecordSessionStop) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.SessionStopPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	r, err := store.GetRun(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, err
	}
	if r.Protocol < 3 || r.Role != x.Principal.Role || strings.TrimSpace(p.ThreadID) == "" || !utf8.ValidString(p.ThreadID) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "current protocol and original thread identity are required")
	}
	var prior string
	err = tx.Q().QueryRowContext(ctx, `SELECT thread_id FROM run_session_stops WHERE run_id=?`, r.RunID).Scan(&prior)
	if err == nil {
		if prior != p.ThreadID {
			return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "physical session close identity changed")
		}
		return Outcome{Result: p}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Outcome{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO run_session_stops(run_id,thread_id,stopped_at) VALUES(?,?,?)`, r.RunID, p.ThreadID, x.Now.UnixMilli()); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: p, Events: []Event{{ObjectType: "run", ObjectID: r.RunID, Kind: "run.session_stopped", Body: map[string]any{"recorded": true}}}}, nil
}

func recordExternalWaitWake(ctx context.Context, x *Exec, tx *store.Tx, r store.RunRow, v protocol.ExternalWait) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	event := x.IDs.New(protocol.PrefixEvent)
	if _, err := tx.InsertEvent(ctx, store.EventRow{EventID: event, Source: "app", ObjectType: "task", ObjectID: r.TaskID, Kind: "task.external_wait_ready", BodyJSON: string(raw), CreatedAt: x.Now.UnixMilli()}); err != nil {
		return err
	}
	if r.Role != protocol.RoleIssueManager {
		return nil
	}
	m, err := store.GetIssueManagerTask(ctx, tx.Q(), r.TaskID)
	if err != nil {
		return err
	}
	message := x.IDs.New(protocol.PrefixMessage)
	body := "External wait returned to this Issue owner: " + v.Kind + " " + v.TargetID + " (" + v.WakeReason + "). Original Task: " + v.TaskID + "; wait: " + v.WaitID + ". Recovery: " + v.Recovery + ". Read current facts, decide the next useful action, and preserve unknown outcomes. No external operation was repeated and no authorization was granted."
	if v.ResumeReason != "" {
		body += " Resume reason: " + v.ResumeReason
	}
	if _, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: message, ConversationID: m.ConversationID, Author: "system", Body: body, CreatedAt: x.Now.UnixMilli()}); err != nil {
		return err
	}
	return appendBackgroundSource(ctx, x, tx, "work_item:"+m.IssueID, m.IssueID, message, event, "task.external_wait_ready", raw)
}
