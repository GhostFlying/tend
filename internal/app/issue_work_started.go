package app

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// markIssueWorkStarted records assigned execution in the same transaction.
// Selecting an owner or recording a conversation alone does not start work.
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
