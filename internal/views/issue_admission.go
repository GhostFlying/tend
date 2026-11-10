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
	issue, err := store.GetCanonicalIssue(ctx, q, primary)
	if err != nil {
		return err
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
		current, err = store.GetCanonicalIssue(ctx, q, current.ParentIssueID)
		if err != nil {
			return err
		}
	}
	var dependencies []protocol.IssueDependency
	achieved := map[string]bool{}
	for _, id := range issue.DependsOn {
		prerequisite, err := store.GetCanonicalIssue(ctx, q, id)
		if err != nil {
			return err
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
