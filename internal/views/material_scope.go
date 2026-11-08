package views

import (
	"context"
	"errors"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// MaterialIssueIDs preserves the selected Issue's actual ancestry, including
// retained legacy contexts, without enumerating the whole project.
func MaterialIssueIDs(ctx context.Context, q store.Querier, issue string) ([]string, error) {
	var out []string
	for id := issue; id != ""; {
		if slices.Contains(out, id) {
			return nil, protocol.Errorf(protocol.CodeInvalidTransition, "Issue ancestry contains a cycle")
		}
		out = append(out, id)
		current, err := store.GetCanonicalIssue(ctx, q, id)
		if errors.Is(err, store.ErrNotFound) {
			if _, err := store.GetIssue(ctx, q, id); err != nil {
				return nil, err
			}
			break
		}
		if err != nil {
			return nil, err
		}
		id = current.ParentIssueID
	}
	return out, nil
}

// SelectMaterialIssue is the same context selection used by an actual original
// read and by its discovery reference. Reader identity survives that selection.
func SelectMaterialIssue(ctx context.Context, q store.Querier, scope store.MaterialScope, id string) (store.MaterialScope, error) {
	if id == "" || (len(scope.IssueIDs) > 0 && id == scope.IssueIDs[0]) {
		return scope, nil
	}
	item, err := store.GetIssue(ctx, q, id)
	if errors.Is(err, store.ErrNotFound) {
		return scope, protocol.Errorf(protocol.CodeNotFound, "Issue does not exist")
	}
	if err != nil {
		return scope, err
	}
	if !slices.Contains(scope.IssueIDs, id) && (scope.Project == "" || scope.Project != item.Project) {
		return scope, protocol.Errorf(protocol.CodeForbidden, "Issue is outside this context")
	}
	ids, err := MaterialIssueIDs(ctx, q, id)
	if err != nil {
		return scope, err
	}
	selected, err := MaterialScope(ctx, q, ids)
	selected.ReaderIssueID, selected.Project = scope.ReaderIssueID, scope.Project
	return selected, err
}
