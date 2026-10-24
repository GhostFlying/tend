package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func testConversationImage(t *testing.T) protocol.ConversationImageInput {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return protocol.ConversationImageInput{Name: "test.png", MediaType: "image/png", DataBase64: base64.StdEncoding.EncodeToString(b.Bytes())}
}

func TestConversationImagesAtomicDedupAndNativeBytes(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	image := testConversationImage(t)
	principal := protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "web"}
	env := h.env("conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{Images: []protocol.ConversationImageInput{image, image}}, nil)
	first := h.exec(principal, env, SubmitMessage{})
	second := h.exec(principal, env, SubmitMessage{})
	if first.Status != protocol.OpAccepted || !bytes.Equal(first.Result, second.Result) {
		t.Fatalf("dedup result: %+v %+v", first, second)
	}
	if h.count("conversation_messages") != 1 || h.count("conversation_images") != 2 {
		t.Fatal("message/image duplicated")
	}
	var result struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(first.Result, &result); err != nil {
		t.Fatal(err)
	}
	urls, err := h.app.MessageImageURLs(ctx, result.MessageID)
	if err != nil || len(urls) != 2 || urls[0] != "data:image/png;base64,"+image.DataBase64 {
		t.Fatalf("native images: %v %v", urls, err)
	}
	if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
		images, err := store.ConversationImages(ctx, tx.Q(), result.MessageID)
		if err != nil {
			return err
		}
		data, err := h.app.ConversationImage(ctx, images[0].ImageID)
		if err != nil {
			return err
		}
		if data.DataBase64 != image.DataBase64 {
			t.Error("download bytes changed")
		}
		var body, events string
		if err := tx.Q().QueryRowContext(ctx, `SELECT body FROM conversation_messages WHERE message_id=?`, result.MessageID).Scan(&body); err != nil {
			return err
		}
		if err := tx.Q().QueryRowContext(ctx, `SELECT group_concat(body_json) FROM events WHERE object_id=?`, result.MessageID).Scan(&events); err != nil {
			return err
		}
		if body != "" || strings.Contains(events, image.DataBase64) {
			t.Error("image encoded into text/event")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestConversationImageValidationRollsBackWholeMessage(t *testing.T) {
	good := testConversationImage(t)
	for _, test := range []struct {
		name   string
		images []protocol.ConversationImageInput
	}{
		{"bad_base64", []protocol.ConversationImageInput{{Name: "x", MediaType: "image/png", DataBase64: "??"}}},
		{"mismatch", []protocol.ConversationImageInput{{Name: "x", MediaType: "image/jpeg", DataBase64: good.DataBase64}}},
		{"valid_then_invalid", []protocol.ConversationImageInput{good, {Name: "x", MediaType: "image/png", DataBase64: base64.StdEncoding.EncodeToString([]byte("not an image"))}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			op := h.exec(protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "test"}, h.env("conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{Body: "text", Images: test.images}, nil), SubmitMessage{})
			if op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeInvalidRequest {
				t.Fatalf("bad input accepted: %+v", op)
			}
			if h.count("conversation_messages") != 0 || h.count("conversation_images") != 0 {
				t.Fatal("partial message persisted")
			}
		})
	}
}

func TestImageChangesInvalidateSummaryAndRetainMetadataOnly(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.app.BindMainThread(ctx, "main", "source", "tools", ""); err != nil {
		t.Fatal(err)
	}
	in := testConversationImage(t)
	raw, _ := base64.StdEncoding.DecodeString(in.DataBase64)
	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		if _, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: "image_notice", Author: "system"}); err != nil {
			return err
		}
		return tx.InsertConversationImage(ctx, "image_notice", 0, store.ConversationImageRow{ConversationImage: protocol.ConversationImage{ImageID: "image_1", Name: in.Name, MediaType: in.MediaType}, Data: raw})
	}); err != nil {
		t.Fatal(err)
	}
	first, err := h.app.HandoffSummaryInput(ctx, "main", 256<<10)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(first.Observations), `"image_id":"image_1"`) || strings.Contains(string(first.Observations), in.DataBase64) {
		t.Fatalf("summary attachment metadata: %s", first.Observations)
	}
	if _, err := h.app.SaveHandoffSummary(ctx, store.HandoffSummary{Instance: "main", Snapshot: first.Snapshot, Body: "image retained, contents in original history", FormatVersion: 2}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE conversation_images SET name='renamed.png' WHERE image_id='image_1'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	next, err := h.app.HandoffSummaryInput(ctx, "main", 256<<10)
	if err != nil {
		t.Fatal(err)
	}
	if first.Snapshot.Covers(next.Snapshot) || !strings.Contains(string(next.Observations), "renamed.png") {
		t.Fatal("image mutation falsely covered")
	}
}
