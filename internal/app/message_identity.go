package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// existingMessage checks the actual immutable input and its original files.
func existingMessage(ctx context.Context, q store.Querier, p protocol.SubmitMessagePayload, author string, images []store.ConversationImageRow) (*store.MessageView, error) {
	if !protocol.ValidID(protocol.PrefixMessage, p.MessageID) {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "message_id must identify the new message; retain it across retries")
	}
	current, err := store.GetMessage(ctx, q, p.MessageID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	conversation, err := store.MessageConversation(ctx, q, p.MessageID)
	if err != nil {
		return nil, err
	}
	if conversation != store.ConversationID(p.ConversationID) || current.Author != author || current.Body != p.Body || current.ReplyTo != p.ReplyTo {
		return nil, protocol.Errorf(protocol.CodeConflictPayload, "message identity already has another immutable input")
	}
	attachments, err := store.ConversationAttachments(ctx, q, p.MessageID)
	if err != nil {
		return nil, err
	}
	if len(attachments) != len(images)+len(p.Attachments) {
		return nil, protocol.Errorf(protocol.CodeConflictPayload, "message files differ")
	}
	for i, img := range images {
		sum := sha256.Sum256(img.Data)
		if attachments[i].Name != img.Name || attachments[i].MediaType != img.MediaType || attachments[i].SHA256 != hex.EncodeToString(sum[:]) {
			return nil, protocol.Errorf(protocol.CodeConflictPayload, "message image differs")
		}
	}
	for i, id := range p.Attachments {
		if attachments[len(images)+i].FileID != id {
			return nil, protocol.Errorf(protocol.CodeConflictPayload, "message attachment differs")
		}
	}
	return &current, nil
}
