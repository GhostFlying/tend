package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func testTelegram(t *testing.T, handler http.HandlerFunc) (*Client, *app.App, protocol.IDGen) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	clock := testkit.NewClock()
	ids := testkit.NewIDGen(clock, 55)
	s, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "test.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	a := app.New(s, ids, clock.Now)
	c, err := New(a, "test-token", server.Client(), server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(protocol.BindTelegram{UserID: 42})
	rev := int64(2)
	op, err := a.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "test"}, &protocol.Envelope{RequestID: ids.New(protocol.PrefixRequest), Operation: "messaging.bind", Target: protocol.Target{Type: "channel", ID: "telegram"}, ExpectedControlRevision: &rev, Payload: raw}, app.MessagingCommand{Action: "bind"})
	if err != nil || op.Error != nil {
		t.Fatal(op, err)
	}
	return c, a, ids
}
func botSetup(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/bottest-token/getMe":
		io.WriteString(w, `{"ok":true,"result":{"id":777,"is_bot":true,"first_name":"Test"}}`)
		return true
	case "/bottest-token/getWebhookInfo":
		io.WriteString(w, `{"ok":true,"result":{"url":""}}`)
		return true
	}
	return false
}
func TestTelegramPollingDurableReplayAndMedia(t *testing.T) {
	var polls atomic.Int64
	c, a, _ := testTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		if botSetup(w, r) {
			return
		}
		switch r.URL.Path {
		case "/bottest-token/getUpdates":
			var p struct {
				Offset int64 `json:"offset"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			if polls.Add(1) == 1 && p.Offset != 0 {
				t.Errorf("initial offset=%d", p.Offset)
			}
			if polls.Load() > 1 && p.Offset != 4 {
				t.Errorf("offset was not durable: %d", p.Offset)
			}
			io.WriteString(w, `{"ok":true,"result":[{"update_id":1,"message":{"message_id":1,"from":{"id":42},"chat":{"id":42,"type":"private"},"text":"/start"}},{"update_id":2,"message":{"message_id":2,"from":{"id":42},"chat":{"id":42,"type":"private"},"text":"hello"}},{"update_id":3,"message":{"message_id":3,"from":{"id":42},"chat":{"id":42,"type":"private"},"document":{"file_id":"file-a","file_name":"original.txt","mime_type":"text/plain","file_size":8},"caption":"evidence"}}]}`)
		case "/bottest-token/getFile":
			io.WriteString(w, `{"ok":true,"result":{"file_id":"file-a","file_path":"files/original.txt","file_size":8}}`)
		case "/file/bottest-token/files/original.txt":
			io.WriteString(w, "original")
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	if err := c.poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	if offset, err := a.TelegramOffset(t.Context(), 777); err != nil || offset != 4 {
		t.Fatal(offset, err)
	}
	for range 3 {
		u, ok, err := a.PendingTelegram(t.Context(), 777)
		if err != nil || !ok {
			t.Fatal(u, ok, err)
		}
		if err = c.process(t.Context(), u); err != nil {
			t.Fatal(err)
		}
	}
	page, err := a.MessagingMessages(t.Context(), 0, "", "", "telegram")
	if err != nil || len(page.Items) != 2 {
		t.Fatal(page, err)
	}
	if len(page.Items[1].Attachments) != 1 || page.Items[1].Attachments[0].Size != 8 {
		t.Fatal(page.Items[1])
	}
	if err = c.poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := a.PendingTelegram(t.Context(), 777); err != nil || ok {
		t.Fatal("replayed processed update", ok, err)
	}
}
func TestTelegramOutgoingStreamingAndUnknown(t *testing.T) {
	var sends atomic.Int64
	c, a, ids := testTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		if botSetup(w, r) {
			return
		}
		if strings.HasSuffix(r.URL.Path, "sendMessage") {
			sends.Add(1)
			io.WriteString(w, `{"ok":true,"result":{"message_id":20,"chat":{"id":42,"type":"private"}}}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "sendDocument") {
			if r.ContentLength != -1 {
				t.Error("upload buffered instead of streaming")
			}
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
			}
			if r.MultipartForm != nil {
				defer r.MultipartForm.RemoveAll()
			}
			file, _, err := r.FormFile("document")
			if err != nil {
				t.Error(err)
			} else {
				defer file.Close()
				raw, _ := io.ReadAll(file)
				if string(raw) != "unchanged original" {
					t.Error(string(raw))
				}
			}
			io.WriteString(w, `{"ok":true,"result":{"message_id":21,"chat":{"id":42,"type":"private"}}}`)
			return
		}
		w.WriteHeader(404)
	})
	u := app.TelegramInbox{BotID: 777, UpdateID: 1, Payload: json.RawMessage(`{"update_id":1}`)}
	if err := a.PersistTelegramBatch(t.Context(), 777, []app.TelegramInbox{u}); err != nil {
		t.Fatal(err)
	}
	u, _, _ = a.PendingTelegram(t.Context(), 777)
	if op, err := a.ReceiveTelegram(t.Context(), u, app.TelegramIncoming{UserID: 42, ChatID: 42, Start: true}); err != nil || op.Error != nil {
		t.Fatal(op, err)
	}
	owner := protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "test"}
	uploaded, err := a.UploadConversationAttachment(t.Context(), owner, ids.New(protocol.PrefixRequest), "original.txt", "text/plain", strings.NewReader("unchanged original"))
	if err != nil {
		t.Fatal(err)
	}
	var file protocol.ConversationAttachment
	_ = json.Unmarshal(uploaded.Result, &file)
	payload, _ := json.Marshal(protocol.MessagingPublication{Body: "Result", Purpose: "final", Reason: "verified_result", Attachments: []string{file.FileID}})
	op, err := a.Execute(t.Context(), owner, &protocol.Envelope{RequestID: ids.New(protocol.PrefixRequest), Operation: "messaging.notify", Target: protocol.Target{Type: "message"}, Payload: payload}, app.MessagingCommand{Action: "notify"})
	if err != nil || op.Error != nil {
		t.Fatal(op, err)
	}
	for range 2 {
		d, _, ok, err := a.NextDelivery(t.Context(), app.MessagingDestination)
		if err != nil || !ok {
			t.Fatal(ok, err)
		}
		r := c.Deliver(t.Context(), d)
		if r.Outcome != app.Delivered {
			t.Fatal(r)
		}
		if err = a.FinishDelivery(t.Context(), d, r); err != nil {
			t.Fatal(err)
		}
	}
	if sends.Load() != 1 {
		t.Fatal(sends.Load())
	}
	if r := c.Reconcile(t.Context(), app.Delivery{}); r.Outcome != app.Unknown {
		t.Fatal(r)
	}
}
func TestTelegramRejectsWebhook(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "getWebhookInfo") {
			io.WriteString(w, `{"ok":true,"result":{"url":"https://example.invalid/hook"}}`)
			return
		}
		botSetup(w, r)
	}))
	defer s.Close()
	c, err := New(nil, "test-token", s.Client(), s.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err = c.Prepare(ctx); err == nil || !strings.Contains(err.Error(), "webhook") {
		t.Fatal(err)
	}
}

