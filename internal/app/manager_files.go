package app

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

// ManagerFile resolves only retained bytes attributable to this run's Issue.
// Old epochs may read evidence but cannot acquire new coordination authority.
func (a *App) ManagerFile(ctx context.Context, p protocol.Principal, id string) (protocol.ArtifactFile, error) {
	var file protocol.ArtifactFile
	if p.Kind != protocol.PrincipalRun || p.Role != protocol.RoleIssueManager {
		return file, protocol.Errorf(protocol.CodeForbidden, "manager run required")
	}
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		r, err := store.GetRun(ctx, tx.Q(), p.Subject)
		if err != nil {
			return err
		}
		if r.Role != protocol.RoleIssueManager {
			return protocol.Errorf(protocol.CodeForbidden, "not a manager run")
		}
		m, err := store.GetIssueManagerTask(ctx, tx.Q(), r.TaskID)
		if err != nil {
			return err
		}
		ids, err := materialIssueIDs(ctx, tx.Q(), m.IssueID)
		if err != nil {
			return err
		}
		scope, err := views.MaterialScope(ctx, tx.Q(), ids)
		if err != nil {
			return err
		}
		scope.UploadTaskID, scope.UploadRunID = r.TaskID, r.RunID
		metadata, err := views.IssueMaterialFile(ctx, tx.Q(), scope, id)
		file = metadata.ArtifactFile
		return err
	})
	return file, err
}

// issueScopedFile resolves the same ordinary project knowledge used by reads.
// A new Task still snapshots its selected immutable metadata; it gains no
// control over the source responsibility or permission for a new side effect.
func issueScopedFile(ctx context.Context, q store.Querier, issue string, _ int64, id string) (protocol.ArtifactFile, error) {
	ids, err := materialIssueIDs(ctx, q, issue)
	if err != nil {
		return protocol.ArtifactFile{}, err
	}
	scope, err := views.MaterialScope(ctx, q, ids)
	if err != nil {
		return protocol.ArtifactFile{}, err
	}
	file, err := views.IssueMaterialFile(ctx, q, scope, id)
	return file.ArtifactFile, err
}
