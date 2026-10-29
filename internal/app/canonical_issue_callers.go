package app

import (
	"context"
	"encoding/json"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// IssueFollowupCommand reaches the existing rule engine only after canonical
// responsibility and rule ownership have been checked. Legacy guards stay intact.
type IssueFollowupCommand struct{ Action string }

func (c IssueFollowupCommand) Operation() string { return "issues.followups_" + c.Action }
func (IssueFollowupCommand) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}
func (c IssueFollowupCommand) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.IssueFollowupPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if !x.FollowupsEnabled && c.Action != "pause" && c.Action != "cancel" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "active followups require the hosted main agent")
	}
	id := x.Env.Target.ID
	if c.Action == "create" {
		if x.Env.Target.Type != "issue" {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "followup creation targets an Issue")
		}
	} else {
		if x.Env.Target.Type != "followup" {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "followup control targets a rule")
		}
		r, err := store.GetFollowup(ctx, tx.Q(), id)
		if err != nil {
			return Outcome{}, errNotFound(err, "followup does not exist")
		}
		id = r.WorkItemID
		var deadline bool
		if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM followup_targets WHERE rule_id=? AND kind IN ('deadline','attention'))`, r.RuleID).Scan(&deadline); err != nil {
			return Outcome{}, err
		}
		if deadline {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "manage owned reminder rules through their deadline or Attention operation")
		}
	}
	issue, err := store.GetCanonicalIssue(ctx, tx.Q(), id)
	if err != nil {
		return Outcome{}, errNotFound(err, "Issue does not exist")
	}
	if err := domain.CheckIssueEpoch(issue.IssueControl, p.ExpectedAssignmentEpoch); err != nil {
		return Outcome{}, err
	}
	if issue.State == protocol.IssueClosed || issue.State == protocol.IssueCancelled || issue.State == protocol.IssueCancelling && c.Action != "pause" && c.Action != "cancel" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "Issue cannot schedule this followup operation")
	}
	if c.Action == "create" {
		rev, err := requireRevision(x.Env)
		if err != nil {
			return Outcome{}, err
		}
		if rev != issue.Revision {
			return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "Issue revision changed")
		}
	}
	if (c.Action == "create" || c.Action == "update") && p.Spec == nil {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "followup spec is required")
	}
	if p.Spec != nil && x.coordinationExecution != nil && p.Spec.SourceID == "" {
		turn, err := store.GetMainTurn(ctx, tx.Q(), x.coordinationExecution.ID)
		if err != nil {
			return Outcome{}, err
		}
		p.Spec.SourceID = turn.MessageID
	}
	inner := *x
	env := *x.Env
	inner.Env = &env
	if c.Action == "create" {
		env.Payload, err = json.Marshal(p.Spec)
	} else {
		env.Payload, err = json.Marshal(protocol.UpdateFollowupPayload{Spec: p.Spec})
	}
	if err != nil {
		return Outcome{}, err
	}
	out, err := applyFollowup(ctx, &inner, tx, c.Action, &issue)
	if err != nil {
		return out, err
	}
	if r, ok := out.Result.(protocol.FollowupRule); ok {
		out.Result = protocol.CanonicalFollowupRule(r, "")
	}
	return out, nil
}

type IssueRefreshDelivery struct{}

func (IssueRefreshDelivery) Operation() string { return "issues.refresh_delivery" }
func (IssueRefreshDelivery) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}
func (IssueRefreshDelivery) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	if x.Env.Target.Type != "issue" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "delivery refresh targets an Issue")
	}
	var p protocol.IssueRefreshDeliveryPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	issue, err := store.GetCanonicalIssue(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, errNotFound(err, "Issue does not exist")
	}
	if err := domain.CheckIssueEpoch(issue.IssueControl, p.ExpectedAssignmentEpoch); err != nil {
		return Outcome{}, err
	}
	inner := *x
	env := *x.Env
	inner.Env = &env
	env.Payload, err = json.Marshal(protocol.RefreshDeliveryPayload{DeliveryID: p.DeliveryID})
	if err != nil {
		return Outcome{}, err
	}
	out, err := (RefreshDelivery{}).Apply(ctx, &inner, tx)
	if err != nil {
		return out, err
	}
	if o, ok := out.Result.(protocol.DeliveryObservation); ok {
		out.Result = protocol.CanonicalDeliveryObservation(o)
	}
	return out, nil
}
