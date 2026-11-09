package app

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func decisionContinuationPolicy(p *protocol.DecisionContinuationPolicy) protocol.DecisionContinuationPolicy {
	if p == nil {
		return protocol.DecisionContinuationPolicy{}
	}
	policy := *p
	policy.Reason = strings.TrimSpace(policy.Reason)
	return policy
}

func recordDecisionContinuation(ctx context.Context, x *Exec, tx *store.Tx, d store.DecisionRow, p *protocol.DecisionContinuationPolicy) error {
	// Legacy answers retain their original automatic handoff contract.
	if p == nil {
		return nil
	}
	if d.Worker == nil {
		return protocol.Errorf(protocol.CodeInvalidRequest, "continuation disposition requires a worker handoff question")
	}
	h, err := store.GetQuestionHandoff(ctx, tx.Q(), d.Worker.RunID)
	if err != nil {
		return err
	}
	if h == nil {
		return protocol.Errorf(protocol.CodeInvalidRequest, "the question has no frozen handoff contract")
	}
	policy := decisionContinuationPolicy(p)
	state := "waiting"
	switch policy.Mode {
	case protocol.DecisionContinueNow:
		if policy.Dependency != nil {
			return protocol.Errorf(protocol.CodeInvalidRequest, "continue_now does not accept a dependency")
		}
		state = "ready"
	case protocol.DecisionWait:
		if policy.Reason == "" || policy.Dependency != nil {
			return protocol.Errorf(protocol.CodeInvalidRequest, "wait requires a reason and an explicit-resume condition")
		}
	case protocol.DecisionDependency:
		if policy.Reason == "" || policy.Dependency == nil || policy.Dependency.TaskID == "" || policy.Dependency.SpecVersion < 1 || policy.Dependency.TaskID == d.TaskID {
			return protocol.Errorf(protocol.CodeInvalidRequest, "dependency requires a reason and another exact task spec")
		}
		dependency, err := store.GetTask(ctx, tx.Q(), policy.Dependency.TaskID)
		if err != nil {
			return errNotFound(err, "dependency task does not exist")
		}
		if dependency.Kind != protocol.TaskKindExecution {
			return protocol.Errorf(protocol.CodeInvalidRequest, "dependency must be an ordinary execution task")
		}
		if dependency.SpecVersion != policy.Dependency.SpecVersion {
			return protocol.Errorf(protocol.CodeStaleRevision, "dependency task spec changed")
		}
		ready, _, err := store.DecisionDependencyReady(ctx, tx.Q(), policy.Dependency)
		if err != nil {
			return err
		}
		if ready {
			state = "ready"
		}
	default:
		return protocol.Errorf(protocol.CodeInvalidRequest, "continuation mode must be continue_now, wait or dependency")
	}
	return tx.PutDecisionContinuation(ctx, store.DecisionContinuationRow{DecisionID: d.DecisionID, Revision: 1,
		Policy: policy, State: state, AnswerRequestID: x.Env.RequestID, CreatedAt: x.Now.UnixMilli(), UpdatedAt: x.Now.UnixMilli()})
}

// ResumeDecisionContinuation changes scheduling authority without rewriting the
// answer. Reconcile derives readiness from a frozen dependency, not a bool.
type ResumeDecisionContinuation struct{ Reconcile, FromMessage bool }

func (c ResumeDecisionContinuation) Operation() string {
	if c.Reconcile {
		return protocol.OpReconcileDecisionContinuation
	}
	return protocol.OpResumeDecisionContinuation
}

func (c ResumeDecisionContinuation) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	if c.FromMessage {
		return allowKinds(x.Principal, protocol.PrincipalMainAgent)
	}
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}

