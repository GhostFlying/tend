package views

import (
	"context"
	"slices"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/scheduler"
	"github.com/GhostFlying/tend/internal/store"
)

// The explicit owner task command authorizes this frozen agreement. Source
// review duties remain Issue responsibilities, not a global execution baseline.
func applyCanonicalTaskAdmission(ctx context.Context, q store.Querier, c *scheduler.Candidate, primary string, frozen []protocol.TaskWorkContext) error {
	controls := map[string]protocol.IssueControl{}
	inheritedPlans := map[string]protocol.IssuePlan{}
	issue, err := store.GetCanonicalIssue(ctx, q, primary)
	if err != nil {
		return err
	}
	for current := issue; ; {
		controls[current.IssueID] = current.IssueControl
		if current.IssueID != primary {
			inheritedPlans[current.IssueID] = current.Plan
		}
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
		if inherited, ok := inheritedPlans[binding.WorkItemID]; ok {
			// Parent plan edits do not have a disposition of this child's task.
			// Compare only the inherited semantic inputs frozen at that revision;
			// metadata/membership revisions and primary continue_existing remain valid.
			previous, err := store.GetCanonicalIssueRevision(ctx, q, binding.WorkItemID, binding.Revision)
			if err != nil {
				return err
			}
			if previous.Plan.Intent != inherited.Intent || !slices.Equal(previous.Plan.Background, inherited.Background) {
				c.ContextPending = true
			}
		}
		digest, _, err := store.EffectiveContext(ctx, q, binding.WorkItemID)
		if err != nil {
			return err
		}
		if binding.ContextDigest != "" && binding.ContextDigest != digest {
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
