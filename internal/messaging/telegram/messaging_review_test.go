package telegram

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/GhostFlying/tend/internal/app"
)

func TestReviewCaptionedUnsupportedMedia(t *testing.T) {
	c, a, _ := testTelegram(t, func(w http.ResponseWriter, r *http.Request) { botSetup(w, r) })
	raw := json.RawMessage(`{"update_id":1,"message":{"message_id":1,"from":{"id":42},"chat":{"id":42,"type":"private"},"text":"/start"}}`)
	raw2 := json.RawMessage(`{"update_id":2,"message":{"message_id":2,"from":{"id":42},"chat":{"id":42,"type":"private"},"caption":"Review the video evidence","video":{"file_id":"video-a","width":100,"height":100,"duration":1}}}`)
	if err := a.PersistTelegramBatch(t.Context(), 777, []app.TelegramInbox{{UpdateID: 1, Payload: raw}, {UpdateID: 2, Payload: raw2}}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		u, ok, err := a.PendingTelegram(t.Context(), 777)
		if err != nil || !ok {
			t.Fatal(ok, err)
		}
		if err = c.process(t.Context(), u); err != nil {
			t.Fatal(err)
		}
	}
	page, err := a.MessagingMessages(t.Context(), 0, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range page.Items {
		if message.Direction == "input" {
			t.Fatalf("unsupported video admitted without evidence: %+v", message)
		}
	}
	if len(page.Items) != 1 || page.Items[0].Direction != "notification" {
		t.Fatal("unsupported input should receive a factual rejection", page)
	}
}

func TestReviewInvalidFileDoesNotBlockInbox(t *testing.T) {
	c, a, _ := testTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		if botSetup(w, r) {
			return
		}
		switch r.URL.Path {
		case "/bottest-token/getFile":
			io.WriteString(w, `{"ok":true,"result":{"file_id":"file-a","file_path":"files/source.txt","file_size":8}}`)
		case "/file/bottest-token/files/source.txt":
			io.WriteString(w, "original")
		}
	})
	raw := json.RawMessage(`{"update_id":1,"message":{"message_id":1,"from":{"id":42},"chat":{"id":42,"type":"private"},"text":"/start"}}`)
	raw2 := json.RawMessage(`{"update_id":2,"message":{"message_id":2,"from":{"id":42},"chat":{"id":42,"type":"private"},"document":{"file_id":"file-a","file_name":"notes\\2026.txt","mime_type":"text/plain","file_size":8}}}`)
	if err := a.PersistTelegramBatch(t.Context(), 777, []app.TelegramInbox{{UpdateID: 1, Payload: raw}, {UpdateID: 2, Payload: raw2}}); err != nil {
		t.Fatal(err)
	}
	u, _, _ := a.PendingTelegram(t.Context(), 777)
	if err := c.process(t.Context(), u); err != nil {
		t.Fatal(err)
	}
	u, _, _ = a.PendingTelegram(t.Context(), 777)
	err := c.process(t.Context(), u)
	_, ok, pendingErr := a.PendingTelegram(t.Context(), 777)
	if err != nil && ok && pendingErr == nil {
		t.Fatalf("permanent bad filename is retried forever: %v", err)
	}
}

func TestMessagingPermanentFile404DoesNotBlockInbox(t *testing.T) {
	c, a, _ := testTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		if botSetup(w, r) {
			return
		}
		if r.URL.Path == "/bottest-token/getFile" {
			io.WriteString(w, `{"ok":true,"result":{"file_id":"file-a","file_path":"files/missing.txt","file_size":8}}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	raws := []json.RawMessage{
		json.RawMessage(`{"update_id":1,"message":{"message_id":1,"from":{"id":42},"chat":{"id":42,"type":"private"},"text":"/start"}}`),
		json.RawMessage(`{"update_id":2,"message":{"message_id":2,"from":{"id":42},"chat":{"id":42,"type":"private"},"document":{"file_id":"file-a","file_name":"missing.txt","mime_type":"text/plain","file_size":8}}}`),
		json.RawMessage(`{"update_id":3,"message":{"message_id":3,"from":{"id":42},"chat":{"id":42,"type":"private"},"text":"A later valid message"}}`)}
	var batch []app.TelegramInbox
	for i, raw := range raws {
		batch = append(batch, app.TelegramInbox{UpdateID: int64(i + 1), Payload: raw})
	}
	if err := a.PersistTelegramBatch(t.Context(), 777, batch); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		u, _, _ := a.PendingTelegram(t.Context(), 777)
		_ = c.process(t.Context(), u)
	}
	u, ok, err := a.PendingTelegram(t.Context(), 777)
	if err != nil {
		t.Fatal(err)
	}
	if ok && u.UpdateID == 2 {
		t.Fatal("permanent download 404 leaves inbox head pending; update 3 cannot proceed")
	}
}
