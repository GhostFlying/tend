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
	if errors.Is(err, store.ErrNotFound) {
		result, _, err := store.GetFinalizedDeliverableFile(ctx, tx.Q(), file)
		if err != nil {
			return errNotFound(err, "finalized attachment unavailable")
		}
		if execution != "" && conversation != "main" {
			var issue string
			if x.coordinationExecution != nil {
				issue = x.coordinationExecution.IssueID
			}
			primary, err := store.GetTaskIssueOwner(ctx, tx.Q(), result.TaskID)
			if err != nil {
				return err
			}
			if issue == "" || issue != primary {
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
	if execution == "" {
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
