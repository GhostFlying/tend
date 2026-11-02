package app

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// markIssueWorkStarted records concrete execution/delegation in the same
// transaction. A coordinator's mere claim or conversation is not work evidence.
func markIssueWorkStarted(ctx context.Context, x *Exec, tx *store.Tx, id string) ([]Event, error) {
	var events []Event
	for id != "" {
		issue, err := store.GetCanonicalIssue(ctx, tx.Q(), id)
		if err != nil {
			return nil, err
		}
		if issue.State != protocol.IssueActive || issue.AssignmentState != "active" || (issue.BoardStatus() != protocol.IssueTodo && issue.BoardStatus() != protocol.IssueInProgress) {
			break
		}
		if issue.BoardStatus() == protocol.IssueTodo {
			previous := issue.Revision
			issue.WorkStatus = protocol.IssueInProgress
			issue.Revision++
			issue.UpdatedAt = x.Now.UnixMilli()
			out, err := saveCanonicalOwnerDefinition(ctx, x, tx, issue, previous, canonicalOwnerIssueSource{}, map[string]any{"reason": "execution or child delegation started"}, "issue.status_changed")
			if err != nil {
				return nil, err
			}
			events = append(events, out.Events...)
		}
		id = issue.ParentIssueID
	}
	return events, nil
}
