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
		file, err = issueScopedFile(ctx, tx.Q(), m.IssueID, m.AssignmentEpoch, id)
		return err
	})
	return file, err
}

// issueScopedFile is shared by direct manager reads and new input binding, so
// a spec cannot create authority that the creating responsibility does not hold.
func issueScopedFile(ctx context.Context, q store.Querier, issue string, epoch int64, id string) (protocol.ArtifactFile, error) {
	// Publication adds attachment provenance without removing the independent
	// authority of this Issue's own finalized deliverable.
	delivery, _, deliveryErr := store.GetFinalizedDeliverableFile(ctx, q, id)
	if errors.Is(deliveryErr, store.ErrNotFound) {
		delivery, _, deliveryErr = store.GetFinalizedAcceptanceFile(ctx, q, id)
	}
	if deliveryErr != nil && !errors.Is(deliveryErr, store.ErrNotFound) {
		return protocol.ArtifactFile{}, deliveryErr
	}
	if deliveryErr == nil {
		primary, err := store.GetTaskIssueOwner(ctx, q, delivery.TaskID)
		if err != nil {
			return protocol.ArtifactFile{}, err
		}
		if primary == issue {
			return delivery.ArtifactFile, nil
		}
	}
	attachment, err := store.GetConversationAttachment(ctx, q, id)
	if err == nil {
		file := protocol.ArtifactFile{FileID: id, Name: attachment.Name, Size: attachment.Size, SHA256: attachment.SHA256}
		allowed, err := store.ArtifactFileSharedWithIssue(ctx, q, issue, epoch, file)
		if err != nil {
			return file, err
		}
		if allowed {
			return file, nil
		}
		message, err := store.AttachmentSourceMessage(ctx, q, id)
		if err != nil {
			return file, err
		}
		if message == "" {
			return file, protocol.Errorf(protocol.CodeForbidden, "attachment has no Issue source")
		}
		if err := hostedSourceInIssueScope(ctx, q, issue, message); err != nil {
			return file, err
		}
		return file, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return protocol.ArtifactFile{}, err
	}
	if deliveryErr != nil {
		return protocol.ArtifactFile{}, deliveryErr
	}
	allowed, err := store.ArtifactFileSharedWithIssue(ctx, q, issue, epoch, delivery.ArtifactFile)
	if err != nil {
		return protocol.ArtifactFile{}, err
	}
	if !allowed {
		return protocol.ArtifactFile{}, protocol.Errorf(protocol.CodeForbidden, "file is outside the manager Issue")
	}
	return delivery.ArtifactFile, nil
}
