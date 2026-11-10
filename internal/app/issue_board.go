package app

import (
	"context"
	"encoding/json"
	"errors"
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
	if current.BoardStatus() == protocol.IssueBacklog && p.Status != protocol.IssueBacklog && p.Status != protocol.IssueTodo && p.Status != protocol.IssueWorkCancelled {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "move backlog to todo before starting work")
	}
	if current.BoardStatus() == p.Status && p.Owner == nil {
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
		if p.Status != protocol.IssueBacklog {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "reopen the Issue to backlog before moving it to todo")
		}
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
	if p.Owner != nil {
		choice := &protocol.IssueOwnerSelection{CategoryID: p.Owner.CategoryID, AgentID: p.Owner.AgentID, OwnershipPolicy: protocol.IssueOwnerPolicy}
		if err := validateIssueOwnerSelection(ctx, tx.Q(), choice); err != nil {
			return Outcome{}, err
		}
		if manager, err := store.CurrentIssueManagerTask(ctx, tx.Q(), current.IssueID); err == nil {
			raw, err := store.GetSpec(ctx, tx.Q(), manager.TaskID, 1)
			if err != nil {
				return Outcome{}, err
			}
			var spec protocol.IssueManagerSpec
			if err := json.Unmarshal([]byte(raw), &spec); err != nil {
				return Outcome{}, err
			}
			if spec.CategoryID != choice.CategoryID || spec.AgentID != choice.AgentID || spec.OwnershipPolicy != choice.OwnershipPolicy {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "replace the existing owner explicitly before changing execution selection")
			}
		} else if !errors.Is(err, store.ErrNotFound) {
			return Outcome{}, err
		}
		next.OwnerSelection = choice
	}
	if p.Status == protocol.IssueTodo {
		if next.OwnerSelection == nil {
			manager, err := store.CurrentIssueManagerTask(ctx, tx.Q(), current.IssueID)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return Outcome{}, err
			}
			if err == nil {
				raw, err := store.GetSpec(ctx, tx.Q(), manager.TaskID, 1)
				if err != nil {
					return Outcome{}, err
				}
				var spec protocol.IssueManagerSpec
				if err := json.Unmarshal([]byte(raw), &spec); err != nil {
					return Outcome{}, err
				}
				next.OwnerSelection = &protocol.IssueOwnerSelection{CategoryID: spec.CategoryID, AgentID: spec.AgentID, OwnershipPolicy: spec.OwnershipPolicy}
			}
		}
		frozenReview, err := store.IssueHasFrozenReview(ctx, tx.Q(), current.IssueID)
		if err != nil {
			return Outcome{}, err
		}
		if next.OwnerSelection != nil || !frozenReview {
			if err := validateIssueOwnerSelection(ctx, tx.Q(), next.OwnerSelection); err != nil {
				return Outcome{}, err
			}
		}
		if current.BoardStatus() != protocol.IssueTodo {
			next.TodoAt = x.Now.UnixMilli()
			next.TodoRevision = current.Revision + 1
		}
	}
	if activation {
		next.TodoAt = 0
		next.TodoRevision = 0
	}
	next.WorkStatus = p.Status
	next.Revision++
	next.UpdatedAt = x.Now.UnixMilli()
	if err := validateCanonicalOwnerGraph(ctx, tx.Q(), next, nil, activation); err != nil {
		return Outcome{}, err
	}
	return saveCanonicalOwnerDefinition(ctx, x, tx, next, current.Revision, canonicalOwnerIssueSource{}, p, "issue.status_changed")
}
