package app

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// YieldQuestion records confirmed process exit without consuming a task attempt.
type YieldQuestion struct{}

func (YieldQuestion) Operation() string { return protocol.OpRunsYieldQuestion }
func (YieldQuestion) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	if x.Principal.Kind != protocol.PrincipalRun || x.Principal.Subject != x.Env.Target.ID {
		return protocol.Errorf(protocol.CodeForbidden, "only the source execution may confirm its handoff")
	}
	return nil
}
func (YieldQuestion) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.YieldQuestionPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	h, err := store.GetQuestionHandoff(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, err
	}
	if h == nil || h.DecisionID != p.DecisionID {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "execution has no matching question handoff")
	}
	rc, err := loadRun(ctx, tx.Q(), h.RunID)
	if err != nil {
		return Outcome{}, err
	}
	if x.Principal.Role != rc.run.Role {
		return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "execution role changed")
	}
	if err := endRun(ctx, x, tx, rc.run, protocol.RunFinished); err != nil {
		return Outcome{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE run_question_handoffs SET stopped=1,thread_id=? WHERE run_id=?`, p.ThreadID, h.RunID); err != nil {
		return Outcome{}, err
	}
	if err := resumeAnsweredQuestion(ctx, x, tx, h.DecisionID); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: map[string]any{"run_id": h.RunID, "decision_id": h.DecisionID, "observed": protocol.RunFinished}, Events: []Event{{ObjectType: "run", ObjectID: h.RunID, Kind: "run.question_yielded", Body: map[string]any{"decision_id": h.DecisionID}}}}, nil
}

// Both answer and stopped receipt call this inside their existing transaction.
// Whichever arrives second makes exactly the original action claimable.
func resumeAnsweredQuestion(ctx context.Context, x *Exec, tx *store.Tx, decision string) error {
	var run string
	ids, err := store.FollowupIDs(ctx, tx.Q(), `SELECT h.run_id FROM run_question_handoffs h JOIN decisions d USING(decision_id) WHERE h.decision_id=? AND h.stopped=1 AND h.successor_run_id IS NULL AND h.continuation_action_id IS NULL AND d.status='answered'`, decision)
	if err != nil || len(ids) == 0 {
		return err
	}
	run = ids[0]
	policy, err := store.GetDecisionContinuation(ctx, tx.Q(), decision)
	if err != nil || policy != nil && policy.State != "ready" {
		return err
	}
	if policy != nil && policy.Policy.Mode == protocol.DecisionDependency {
		ready, _, err := store.DecisionDependencyReady(ctx, tx.Q(), policy.Policy.Dependency)
		if err != nil || !ready {
			return err
		}
	}
	valid, err := store.QuestionHandoffCurrent(ctx, tx.Q(), run)
	if err != nil || !valid {
		return err
	}
	r, err := store.GetRun(ctx, tx.Q(), run)
	if err != nil {
		return err
	}
	a, err := store.GetAction(ctx, tx.Q(), r.ActionID)
	if err != nil {
		return err
	}
	original := a.ActionID
	a.ActionID = x.IDs.New(protocol.PrefixAction)
	a.EffectKey = "question_continuation|" + decision
	a.Status = protocol.ActionPending
	a.CreatedRequestID = x.Env.RequestID
	a.CreatedAt = x.Now.UnixMilli()
	if err := tx.InsertAction(ctx, a); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE run_question_handoffs SET continuation_action_id=? WHERE run_id=?`, a.ActionID, r.RunID); err != nil {
		return err
	}
	if policy != nil {
		policy.State, policy.UpdatedAt = "continued", x.Now.UnixMilli()
		policy.Revision++
		if err := tx.PutDecisionContinuation(ctx, *policy); err != nil {
			return err
		}
	}
	return tx.SetActionStatus(ctx, original, protocol.ActionDone, x.Now.UnixMilli())
}

// NativeQuestionPredecessor supplies a transport binding from committed ledger
// identity only. The native endpoint separately rechecks the new run's lease.
func (a *App) NativeQuestionPredecessor(ctx context.Context, run string) (string, error) {
	var parent string
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		ids, err := store.FollowupIDs(ctx, tx.Q(), `SELECT h.run_id FROM run_question_handoffs h JOIN runs old ON old.run_id=h.run_id JOIN runs next ON next.run_id=h.successor_run_id JOIN decisions d USING(decision_id) WHERE h.successor_run_id=? AND h.stopped=1 AND old.observed_state='finished' AND d.status='answered' AND next.role=old.role AND next.role='implement' AND next.host_id=old.host_id AND next.generation=old.generation AND next.task_id=old.task_id`, run)
		if err != nil || len(ids) == 0 {
			return err
		}
		old, err := store.GetRunExecution(ctx, tx.Q(), ids[0])
		if err != nil {
			return err
		}
		next, err := store.GetRunExecution(ctx, tx.Q(), run)
		if err != nil {
			return err
		}
		if old == nil || next == nil || *old != *next {
			return protocol.Errorf(protocol.CodeForbidden, "question successor execution identity changed")
		}
		parent = ids[0]
		return nil
	})
	if err == nil && parent == "" {
		return a.nativePausePredecessor(ctx, run)
	}

	return parent, err
}

// QuestionHandoffReplayCredential authenticates only an identical successful
// yield request after its transaction released the original lease. It grants no
// new command and does not require the predecessor to remain current.
func (a *App) QuestionHandoffReplayCredential(ctx context.Context, run string, env *protocol.Envelope) (leaseID, hostID string, role protocol.RunRole, err error) {
	if env.Operation != protocol.OpRunsYieldQuestion || env.Target.Type != "run" || env.Target.ID != run {
		return "", "", "", store.ErrNotFound
	}
	hash, err := protocol.PayloadHash(env)
	if err != nil {
		return "", "", "", store.ErrNotFound
	}
	err = a.store.Read(ctx, func(tx *store.ReadTx) error {
		r, err := store.GetRun(ctx, tx.Q(), run)
		if err != nil {
			return err
		}
		p := protocol.Principal{Kind: protocol.PrincipalRun, Subject: run, Role: r.Role}
		record, err := store.GetRequest(ctx, tx.Q(), p.DedupDomain(), env.RequestID)
		if err != nil {
			return err
		}
		if record.Operation != env.Operation || record.PayloadHash != hash || record.Status != protocol.OpSucceeded {
			return store.ErrNotFound
		}
		h, err := store.GetQuestionHandoff(ctx, tx.Q(), run)
		if err != nil {
			return err
		}
		if h == nil || !h.Stopped {
			return store.ErrNotFound
		}
		lease, err := store.GetLeaseByRun(ctx, tx.Q(), run)
		if err != nil {
			return err
		}
		leaseID, hostID, role = lease.LeaseID, r.HostID, r.Role
		return nil
	})
	return
}
