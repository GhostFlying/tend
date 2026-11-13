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
	// A prior chat destination does not replace the original upload/result
	// scope. Reuse the same visibility for first publication and supplements.
	if err == nil && x.coordinationExecution != nil && x.coordinationExecution.IssueID != "" {
		original, checkErr := store.GetDeliverableFile(ctx, tx.Q(), file)
		if checkErr != nil && !errors.Is(checkErr, store.ErrNotFound) {
			return checkErr
		}
		if checkErr == nil {
			allowed, checkErr := communicationUploadInScope(ctx, tx.Q(), x, x.coordinationExecution.IssueID, original)
			if checkErr != nil {
				return checkErr
			}
			if allowed {
				return nil
			}
		}
	}
	if errors.Is(err, store.ErrNotFound) {
		result, err := store.GetDeliverableFile(ctx, tx.Q(), file)
		if err != nil {
			return errNotFound(err, "attachment original unavailable")
		}
		if execution != "" && x.Principal.Kind != protocol.PrincipalOwner && conversation != "main" {
			var issue string
			if x.coordinationExecution != nil {
				issue = x.coordinationExecution.IssueID
			}
			allowed, err := communicationUploadInScope(ctx, tx.Q(), x, issue, result)
			if err != nil {
				return err
			}
			if !allowed {
				return protocol.Errorf(protocol.CodeForbidden, "attachment is outside this conversation or Issue")
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

// Ordinary publication is separate from formal artifact/acceptance provenance.
// Original uploads of this Issue remain usable by its current responsible
// conversation after the producer finishes; no old Run credential is revived.
func communicationUploadInScope(ctx context.Context, q store.Querier, x *Exec, issue string, file store.DeliverableFileRow) (bool, error) {
	if allowed, err := communicationResultInScope(ctx, q, issue, file.FileID); err != nil || allowed {
		return allowed, err
	}
	if x.coordinationExecution != nil && x.coordinationExecution.Selection.RunnerRunID != "" {
		if compatible, err := store.CompatibleFileProducer(ctx, q, file, x.coordinationExecution.Selection.RunnerRunID); err != nil || compatible {
			return compatible, err
		}
	}
	var allowed bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_work_item_owners WHERE task_id=? AND work_item_id=? AND ?<>'') OR EXISTS(SELECT 1 FROM issue_manager_tasks WHERE task_id=? AND issue_id=? AND ?<>'')`, file.TaskID, issue, issue, file.TaskID, issue, issue).Scan(&allowed)
	return allowed, err
}
