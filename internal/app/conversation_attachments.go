package app

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"os"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

const OpUploadAttachment = "conversation.upload_attachment"

type uploadAttachment struct {
	file protocol.ConversationAttachment
}

func (uploadAttachment) Operation() string { return OpUploadAttachment }
func (uploadAttachment) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}
func (c uploadAttachment) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	f := c.file
	f.FileID = x.IDs.New(protocol.PrefixFile)
	if err := tx.InsertConversationAttachment(ctx, store.ConversationAttachmentRow{ConversationAttachment: f, CreatedAt: x.Now.UnixMilli()}); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: f, Events: []Event{{ObjectType: "file", ObjectID: f.FileID, Kind: "conversation.attachment_uploaded", Body: map[string]any{"name": f.Name, "size": f.Size, "sha256": f.SHA256}}}}, nil
}

// UploadConversationAttachment stages immutable bytes before committing their identity.
// No message or request payload contains the uploaded file content.
func (a *App) UploadConversationAttachment(ctx context.Context, p protocol.Principal, requestID, name, mediaType string, body io.Reader) (protocol.Operation, error) {
	if err := allowKinds(p, protocol.PrincipalOwner); err != nil {
		return protocol.Operation{}, err
	}
	if !protocol.ValidID(protocol.PrefixRequest, requestID) {
		return protocol.Operation{}, protocol.Errorf(protocol.CodeInvalidRequest, "a valid request_id is required")
	}
	if strings.TrimSpace(name) == "" || name == "." || name == ".." || path.Base(name) != name || strings.ContainsAny(name, "\\\r\n\x00") || len(name) > 255 || !utf8.ValidString(name) {
		return protocol.Operation{}, protocol.Errorf(protocol.CodeInvalidRequest, "invalid attachment name")
	}
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	media, _, err := mime.ParseMediaType(mediaType)
	if err != nil {
		return protocol.Operation{}, protocol.Errorf(protocol.CodeInvalidRequest, "invalid media_type")
	}
	obj, err := a.store.FileStore().Put(ctx, body)
	if err != nil {
		return protocol.Operation{}, err
	}
	f := protocol.ConversationAttachment{Name: name, MediaType: media, Size: obj.Size, SHA256: obj.SHA256}
	payload, err := json.Marshal(f)
	if err != nil {
		return protocol.Operation{}, err
	}
	env := protocol.Envelope{RequestID: requestID, Operation: OpUploadAttachment, Target: protocol.Target{Type: "conversation"}, Payload: payload}
	return a.Execute(ctx, p, &env, uploadAttachment{file: f})
}

// ConversationAttachment returns metadata without loading original bytes.
func (a *App) ConversationAttachment(ctx context.Context, id string) (out protocol.ConversationAttachment, err error) {
	err = a.store.Read(ctx, func(tx *store.ReadTx) error {
		r, e := store.GetConversationAttachment(ctx, tx.Q(), id)
		out = r.ConversationAttachment
		return errNotFound(e, "attachment does not exist")
	})
	return
}

// OpenConversationAttachment opens an immutable original for a caller authorized by the API.
func (a *App) OpenConversationAttachment(ctx context.Context, id string) (protocol.ConversationAttachment, *os.File, error) {
	f, err := a.ConversationAttachment(ctx, id)
	if err != nil {
		return f, nil, err
	}
	stream, err := a.store.FileStore().Open(f.SHA256)
	return f, stream, err
}

// MessageAttachmentReferences retains original paths independently of automatic vision input.
func (a *App) MessageAttachmentReferences(ctx context.Context, messageID string) ([]AttachmentReference, error) {
	var result []AttachmentReference
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		files, err := store.ConversationAttachments(ctx, tx.Q(), messageID)
		if err != nil {
			return err
		}
		for _, f := range files {
			path, err := a.store.FileStore().Path(f.SHA256)
			if err != nil {
				return err
			}
			result = append(result, AttachmentReference{ConversationAttachment: f, LocalPath: path})
		}
		return nil
	})
	return result, err
}

// AttachmentReference points the hosted agent at original bytes, never at an inferred interpretation.
type AttachmentReference struct {
	protocol.ConversationAttachment
	LocalPath string `json:"local_path"`
}
