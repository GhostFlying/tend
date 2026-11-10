package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// issueAcceptanceWasDefault proves the initial default from the immutable
// creation input. Explicit creator choices, later changes and unavailable
// provenance retain their original independent-acceptance requirement.
func issueAcceptanceWasDefault(ctx context.Context, q store.Querier, issue protocol.Issue) (bool, error) {
	p := issue.AcceptancePolicy
	if p == nil || !p.Required || p.IssueRevision != 1 || p.RequestID == "" {
		return false, nil
	}
	var raw string
	if err := q.QueryRowContext(ctx, `SELECT reason_json FROM issue_control_revisions WHERE issue_id=? AND revision=1 AND request_id=?`, issue.IssueID, p.RequestID).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	var original struct {
		IndependentAcceptance *protocol.IndependentAcceptanceRequest `json:"independent_acceptance"`
	}
	if err := json.Unmarshal([]byte(raw), &original); err != nil {
		return false, err
	}
	return original.IndependentAcceptance == nil, nil
}

// taskAcceptancePolicy records the responsible Agent's choice for new work.
// Frozen Tasks are not rewritten, and explicit human requirements remain.
func taskAcceptancePolicy(ctx context.Context, x *Exec, q store.Querier, issue protocol.Issue, spec domain.Spec, input *protocol.IndependentAcceptanceRequest) (*protocol.IssueAcceptancePolicy, error) {
	review, err := explicitTaskReviewTarget(ctx, q, issue, spec)
	if err != nil {
		return nil, err
	}
	defaulted, err := issueAcceptanceWasDefault(ctx, q, issue)
	if err != nil {
		return nil, err
	}
	standing := issue.AcceptancePolicy
	humanRequired := !review && standing != nil && standing.Required && !defaulted && (standing.DecidedBy.OwnerSubject != "" || standing.DecidedBy.Kind == "owner" || standing.DecidedBy.Kind == "" && standing.Creator.Kind == "owner")
	if humanRequired && input == nil {
		policy := *standing
		return &policy, nil
	}
	required := humanRequired || spec.AgentSelection != nil && spec.AgentSelection.Acceptance.CategoryID != ""
	if standing != nil && !standing.Required {
		required = false
	}
	if review {
		required = false
	}
	reason := "Issue owner chose self-check for this work; no explicit human independent-acceptance requirement"
	if review {
		reason = "This explicit independent review reports its real result once; the reviewed object's policy does not require a review of the review"
	}
	if required {
		reason = "Retain the explicit independent-acceptance requirement for this work"
	}
	if input != nil {
		if _, err := newIssueAcceptancePolicy(x, input); err != nil {
			return nil, err
		}
		if humanRequired && !input.Required {
			return nil, protocol.Errorf(protocol.CodeForbidden, "this Issue has an explicit human independent-acceptance requirement")
		}
		required, reason = input.Required, input.Reason
	}
	p := &protocol.IssueAcceptancePolicy{Required: required, Reason: reason, RequestID: x.Env.RequestID, IssueRevision: issue.Revision}
	if standing != nil {
		p.Creator, p.DecidedBy = standing.Creator, standing.DecidedBy
	}
	if x.coordinationExecution != nil {
		identity := x.coordinationExecution.Selection.Identity
		p.DecidedBy = protocol.AcceptancePolicyCreator{Kind: "agent", Agent: &identity, SourceIssueID: issue.IssueID}
	} else if x.Actor != nil && x.actorAgent != nil {
		p.DecidedBy = protocol.AcceptancePolicyCreator{Kind: "agent", Agent: x.actorAgent, OwnerSubject: x.Principal.Subject}
	} else if x.Principal.Kind == protocol.PrincipalOwner {
		p.DecidedBy = protocol.AcceptancePolicyCreator{Kind: "owner", OwnerSubject: x.Principal.Subject}
	}
	return p, nil
}

// The review target is explicit business context, never a category exemption.
// Ordinary file inputs alone do not change the Task's quality agreement.
func explicitTaskReviewTarget(ctx context.Context, q store.Querier, issue protocol.Issue, spec domain.Spec) (bool, error) {
	if spec.ReviewArtifactID == "" && spec.ReviewDeliveryID == "" {
		return false, nil
	}
	if !spec.RequiresDelivery(domain.DeliveryReport) {
		return false, protocol.Errorf(protocol.CodeInvalidRequest, "an explicit review Task must produce its actual report")
	}
	if spec.ReviewArtifactID != "" {
		if !protocol.ValidID(protocol.PrefixArtifact, spec.ReviewArtifactID) {
			return false, protocol.Errorf(protocol.CodeInvalidRequest, "review_artifact_id must name an actual immutable result")
		}
		if _, err := store.GetArtifact(ctx, q, spec.ReviewArtifactID); err != nil {
			return false, errNotFound(err, "the original review artifact is unavailable")
		}
		allowed, err := store.ArtifactShareReadable(ctx, q, issue.IssueID, spec.ReviewArtifactID, issue.AssignmentEpoch)
		if err != nil {
			return false, err
		}
		if !allowed {
			return false, protocol.Errorf(protocol.CodeForbidden, "the explicit review artifact is outside this Issue's readable contribution scope")
		}
	}
	if spec.ReviewDeliveryID != "" && !slices.ContainsFunc(issue.Plan.Deliveries, func(d protocol.IssueDelivery) bool { return d.DeliveryID == spec.ReviewDeliveryID }) {
		return false, protocol.Errorf(protocol.CodeInvalidRequest, "review_delivery_id must identify this Issue's registered PR")
	}
	return true, nil
}

func newIssueAcceptancePolicy(x *Exec, input *protocol.IndependentAcceptanceRequest) (*protocol.IssueAcceptancePolicy, error) {
	creator := protocol.AcceptancePolicyCreator{}
	if x.coordinationExecution != nil {
		id := x.coordinationExecution.Selection.Identity
		creator.Kind, creator.Agent, creator.SourceIssueID = "agent", &id, x.coordinationExecution.IssueID
	} else if x.Actor != nil && x.actorAgent != nil {
		creator.Kind, creator.Agent, creator.OwnerSubject = "agent", x.actorAgent, x.Principal.Subject
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
