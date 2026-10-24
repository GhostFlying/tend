package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// validateConversationImages accepts old inline-image clients. New uploads use
// streaming attachments; image decoding and resolution never gate retention.
func validateConversationImages(inputs []protocol.ConversationImageInput) ([]store.ConversationImageRow, error) {
	images := make([]store.ConversationImageRow, 0, len(inputs))
	for _, in := range inputs {
		if strings.TrimSpace(in.Name) == "" || len(in.Name) > 255 || !utf8.ValidString(in.Name) {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "invalid image name")
		}
		data, err := base64.StdEncoding.Strict().DecodeString(in.DataBase64)
		if err != nil || len(data) == 0 {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "image must contain valid base64")
		}
		_, format, err := image.DecodeConfig(bytes.NewReader(data))
		media := map[string]string{"png": "image/png", "jpeg": "image/jpeg", "gif": "image/gif"}[format]
		if err != nil || media == "" || media != in.MediaType {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "image content must match PNG, JPEG or GIF media_type")
		}
		images = append(images, store.ConversationImageRow{ConversationImage: protocol.ConversationImage{Name: in.Name, MediaType: media, Size: int64(len(data))}, Data: data})
	}
	return images, nil
}

// ConversationImage serves the old JSON image endpoint. New clients stream the
// attachment endpoint instead; only legacy inline messages populate this table.
func (a *App) ConversationImage(ctx context.Context, id string) (out protocol.ConversationImageData, err error) {
	var row store.ConversationImageRow
	err = a.store.Read(ctx, func(tx *store.ReadTx) error {
		var e error
		row, e = store.GetConversationImage(ctx, tx.Q(), id)
		return errNotFound(e, "image does not exist")
	})
	if err != nil {
		return
	}
	f, e := a.store.FileStore().Open(row.SHA256)
	if e != nil {
		return out, e
	}
	defer f.Close()
	data, e := io.ReadAll(f)
	if e != nil {
		return out, e
	}
	out = protocol.ConversationImageData{ImageID: row.ImageID, Name: row.Name, MediaType: row.MediaType, Size: row.Size, DataBase64: base64.StdEncoding.EncodeToString(data)}
	return
}

// MessageImageURLs offers a bounded automatic vision input for static images.
// Every original remains accessible through MessageAttachmentReferences regardless
// of this convenience budget. GIF analysis is left to the agent using the original.
func (a *App) MessageImageURLs(ctx context.Context, messageID string) (out []string, err error) {
	refs, err := a.MessageAttachmentReferences(ctx, messageID)
	if err != nil {
		return nil, err
	}
	remaining := int64(16 << 20)
	for _, r := range refs {
		if r.MediaType != "image/png" && r.MediaType != "image/jpeg" {
			continue
		}
		if r.Size > remaining {
			continue
		}
		f, e := a.store.FileStore().Open(r.SHA256)
		if e != nil {
			return nil, e
		}
		config, _, decodeErr := image.DecodeConfig(f)
		if decodeErr != nil || config.Width <= 0 || config.Height <= 0 || int64(config.Width) > 16_000_000/int64(config.Height) {
			f.Close()
			continue
		}
		if _, e = f.Seek(0, io.SeekStart); e != nil {
			f.Close()
			return nil, e
		}
		data, e := io.ReadAll(io.LimitReader(f, remaining+1))
		f.Close()
		if e != nil {
			return nil, e
		}
		if int64(len(data)) > remaining {
			continue
		}
		if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
			continue // Invalid previews must not prevent access to the original attachment.
		}
		remaining -= int64(len(data))
		out = append(out, "data:"+r.MediaType+";base64,"+base64.StdEncoding.EncodeToString(data))
	}
	return out, nil
}
