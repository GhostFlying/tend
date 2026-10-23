package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"strings"
	"unicode/utf8"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

const maxConversationImageBytes = 5 << 20

func validateConversationImages(inputs []protocol.ConversationImageInput) ([]store.ConversationImageRow, error) {
	if len(inputs) > 4 {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "a message supports at most 4 images")
	}
	images := make([]store.ConversationImageRow, 0, len(inputs))
	for _, in := range inputs {
		if strings.TrimSpace(in.Name) == "" || len(in.Name) > 255 || !utf8.ValidString(in.Name) {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "image name must be non-empty UTF-8 of at most 255 bytes")
		}
		if len(in.DataBase64) > base64.StdEncoding.EncodedLen(maxConversationImageBytes) {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "image exceeds 5 MiB")
		}
		data, err := base64.StdEncoding.Strict().DecodeString(in.DataBase64)
		if err != nil || len(data) == 0 || len(data) > maxConversationImageBytes {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "image must contain valid base64 of at most 5 MiB")
		}
		cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
		media := map[string]string{"png": "image/png", "jpeg": "image/jpeg", "gif": "image/gif"}[format]
		if err != nil || media == "" || media != in.MediaType {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "image content must match PNG, JPEG or GIF media_type")
		}
		// Bound the allocation needed to validate compressed image contents.
		if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width) > 40_000_000/int64(cfg.Height) {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "image exceeds 40 million pixels")
		}
		if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "image content is incomplete or invalid")
		}
		images = append(images, store.ConversationImageRow{ConversationImage: protocol.ConversationImage{Name: in.Name, MediaType: media, Size: int64(len(data))}, Data: data})
	}
	return images, nil
}

// ConversationImage returns retained image content to authenticated owner reads.
func (a *App) ConversationImage(ctx context.Context, id string) (out protocol.ConversationImageData, err error) {
	err = a.store.Read(ctx, func(tx *store.ReadTx) error {
		r, e := store.GetConversationImage(ctx, tx.Q(), id)
		if e != nil {
			return errNotFound(e, "image does not exist")
		}
		out = protocol.ConversationImageData{ImageID: r.ImageID, Name: r.Name, MediaType: r.MediaType, Size: r.Size, DataBase64: base64.StdEncoding.EncodeToString(r.Data)}
		return nil
	})
	return out, err
}

// MessageImageURLs loads original bytes for native model input, never context text.
func (a *App) MessageImageURLs(ctx context.Context, messageID string) (out []string, firstFrameOnly bool, err error) {
	err = a.store.Read(ctx, func(tx *store.ReadTx) error {
		images, e := store.ConversationImages(ctx, tx.Q(), messageID)
		if e != nil {
			return e
		}
		for _, i := range images {
			r, e := store.GetConversationImage(ctx, tx.Q(), i.ImageID)
			if e != nil {
				return e
			}
			if r.MediaType == "image/gif" {
				frame, _, err := image.Decode(bytes.NewReader(r.Data))
				if err != nil {
					return err
				}
				var encoded bytes.Buffer
				if err := png.Encode(&encoded, frame); err != nil {
					return err
				}
				r.Data, r.MediaType = encoded.Bytes(), "image/png"
				firstFrameOnly = true
			}
			out = append(out, "data:"+r.MediaType+";base64,"+base64.StdEncoding.EncodeToString(r.Data))
		}
		return nil
	})
	return out, firstFrameOnly, err
}
