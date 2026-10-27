package mainthread

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestFault_SteerPreparationFailureDoesNotCallNative(t *testing.T) {
	h := testkit.NewHarness()
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	defer close(release)
	h.Turn = func(*testkit.FakeThread, string, harness.TurnInput) string {
		started <- struct{}{}
		<-release
		return ""
	}
	var nativeCalls atomic.Int32
	e := newEnv(t, h, withSteer(h, func(context.Context, harness.Thread, string, harness.TurnInput) error {
		nativeCalls.Add(1)
		return nil
	}))
	e.say("original goal")
	<-started
	var raw bytes.Buffer
	if err := png.Encode(&raw, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	op, err := e.app.UploadConversationAttachment(t.Context(), owner, e.ids.New(protocol.PrefixRequest), "unreadable.png", "image/png", &raw)
	if err != nil {
		t.Fatal(err)
	}
	var file protocol.ConversationAttachment
	if err := json.Unmarshal(op.Result, &file); err != nil {
		t.Fatal(err)
	}
	path, err := e.store.FileStore().Path(file.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	// Only this test's isolated original is made unreadable. Submission
	// retains its metadata and message; preparation cannot read its bytes.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	var message struct {
		ID string `json:"message_id"`
	}
	if err := json.Unmarshal(e.exec(owner, "conversation.submit_message", protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{Body: "supplement retained", Attachments: []string{file.FileID}}, app.SubmitMessage{}), &message); err != nil {
		t.Fatal(err)
	}
	eventually(t, "preparation waiting", func() bool {
		return e.count(`SELECT COUNT(*) FROM main_steers s JOIN outbox o ON o.request_id=s.request_id WHERE s.message_id=? AND s.status='waiting' AND o.status='pending'`, message.ID) == 1
	})
	var requestID string
	if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT request_id FROM main_steers WHERE message_id=?`, message.ID).Scan(&requestID)
	}); err != nil {
		t.Fatal(err)
	}
	steer, ok, err := e.app.MainSteer(t.Context(), requestID)
	if err != nil || !ok || !strings.HasPrefix(steer.Reason, "attachment or message preparation failed") || steer.NativeItemID != "" || nativeCalls.Load() != 0 {
		t.Fatalf("preparation misreported as native failure: %+v %v calls=%d", steer, err, nativeCalls.Load())
	}
	if e.count(`SELECT COUNT(*) FROM conversation_messages WHERE message_id=? AND body='supplement retained'`, message.ID) != 1 ||
		e.count(`SELECT COUNT(*) FROM outbox WHERE request_id=? AND status='pending'`, steer.RequestID) != 1 {
		t.Fatal("preparation failure lost the queued original")
	}
}
