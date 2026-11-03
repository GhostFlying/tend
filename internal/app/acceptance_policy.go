package app

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func newIssueAcceptancePolicy(x *Exec, input *protocol.IndependentAcceptanceRequest) (*protocol.IssueAcceptancePolicy, error) {
	creator := protocol.AcceptancePolicyCreator{}
	if x.coordinationExecution != nil {
		id := x.coordinationExecution.Selection.Identity
		creator.Kind, creator.Agent, creator.SourceIssueID = "agent", &id, x.coordinationExecution.IssueID
	} else if x.Principal.Kind == protocol.PrincipalOwner {
		creator.Kind, creator.OwnerSubject = "owner", x.Principal.Subject
	} else {
		return nil, protocol.Errorf(protocol.CodeForbidden, "Issue creator identity is unavailable")
	}
	p := &protocol.IssueAcceptancePolicy{Required: true, Creator: creator, DecidedBy: creator, RequestID: x.Env.RequestID, IssueRevision: 1, Reason: "Retain independent acceptance by default"}
	if input != nil {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(x.Env.Payload, &fields); err != nil {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "acceptance policy payload is invalid")
		}
		if x.Env.Operation != "issues.set_acceptance_policy" {
			if err := json.Unmarshal(fields["independent_acceptance"], &fields); err != nil {
				return nil, protocol.Errorf(protocol.CodeInvalidRequest, "independent_acceptance requires an explicit required boolean")
			}
		}
		var choice *bool
		if err := json.Unmarshal(fields["required"], &choice); err != nil || choice == nil {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "independent_acceptance.required must be an explicit boolean")
		}

		if strings.TrimSpace(input.Reason) == "" {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "independent_acceptance.reason is required")
		}
		p.Required, p.Reason = input.Required, input.Reason
	}
	return p, nil
}

// SetIssueAcceptancePolicy changes future work only. Current assignees and
// worker credentials cannot replace the immutable creator authority.
type SetIssueAcceptancePolicy struct{}

func (SetIssueAcceptancePolicy) Operation() string { return "issues.set_acceptance_policy" }
func (SetIssueAcceptancePolicy) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}
func (SetIssueAcceptancePolicy) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	if x.Env.Target.Type != "issue" || x.Env.ParentRequestID != "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "an independent Issue policy request is required")
	}
	var p protocol.SetIssueAcceptancePolicyPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	expected, err := requireRevision(x.Env)
	if err != nil {
		return Outcome{}, err
	}
	current, err := store.GetCanonicalIssue(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, errNotFound(err, "Issue does not exist")
	}
	if current.Revision != expected {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "Issue revision changed")
	}
	if err := domain.CheckIssueEpoch(current.IssueControl, p.ExpectedAssignmentEpoch); err != nil {
		return Outcome{}, err
	}
	if current.AcceptancePolicy == nil {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "historical Issue has no authenticated creator policy")
	}
	actual, err := newIssueAcceptancePolicy(x, &p.IndependentAcceptanceRequest)
	if err != nil {
		return Outcome{}, err
	}
	topMain := x.coordinationExecution != nil && x.coordinationExecution.Selection.Purpose == agentconfig.Main && current.ParentIssueID == ""
	if x.Principal.Kind != protocol.PrincipalOwner && !topMain && !reflect.DeepEqual(actual.Creator, current.AcceptancePolicy.Creator) {
		return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "only the original Issue creator may decide independent acceptance")
	}
	actual.Creator = current.AcceptancePolicy.Creator
	actual.IssueRevision = current.Revision + 1
	current.AcceptancePolicy = actual
	current.Revision++
	current.UpdatedAt = x.Now.UnixMilli()
	return saveCanonicalOwnerDefinition(ctx, x, tx, current, expected, canonicalOwnerIssueSource{}, p, "issue.acceptance_policy_changed")
}
