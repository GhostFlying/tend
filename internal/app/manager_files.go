package app

import (
	"context"
	"errors"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
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
		attachment, err := store.GetConversationAttachment(ctx, tx.Q(), id)
		if err == nil {
			message, err := store.AttachmentSourceMessage(ctx, tx.Q(), id)
			if err != nil {
				return err
			}
			if message == "" {
				return protocol.Errorf(protocol.CodeForbidden, "attachment has no Issue source")
			}
			if err = hostedSourceInIssueScope(ctx, tx.Q(), m.IssueID, message); err != nil {
				return err
			}
			file = protocol.ArtifactFile{FileID: id, Name: attachment.Name, Size: attachment.Size, SHA256: attachment.SHA256}
			return nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		delivery, _, err := store.GetFinalizedDeliverableFile(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		primary, err := store.GetTaskIssueOwner(ctx, tx.Q(), delivery.TaskID)
		if err != nil {
			return err
		}
		if primary != m.IssueID {
			return protocol.Errorf(protocol.CodeForbidden, "file is outside the manager Issue")
		}
		file = delivery.ArtifactFile
		return nil
	})
	return file, err
}
