package views

import (
	"context"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/scheduler"
	"github.com/GhostFlying/tend/internal/store"
)

// The explicit owner task command authorizes this frozen agreement. Source
// review duties remain Issue responsibilities, not a global execution baseline.
func applyCanonicalTaskAdmission(ctx context.Context, q store.Querier, c *scheduler.Candidate, primary string) error {
	current, err := store.TaskOwnerContractCurrent(ctx, q, c.TaskID)
	if err != nil {
		return err
	}
	if !current {
		c.OwnerResponsibilityChanged = true
	}
	contract, err := store.GetTaskOwnerContract(ctx, q, c.TaskID)
	if err == nil {
		c.OwnerPin = contract.Execution
		c.OwnerAgentRevision, c.OwnerRuntimeRevision = uint64(contract.AgentRevision), uint64(contract.RuntimeRevision)
		if contract.ReviewIssueID != "" && (c.Kind == protocol.ActionAccept || c.OwnerPin != nil) {
			review, err := store.GetCanonicalIssue(ctx, q, contract.ReviewIssueID)
			if err != nil {
				return err
			}
			if c.Kind == protocol.ActionAccept {
				if err := ApplyIssueAdmission(ctx, q, c, review.IssueID, false); err != nil {
					return err
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
			} else if review.AssignmentEpoch != 1 || review.Assignee.Ref != contract.AgentRef || review.Revision != contract.ReviewAdmissionRevision {
				// No previous run is not authority to undo a human takeover or
				// edits made while this bounded review was waiting to start.
				c.OwnerResponsibilityChanged = true
			}
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if err := ApplyIssueAdmission(ctx, q, c, primary, false); err != nil {
		return err
	}
	issue, err := store.GetCanonicalIssue(ctx, q, primary)
	if err != nil {
		return err
	}
	if err := applyStageDependencies(ctx, q, c, protocol.WorkItem{WorkItemID: issue.IssueID, IssuePlan: issue.Plan}); err != nil {
		return err
	}
	// Frozen context records what the Agent received. Later source, definition
	// and ancestry facts notify the owner; they are not another Task permission.
	return nil
}