func TestTelegramPhotoPreservesBytesAndForwardedAuthorship(t *testing.T) {
	original := []byte("retained platform photo")
	var sent atomic.Bool
	c, a, ids := testTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		if botSetup(w, r) {
			return
		}
		switch r.URL.Path {
		case "/bottest-token/getFile":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
			}
			if r.MultipartForm != nil {
				defer r.MultipartForm.RemoveAll()
			}
			if r.FormValue("file_id") != "large-photo" {
				t.Error("did not select largest photo")
			}
			io.WriteString(w, `{"ok":true,"result":{"file_id":"large-photo","file_path":"photos/photo.jpg","file_size":23}}`)
		case "/file/bottest-token/photos/photo.jpg":
			w.Write(original)
		case "/bottest-token/sendPhoto":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
			}
			if r.MultipartForm != nil {
				defer r.MultipartForm.RemoveAll()
			}
			file, _, err := r.FormFile("photo")
			if err != nil {
				t.Error(err)
			} else {
				defer file.Close()
				raw, _ := io.ReadAll(file)
				if string(raw) != string(original) {
					t.Error("photo original changed")
				}
			}
			sent.Store(true)
			io.WriteString(w, `{"ok":true,"result":{"message_id":99,"chat":{"id":42,"type":"private"}}}`)
		default:
			w.WriteHeader(404)
		}
	})
	raw := json.RawMessage(`{"update_id":1,"message":{"message_id":1,"from":{"id":42},"chat":{"id":42,"type":"private"},"text":"/start"}}`)
	if err := a.PersistTelegramBatch(t.Context(), 777, []app.TelegramInbox{{UpdateID: 1, Payload: raw}}); err != nil {
		t.Fatal(err)
	}
	u, _, _ := a.PendingTelegram(t.Context(), 777)
	if err := c.process(t.Context(), u); err != nil {
		t.Fatal(err)
	}
	raw = json.RawMessage(`{"update_id":2,"message":{"message_id":2,"from":{"id":42},"chat":{"id":42,"type":"private"},"forward_origin":{"type":"hidden_user","date":123,"sender_user_name":"Original author"},"caption":"Reference photo","photo":[{"file_id":"small-photo","width":1,"height":1},{"file_id":"large-photo","width":2,"height":2}]}}`)
	if err := a.PersistTelegramBatch(t.Context(), 777, []app.TelegramInbox{{UpdateID: 2, Payload: raw}}); err != nil {
		t.Fatal(err)
	}
	u, _, _ = a.PendingTelegram(t.Context(), 777)
	if err := c.process(t.Context(), u); err != nil {
		t.Fatal(err)
	}
	page, err := a.MessagingMessages(t.Context(), 0, "", "", "")
	if err != nil || len(page.Items) != 1 || page.Items[0].Author != "external" || len(page.Items[0].Attachments) != 1 {
		t.Fatal(page, err)
	}
	source, err := a.ManualSource(t.Context(), page.Items[0].MessageID)
	if err != nil || source.SuppliedBy.Subject != "owner" {
		t.Fatal(source, err)
	}
	payload, _ := json.Marshal(protocol.MessagingPublication{Purpose: "final", Reason: "requested_original", Attachments: []string{page.Items[0].Attachments[0].FileID}})
	op, err := a.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "test"}, &protocol.Envelope{RequestID: ids.New(protocol.PrefixRequest), Operation: "messaging.notify", Target: protocol.Target{Type: "message"}, Payload: payload}, app.MessagingCommand{Action: "notify"})
	if err != nil || op.Error != nil {
		t.Fatal(op, err)
	}
	d, _, ok, err := a.NextDelivery(t.Context(), app.MessagingDestination)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if result := c.Deliver(t.Context(), d); result.Outcome != app.Delivered || !sent.Load() {
		t.Fatal(result)
	}
}
