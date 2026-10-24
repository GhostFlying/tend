package app

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func uploadChatFile(t *testing.T, h *harness, name, media string, raw []byte) protocol.ConversationAttachment {
	t.Helper()
	p := protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "web"}
	req := h.env(OpUploadAttachment, protocol.Target{Type: "conversation"}, nil, nil).RequestID
	op, err := h.app.UploadConversationAttachment(context.Background(), p, req, name, media, bytes.NewReader(raw))
	if err != nil || op.Status != protocol.OpSucceeded {
		t.Fatalf("upload: %+v %v", op, err)
	}
	var file protocol.ConversationAttachment
	if err := json.Unmarshal(op.Result, &file); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestAttachmentUploadReplayAndMessageReferences(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	p := protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "web"}
	raw := []byte(strings.Repeat("original log\n", 600000))
	req := h.env(OpUploadAttachment, protocol.Target{Type: "conversation"}, nil, nil).RequestID
	first, err := h.app.UploadConversationAttachment(ctx, p, req, "trace.log", "text/plain", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	replay, err := h.app.UploadConversationAttachment(ctx, p, req, "trace.log", "text/plain", bytes.NewReader(raw))
	if err != nil || !bytes.Equal(first.Result, replay.Result) {
		t.Fatalf("replay: %v", err)
	}
	if h.count("conversation_attachments") != 1 {
		t.Fatal("duplicate upload")
	}
	if _, err := h.app.UploadConversationAttachment(ctx, p, req, "trace.log", "text/plain", strings.NewReader("changed")); protocol.CodeOf(err) != protocol.CodeConflictPayload {
		t.Fatalf("different bytes reused identity: %v", err)
	}
	var file protocol.ConversationAttachment
	_ = json.Unmarshal(first.Result, &file)
	env := h.env("conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{Attachments: []string{file.FileID}}, nil)
	op := h.exec(p, env, SubmitMessage{})
	again := h.exec(p, env, SubmitMessage{})
	if op.Status != protocol.OpAccepted || !bytes.Equal(op.Result, again.Result) {
		t.Fatalf("message: %+v", op)
	}
	var message struct {
		MessageID string `json:"message_id"`
	}
	_ = json.Unmarshal(op.Result, &message)
	refs, err := h.app.MessageAttachmentReferences(ctx, message.MessageID)
	if err != nil || len(refs) != 1 {
		t.Fatalf("references: %v %v", refs, err)
	}
	saved, err := os.ReadFile(refs[0].LocalPath)
	if err != nil || !bytes.Equal(saved, raw) {
		t.Fatal("original file changed")
	}
	if h.count("conversation_messages") != 1 {
		t.Fatal("message duplicated")
	}
	invalid := h.exec(p, h.env("conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{Body: "bad", Attachments: []string{file.FileID, "missing"}}, nil), SubmitMessage{})
	if invalid.Status != protocol.OpFailed || h.count("conversation_messages") != 1 {
		t.Fatal("invalid reference partially committed")
	}
	duplicate := h.exec(p, h.env("conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{Attachments: []string{file.FileID, file.FileID}}, nil), SubmitMessage{})
	if duplicate.Status != protocol.OpFailed || duplicate.Error.Code != protocol.CodeInvalidRequest || h.count("conversation_messages") != 1 {
		t.Fatal("duplicate references not rejected atomically")
	}
	var contains int
	if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(ctx, `SELECT count(*) FROM request_records WHERE instr(COALESCE(result_json,''),'original log')>0`).Scan(&contains)
	}); err != nil || contains > 0 {
		t.Fatal("file bytes in request record")
	}
}

type solidLargeImage struct{}

func (solidLargeImage) ColorModel() color.Model { return color.RGBAModel }
func (solidLargeImage) Bounds() image.Rectangle { return image.Rect(0, 0, 10000, 5000) }
func (solidLargeImage) At(int, int) color.Color { return color.RGBA{R: 23, G: 45, B: 67, A: 255} }

func TestAttachmentOriginalResolutionAndAnimationPreserved(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	var large bytes.Buffer
	if err := png.Encode(&large, solidLargeImage{}); err != nil {
		t.Fatal(err)
	}
	picture := uploadChatFile(t, h, "large.png", "image/png", large.Bytes())
	palette := color.Palette{color.Black, color.White}
	first := image.NewPaletted(image.Rect(0, 0, 2, 2), palette)
	second := image.NewPaletted(image.Rect(0, 0, 2, 2), palette)
	second.SetColorIndex(0, 0, 1)
	var animation bytes.Buffer
	if err := gif.EncodeAll(&animation, &gif.GIF{Image: []*image.Paletted{first, second}, Delay: []int{5, 7}}); err != nil {
		t.Fatal(err)
	}
	gifFile := uploadChatFile(t, h, "animation.gif", "image/gif", animation.Bytes())
	p := protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "web"}
	op := h.exec(p, h.env("conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{Attachments: []string{picture.FileID, gifFile.FileID}}, nil), SubmitMessage{})
	var message struct {
		MessageID string `json:"message_id"`
	}
	_ = json.Unmarshal(op.Result, &message)
	urls, err := h.app.MessageImageURLs(ctx, message.MessageID)
	if err != nil || len(urls) != 0 {
		t.Fatalf("originals must be agent-selected: %v %v", urls, err)
	}
	for _, item := range []struct {
		file protocol.ConversationAttachment
		raw  []byte
	}{{picture, large.Bytes()}, {gifFile, animation.Bytes()}} {
		meta, f, err := h.app.OpenConversationAttachment(ctx, item.file.FileID)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(f)
		f.Close()
		if err != nil || !bytes.Equal(data, item.raw) || meta.Size != int64(len(item.raw)) {
			t.Fatal("original not retained")
		}
	}
}

func TestAttachmentEmptyUnknownAndUndecodableFilesRemainAvailable(t *testing.T) {
	h := newHarness(t)
	for _, c := range []struct {
		name, media string
		raw         []byte
	}{{"empty.txt", "text/plain", nil}, {"not-a-decoder-format.bin", "application/octet-stream", []byte{0, 1, 2, 255}}, {"unrenderable.png", "image/png", []byte("not decodable")}} {
		f := uploadChatFile(t, h, c.name, c.media, c.raw)
		_, stream, err := h.app.OpenConversationAttachment(context.Background(), f.FileID)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(stream)
		stream.Close()
		if err != nil || !bytes.Equal(data, c.raw) {
			t.Fatal("original changed")
		}
	}
}
