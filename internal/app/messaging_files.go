package app

import (
	"context"
	"errors"
	"mime"
	"path"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// communicationAttachment retains an existing original, never a native path.
func communicationAttachment(ctx context.Context, x *Exec, tx *store.Tx, file, conversation, execution string) error {
	// A reply's destination conversation is not the publisher's authority scope.
	if x.coordinationExecution != nil {
		conversation = x.coordinationExecution.ConversationID
	}
	attachment, err := store.GetConversationAttachment(ctx, tx.Q(), file)
	// An already published child report still has its immutable result
	// provenance. Its first chat destination must not hide it from the parent
	// that owns the formal review/contribution responsibility.
	if err == nil && x.coordinationExecution != nil && x.coordinationExecution.IssueID != "" {
		allowed, checkErr := communicationResultInScope(ctx, tx.Q(), x.coordinationExecution.IssueID, file)
		if checkErr != nil {
			return checkErr
		}
		if allowed {
			return nil
		}
	}
	if errors.Is(err, store.ErrNotFound) {
		result, _, err := store.GetFinalizedDeliverableFile(ctx, tx.Q(), file)
		if errors.Is(err, store.ErrNotFound) {
			// An independent verdict owns its original evidence just as an
			// artifact owns its result files; an upload alone authorizes neither.
			result, _, err = store.GetFinalizedAcceptanceFile(ctx, tx.Q(), file)
		}
		if err != nil {
			return errNotFound(err, "finalized attachment unavailable")
		}
		if execution != "" && x.Principal.Kind != protocol.PrincipalOwner && conversation != "main" {
			var issue string
			if x.coordinationExecution != nil {
				issue = x.coordinationExecution.IssueID
			}
			allowed, err := communicationResultInScope(ctx, tx.Q(), issue, file)
			if err != nil {
				return err
			}
			if !allowed {
				return protocol.Errorf(protocol.CodeForbidden, "result file is outside this responsibility")
			}
		}
		media := mime.TypeByExtension(path.Ext(result.Name))
		if media == "" {
			media = "application/octet-stream"
		}
		return tx.InsertConversationAttachment(ctx, store.ConversationAttachmentRow{ConversationAttachment: protocol.ConversationAttachment{FileID: result.FileID, Name: result.Name, MediaType: media, Size: result.Size, SHA256: result.SHA256}, CreatedAt: x.Now.UnixMilli()})
	}
	if err != nil {
		return err
	}
	if execution == "" || x.Principal.Kind == protocol.PrincipalOwner {
		return nil
	}
	ids, err := store.FollowupIDs(ctx, tx.Q(), `SELECT a.message_id FROM conversation_message_attachments a WHERE a.file_id=?`, attachment.FileID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := communicationSourceVisible(ctx, tx.Q(), id); err != nil {
			if protocol.CodeOf(err) == protocol.CodeForbidden {
				continue
			}
			return err
		}
		origin, err := store.MessageConversation(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		if origin == conversation {
			return nil
		}
		if x.coordinationExecution != nil && x.coordinationExecution.IssueID != "" {
			if err := hostedSourceInIssueScope(ctx, tx.Q(), x.coordinationExecution.IssueID, id); err == nil {
				return nil
			} else if protocol.CodeOf(err) != protocol.CodeForbidden {
				return err
			}
		}
	}
	return protocol.Errorf(protocol.CodeForbidden, "attachment outside this conversation or Issue")
}

func communicationResultInScope(ctx context.Context, q store.Querier, issue, file string) (bool, error) {
	if issue == "" {
		return false, nil
	}
	result, artifact, err := store.GetFinalizedDeliverableFile(ctx, q, file)
	if errors.Is(err, store.ErrNotFound) {
		var acceptance store.AcceptanceRow
		result, acceptance, err = store.GetFinalizedAcceptanceFile(ctx, q, file)
		artifact = acceptance.ArtifactID
	}
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var allowed, revoked bool
	err = q.QueryRowContext(ctx, `WITH RECURSIVE tree(id) AS (
 SELECT ?1 UNION ALL SELECT i.issue_id FROM issue_controls i JOIN tree p ON i.parent_issue_id=p.id
) SELECT (
 EXISTS(SELECT 1 FROM task_work_item_owners o JOIN tree ON tree.id=o.work_item_id WHERE o.task_id=?2)
 OR EXISTS(SELECT 1 FROM artifact_shares s JOIN artifacts a ON a.artifact_id=s.artifact_id AND a.ref_json=s.ref_json WHERE s.issue_id=?1 AND s.artifact_id=?3 AND s.status='active')
), (
 EXISTS(SELECT 1 FROM artifact_shares WHERE issue_id=?1 AND artifact_id=?3 AND status='revoked')
 AND NOT EXISTS(SELECT 1 FROM artifact_shares WHERE issue_id=?1 AND artifact_id=?3 AND status='active')
)`, issue, result.TaskID, artifact).Scan(&allowed, &revoked)
	if err == nil && revoked {
		return false, protocol.Errorf(protocol.CodeForbidden, "this result's explicit share was revoked")
	}
	return allowed, err
}
