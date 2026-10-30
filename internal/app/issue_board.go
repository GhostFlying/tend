package app

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func setCanonicalIssueStatus(ctx context.Context, x *Exec, tx *store.Tx, current protocol.Issue) (Outcome, error) {
	var p protocol.SetIssueStatusPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if !slices.Contains(protocol.IssueWorkStatusValues(), p.Status) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "invalid work status")
	}
	if err := domain.CheckIssueEpoch(current.IssueControl, p.ExpectedAssignmentEpoch); err != nil {
		return Outcome{}, err
	}
	if current.BoardStatus() == p.Status {
		return Outcome{Result: current}, nil
	}
	if p.Status == protocol.IssueDone || p.Status == protocol.IssueWorkCancelled {
		// Managers use their existing evidence-bearing close/cancel tools. An
		// authenticated owner drag is itself an explicit endpoint instruction.
		if x.coordinationExecution != nil {
			return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "use the evidence-bearing endpoint tool")
		}
		facts, err := canonicalOwnerEndpointFacts(ctx, tx.Q(), current)
		if err != nil {
			return Outcome{}, err
		}
		blockers := domain.IssueEndpointBlockers(facts)
		if p.Status == protocol.IssueDone {
			for _, stage := range current.Plan.Stages {
				if stage.Status == protocol.WorkItemStageActive || stage.Status == protocol.WorkItemStagePlanned {
					blockers = append(blockers, protocol.IssueBlocker{Code: "stage", ObjectID: stage.StageID, Revision: current.Revision})
				}
			}
		}
		if len(blockers) > 0 {
			return Outcome{}, canonicalEndpointError("status change has unresolved obligations", blockers)
		}
		// Switching between settled endpoints uses the same domain transitions,
		// without exposing or recording an intermediate active revision.
		if current.State == protocol.IssueClosed || current.State == protocol.IssueCancelled {
			current.IssueControl, err = domain.ReopenIssueControl(current.IssueControl)
			if err != nil {
				return Outcome{}, err
			}
			current.Closure = nil
		}
		inner := *x
		env := *x.Env
		inner.Env = &env
		common := canonicalOwnerControl{ExpectedAssignmentEpoch: current.AssignmentEpoch, Reason: "Owner moved the Issue to " + string(p.Status)}
		if p.Status == protocol.IssueDone {
			endpoint := canonicalOwnerEndpoint{canonicalOwnerControl: common, Closure: &protocol.IssueClosure{Kind: protocol.WorkItemClosureCompleted, Reason: common.Reason, Evidence: []protocol.IssueEvidence{{Kind: protocol.WorkItemEvidenceOwnerDecision, Reference: "current_request", Summary: "Explicit authenticated owner completion from the Issue board"}}}}
			env.Payload, err = json.Marshal(endpoint)
			if err != nil {
				return Outcome{}, err
			}
			return applyCanonicalOwnerLifecycle(ctx, &inner, tx, "close", current)
		}
		// With no unsettled work, record cancellation intent and its observed
		// settlement in one atomic revision, using the same domain transitions.
		next := current
		if next.State != protocol.IssueCancelling {
			next.IssueControl, err = domain.BeginIssueCancellation(current.IssueControl)
			if err != nil {
				return Outcome{}, err
			}
		}
		next.IssueControl, err = domain.FinishIssueCancellation(next.IssueControl, facts)
		if err != nil {
			return Outcome{}, err
		}
		next.Closure = nil
		next.Revision++
		next.UpdatedAt = x.Now.UnixMilli()
		if err = validateCanonicalOwnerGraph(ctx, tx.Q(), next, nil, false); err != nil {
			return Outcome{}, err
		}
		out, err := saveCanonicalOwnerDefinition(ctx, x, tx, next, current.Revision, canonicalOwnerIssueSource{}, common, "issue.cancelled")
		if err != nil {
			return Outcome{}, err
		}
		if err = recordCanonicalEndpointContextReviews(ctx, tx, next.IssueID); err != nil {
			return Outcome{}, err
		}
		return out, nil
	}
	next := current
	activation := false
	if current.State == protocol.IssueClosed || current.State == protocol.IssueCancelled {
		var err error
		next.IssueControl, err = domain.ReopenIssueControl(current.IssueControl)
		if err != nil {
			return Outcome{}, err
		}
		next.Closure = nil
		activation = true
	} else if current.State == protocol.IssueCancelling {
		return Outcome{}, canonicalEndpointError("cancellation must settle before changing work status", []protocol.IssueBlocker{{Code: "issue_cancelling", ObjectID: current.IssueID}})
	}
	next.WorkStatus = p.Status
	next.Revision++
	next.UpdatedAt = x.Now.UnixMilli()
	if err := validateCanonicalOwnerGraph(ctx, tx.Q(), next, nil, activation); err != nil {
		return Outcome{}, err
	}
	return saveCanonicalOwnerDefinition(ctx, x, tx, next, current.Revision, canonicalOwnerIssueSource{}, p, "issue.status_changed")
}