func (c ResumeDecisionContinuation) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	if x.Env.Target.Type != "decision" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "decision target is required")
	}
	var p protocol.ResumeDecisionContinuationPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if c.Reconcile && p.ExpectedAssignmentEpoch != nil {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "dependency reconciliation does not adopt execution responsibility; use explicit resume")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(x.Env.Payload, &fields); err != nil || len(fields["expected_revision"]) == 0 || bytes.Equal(bytes.TrimSpace(fields["expected_revision"]), []byte("null")) || p.ExpectedRevision < 0 {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "explicit nonnegative expected_revision is required")
	}
	expected, err := requireRevision(x.Env)
	if err != nil {
		return Outcome{}, err
	}
	v, err := store.ReadDecisionContinuation(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, errNotFound(err, "question continuation does not exist")
	}
	if expected != v.TaskControlRevision || p.ExpectedRevision != v.Revision {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "continuation or task control revision changed")
	}
	d, err := store.GetDecision(ctx, tx.Q(), v.DecisionID)
	if err != nil {
		return Outcome{}, err
	}
	if x.Principal.Kind != protocol.PrincipalOwner {
		if x.coordinationExecution == nil {
			return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "admitted coordination execution is required")
		}
		if !c.Reconcile {
			if p.SourceMessageID == "" {
				allowed, err := agentTechnicalWaitScope(ctx, tx.Q(), *x.coordinationExecution, d)
				if err != nil {
					return Outcome{}, err
				}
				if !allowed {
					return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "explicit resume requires an actual owner source")
				}
			} else if err := validateAttentionOwnerRelay(ctx, tx.Q(), x, x.coordinationExecution.IssueID, p.SourceMessageID); err != nil {
				return Outcome{}, err
			}
		}
	}
	if v.State == "continued" {
		return Outcome{Result: v, NoEffect: true}, nil
	}
	if d.Status != store.DecisionAnswered {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "record an answer before continuing its handoff")
	}
	valid, err := store.QuestionHandoffCurrent(ctx, tx.Q(), v.RunID)
	if err != nil {
		return Outcome{}, err
	}
	if p.ExpectedAssignmentEpoch != nil && (*p.ExpectedAssignmentEpoch != v.CurrentAssignmentEpoch || strings.TrimSpace(p.Reason) == "") {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "current assignment epoch and explicit continuation reason are required")
	}
	if !valid {
		if err := admitStoppedResponsibilityAdoption(ctx, x, tx, v, p); err != nil {
			return Outcome{}, err
		}
		valid, err = store.QuestionHandoffCurrent(ctx, tx.Q(), v.RunID)
		if err != nil {
			return Outcome{}, err
		}
		if !valid {
			return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "original handoff agreement or responsibility changed")
		}
	}
	if c.Reconcile && (v.Policy == nil || v.Policy.Mode != protocol.DecisionDependency) {
		return Outcome{}, continuationBlocked(v, "this wait requires explicit resume, not readiness reconciliation")
	}
	if v.Policy != nil && v.Policy.Mode == protocol.DecisionDependency {
		ready, reason, err := store.DecisionDependencyReady(ctx, tx.Q(), v.Policy.Dependency)
		if err != nil {
			return Outcome{}, err
		}
		if !ready {
			v.BlockedReason = reason
			return Outcome{}, continuationBlocked(v, "dependency is not completed at its frozen spec")
		}
	}
	row, err := store.GetDecisionContinuation(ctx, tx.Q(), v.DecisionID)
	if err != nil {
		return Outcome{}, err
	}
	if row == nil {
		// Preserve the legacy answer; this new request supplies independent
		// permission and does not retroactively invent an old answer policy.
		row = &store.DecisionContinuationRow{DecisionID: v.DecisionID, Policy: protocol.DecisionContinuationPolicy{Mode: protocol.DecisionContinueNow, Reason: "independent explicit resume of the original contract"}, CreatedAt: x.Now.UnixMilli()}
	}
	row.Revision++
	row.State, row.ResumeRequestID, row.UpdatedAt = "ready", x.Env.RequestID, x.Now.UnixMilli()
	if err := tx.PutDecisionContinuation(ctx, *row); err != nil {
		return Outcome{}, err
	}
	if err := resumeAnsweredQuestion(ctx, x, tx, v.DecisionID); err != nil {
		return Outcome{}, err
	}
	v, err = store.ReadDecisionContinuation(ctx, tx.Q(), v.DecisionID)
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: v, Events: []Event{{ObjectType: "decision", ObjectID: v.DecisionID,
		ObjectVersion: v.Revision, Kind: "decision.continuation_admitted", Body: map[string]any{
			"state": v.State, "source_message_id": p.SourceMessageID, "reconcile": c.Reconcile}}}}, nil
}

func continuationBlocked(v protocol.DecisionContinuationView, message string) error {
	err := protocol.Errorf(protocol.CodeInvalidTransition, "%s", message)
	err.Details, _ = json.Marshal(v)
	return err
}

// ReadDecisionContinuation is a read-only ledger projection, including legacy
// contracts and exact blocking facts. It does not probe or resume a provider.
func (a *App) ReadDecisionContinuation(ctx context.Context, decision string) (protocol.DecisionContinuationView, error) {
	var out protocol.DecisionContinuationView
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		out, err = store.ReadDecisionContinuation(ctx, tx.Q(), decision)
		return errNotFound(err, "question continuation does not exist")
	})
	return out, err
}

// Only actual completion events visit the indexed dependants. No general
// writer-tail scan or external readiness polling is introduced.
func reconcileDecisionDependencies(ctx context.Context, x *Exec, tx *store.Tx, out *Outcome) error {
	seen := map[string]bool{}
	for _, event := range out.Events {
		if event.ObjectType != "task" || event.Kind != "task.completed" || seen[event.ObjectID] {
			continue
		}
		seen[event.ObjectID] = true
		ids, err := store.FollowupIDs(ctx, tx.Q(), `SELECT decision_id FROM decision_continuations WHERE dependency_task_id=? AND mode='dependency' AND state='waiting'`, event.ObjectID)
		if err != nil {
			return err
		}
		for _, id := range ids {
			row, err := store.GetDecisionContinuation(ctx, tx.Q(), id)
			if err != nil {
				return err
			}
			ready, _, err := store.DecisionDependencyReady(ctx, tx.Q(), row.Policy.Dependency)
			if err != nil {
				return err
			}
			if !ready {
				continue
			}
			row.State, row.UpdatedAt = "ready", x.Now.UnixMilli()
			row.Revision++
			if err := tx.PutDecisionContinuation(ctx, *row); err != nil {
				return err
			}
			if err := resumeAnsweredQuestion(ctx, x, tx, id); err != nil {
				return err
			}
			out.Events = append(out.Events, Event{ObjectType: "decision", ObjectID: id, Kind: "decision.continuation_ready",
				Body: map[string]any{"dependency_task_id": event.ObjectID}})
		}
	}
	return nil
}
