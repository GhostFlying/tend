package views

import (
	"context"
	"errors"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/scheduler"
	"github.com/GhostFlying/tend/internal/store"
)

// The explicit owner task command authorizes this frozen agreement. Source
// review duties remain Issue responsibilities, not a global execution baseline.
func applyCanonicalTaskAdmission(ctx context.Context, q store.Querier, c *scheduler.Candidate, primary string, frozen []protocol.TaskWorkContext) error {
	contract, err := store.GetTaskOwnerContract(ctx, q, c.TaskID)
	if err == nil {
		current, err := store.TaskOwnerContractCurrent(ctx, q, c.TaskID)
		if err != nil {
			return err
		}
		if !current {
			c.OwnerResponsibilityChanged = true
		}
		c.OwnerPin = contract.Execution
		c.OwnerAgentRevision, c.OwnerRuntimeRevision = uint64(contract.AgentRevision), uint64(contract.RuntimeRevision)
		if contract.ReviewIssueID != "" && (c.Kind == protocol.ActionAccept || c.OwnerPin != nil) {
			review, err := store.GetCanonicalIssue(ctx, q, contract.ReviewIssueID)
			if err != nil {
				return err
			}
			if c.Kind == protocol.ActionAccept {
				if review.State != protocol.IssueActive || review.AssignmentState != "active" {
					c.DependenciesDone = false
				}
				for _, dependency := range review.DependsOn {
					prerequisite, err := store.GetCanonicalIssue(ctx, q, dependency)
					if err != nil {
						return err
					}
					if prerequisite.State != protocol.IssueClosed || prerequisite.Closure == nil || prerequisite.Closure.Kind != protocol.WorkItemClosureCompleted {
						c.DependenciesDone = false
					}
				}
			}
			ids, err := store.FollowupIDs(ctx, q, `SELECT run_id FROM run_issue_responsibilities WHERE issue_id=? ORDER BY created_at DESC,rowid DESC LIMIT 1`, review.IssueID)
			if err != nil {
				return err
			}
			if len(ids) > 0 {
				pin, err := store.GetRunExecution(ctx, q, ids[0])
				if err != nil {
					return err
				}
				if review.Assignee.Ref != pin.Identity.ExecutionEnvironmentID+"/"+pin.Identity.LocalID {
					c.OwnerResponsibilityChanged = true
				}
				c.ReviewOwnerPin = pin
			} else if review.AssignmentEpoch != 1 || review.Assignee.Ref != contract.AgentRef || review.Revision != 1 {
				// No previous run is not authority to undo a human takeover or
				// edits made while this bounded review was waiting to start.
				c.OwnerResponsibilityChanged = true
			}
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	controls := map[string]protocol.IssueControl{}
	issue, err := store.GetCanonicalIssue(ctx, q, primary)
	if err != nil {
		return err
	}
	for current := issue; ; {
		controls[current.IssueID] = current.IssueControl
		if current.ParentIssueID == "" {
			break
		}
		current, err = store.GetCanonicalIssue(ctx, q, current.ParentIssueID)
		if err != nil {
			return err
		}
	}
	dependencies := []protocol.IssueDependency{}
	achieved := map[string]bool{}
	for _, id := range issue.DependsOn {
		prerequisite, err := store.GetCanonicalIssue(ctx, q, id)
		if err != nil {
			return err
		}
		achieved[id] = prerequisite.State == protocol.IssueClosed && prerequisite.Closure != nil && prerequisite.Closure.Kind == protocol.WorkItemClosureCompleted
		dependencies = append(dependencies, protocol.IssueDependency{IssueID: primary, DependsOn: id})
	}
	if len(domain.IssueDispatchBlockers(primary, controls, dependencies, achieved)) > 0 {
		c.DependenciesDone = false
	}
	if err := applyStageDependencies(ctx, q, c, protocol.WorkItem{WorkItemID: issue.IssueID, IssuePlan: issue.Plan}); err != nil {
		return err
	}
	frozenItems := map[string]bool{}
	for _, binding := range frozen {
		frozenItems[binding.WorkItemID] = true
		if _, current := controls[binding.WorkItemID]; !current {
			c.ContextPending = true
		}
		if c.Kind == protocol.ActionAccept {
			// Independent acceptance judges the current artifact against its frozen
			// spec. Later semantic inputs belong to continuing Issue responsibility;
			// they neither replace that contract nor settle through its verdict.
			// Keep the current ownership, lifecycle and ancestry checks above/below.
			continue
		}
		current, err := store.TaskContextBindingCurrent(ctx, q, c.TaskID, c.SpecVersion, binding)
		if err != nil {
			return err
		}
		if !current {
			c.ContextPending = true
		}
	}
	for id := range controls {
		if !frozenItems[id] {
			c.ContextPending = true
		}
	}
	return nil
}
