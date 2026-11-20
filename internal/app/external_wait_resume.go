package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

// ResumeExternalWait requests another judgment in the original Task. Physical
// stop, Issue responsibility and ordinary scheduling controls remain required.
type ResumeExternalWait struct{}

func (ResumeExternalWait) Operation() string { return protocol.OpTasksResumeWait }
func (ResumeExternalWait) Authorize(ctx context.Context, x *Exec, q store.Querier) error {
	if err := allowKinds(x.Principal, planners...); err != nil {
		return err
	}
	task, err := store.GetTask(ctx, q, x.Env.Target.ID)
	if err != nil {
		return err
	}
	if task.Kind == protocol.TaskKindIssueManager && x.Principal.Kind != protocol.PrincipalOwner && x.Principal.Kind != protocol.PrincipalSystem {
		return protocol.Errorf(protocol.CodeForbidden, "Issue owner wait resumption requires its admitted coordination scope")
	}
	return canonicalTaskPlannerAccess(ctx, q, x)
}
func (ResumeExternalWait) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	if x.Env.Target.Type != "task" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "external wait resumption requires a Task target")
	}
	var p protocol.ResumeExternalWaitPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if strings.TrimSpace(p.WaitID) == "" || strings.TrimSpace(p.Reason) == "" || !utf8.ValidString(p.WaitID+p.Reason) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "original wait_id and resumption reason are required")
	}
	expected, err := requireRevision(x.Env)
	if err != nil {
		return Outcome{}, err
	}
	task, err := store.GetTask(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, err
	}
	if task.ControlRevision != expected {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "Task control revision changed")
	}
	var runID string
	if err := tx.Q().QueryRowContext(ctx, `SELECT run_id FROM run_external_waits WHERE task_id=? AND wait_id=?`, task.TaskID, p.WaitID).Scan(&runID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Outcome{}, protocol.Errorf(protocol.CodeNotFound, "external wait does not belong to this Task")
		}
		return Outcome{}, err
	}
	v, err := store.GetExternalWait(ctx, tx.Q(), runID)
	if err != nil {
		return Outcome{}, err
	}
	if v.WaitKey == reportFollowOnWaitKey {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "answer the original report decision to continue this Issue")
	}
	if p.RestoreNotice {
		fact, err := views.OwnerCIContinuation(ctx, tx.Q(), task.TaskID, runID)
		if err != nil {
			return Outcome{}, err
		}
		if fact.State == "notified" || fact.State == "processed" {
			return Outcome{Result: fact}, nil
		}
		if fact.State != "missing" {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "original CI source is not eligible for recovery")
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return Outcome{}, err
		}
		if err = appendBackgroundSource(ctx, x, tx, "work_item:"+fact.IssueID, fact.IssueID, fact.SourceMessage, fact.SourceEvent, "task.external_wait_ready", raw); err != nil {
			return Outcome{}, err
		}
		v.NoticeRestored = true
		if err := tx.PutExternalWait(ctx, *v, x.Now.UnixMilli()); err != nil {
			return Outcome{}, err
		}
		fact, err = views.OwnerCIContinuation(ctx, tx.Q(), task.TaskID, runID)
		if err != nil {
			return Outcome{}, err
		}
		if fact.State != "notified" {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "original owner notice was not accepted")
		}
		return Outcome{Result: fact}, nil
	}
	if v.Status != "stopping" && v.Status != "waiting" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "external wait already settled")
	}
	current, err := store.RunIssueResponsibilityCurrent(ctx, tx.Q(), runID)
	if err != nil {
		return Outcome{}, err
	}
	if !current || task.Lifecycle.Terminal() {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "the original Task responsibility is no longer current")
	}
	v.ResumeReason = p.Reason
	if err := tx.PutExternalWait(ctx, *v, x.Now.UnixMilli()); err != nil {
		return Outcome{}, err
	}
	if err := refreshExternalWaits(ctx, x, tx); err != nil {
		return Outcome{}, err
	}
	v, err = store.GetExternalWait(ctx, tx.Q(), runID)
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: v, Events: []Event{{ObjectType: "task", ObjectID: task.TaskID, Kind: "task.external_wait_resume_requested", Body: map[string]any{"wait_id": v.WaitID, "reason": v.ResumeReason}}}}, nil
}
