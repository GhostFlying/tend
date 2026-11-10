package views

import (
	"context"
	"fmt"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/scheduler"
	"github.com/GhostFlying/tend/internal/store"
)

func ApplyIssueAdmission(ctx context.Context, q store.Querier, c *scheduler.Candidate, primary string, settlement bool) error {
	facts, err := store.IssueAdmissionFacts(ctx, q, []string{primary})
	if err != nil {
		return err
	}
	return applyIssueAdmissionFacts(c, primary, settlement, facts)
}

func applyIssueAdmissionFacts(c *scheduler.Candidate, primary string, settlement bool, facts map[string]protocol.Issue) error {
	issue, ok := facts[primary]
	if !ok {
		return fmt.Errorf("Issue scheduling control is missing: %s", primary)
	}
	controls := map[string]protocol.IssueControl{}
	for current := issue; ; {
		control := current.IssueControl
		if settlement && issue.State == protocol.IssueCancelling && (control.State == protocol.IssueActive || control.State == protocol.IssuePaused || control.State == protocol.IssueCancelling) {
			control.State = protocol.IssueActive
			control.WorkStatus = protocol.IssueTodo
		}
		controls[current.IssueID] = control
		if current.ParentIssueID == "" {
			break
		}
		parent := current.ParentIssueID
		current, ok = facts[parent]
		if !ok {
			return fmt.Errorf("Issue ancestor control is missing: %s", parent)
		}
	}
	var dependencies []protocol.IssueDependency
	achieved := map[string]bool{}
	for _, id := range issue.DependsOn {
		prerequisite, exists := facts[id]
		if !exists {
			return fmt.Errorf("Issue prerequisite control is missing: %s", id)
		}
		achieved[id] = prerequisite.State == protocol.IssueClosed && prerequisite.Closure != nil && prerequisite.Closure.Kind == protocol.WorkItemClosureCompleted
		dependencies = append(dependencies, protocol.IssueDependency{IssueID: primary, DependsOn: id})
	}
	if settlement && issue.State == protocol.IssueCancelling {
		dependencies = nil
	}
	blockers := domain.IssueDispatchBlockers(primary, controls, dependencies, achieved)
	if len(blockers) > 0 {
		c.DependenciesDone = false
		blocker := blockers[0]
		message := "Issue responsibility is not admitted for execution"
		switch blocker.Code {
		case "issue_backlog":
			message = "Issue is in backlog"
		case "issue_dependency":
			message = "Issue prerequisite is not complete"
		case "issue_assignment":
			message = "Issue assignment is being transferred"
		case "issue_admission":
			message = "Issue is paused, cancelling or closed"
		}
		c.AdmissionMessage = fmt.Sprintf("%s: %s", message, blocker.ObjectID)
	}
	return nil
}
