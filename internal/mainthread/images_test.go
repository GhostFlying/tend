package mainthread

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestImageOnlyMessageReachesNativeTurn(t *testing.T) {
	h := testkit.NewHarness()
	h.Turn = func(th *testkit.FakeThread, turn string, in harness.TurnInput) string {
		th.Say(turn, "answer", "image received")
		return "completed"
	}
	e := newEnv(t, h)
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(b.Bytes())
	e.exec(owner, "conversation.submit_message", protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{Images: []protocol.ConversationImageInput{{Name: "x.png", MediaType: "image/png", DataBase64: encoded}}}, app.SubmitMessage{})
	eventually(t, "image turn completes", func() bool {
		return e.count(`SELECT COUNT(*) FROM main_turns WHERE request_id NOT IN (SELECT request_id FROM logical_turn_executions WHERE repair=1) AND status='completed'`) == 1
	})
	starts := businessStarts(h)
	if len(starts) != 1 || len(starts[0].ImageURLs) != 1 || starts[0].ImageURLs[0] != "data:image/png;base64,"+encoded || strings.Contains(starts[0].Text, encoded) {
		t.Fatalf("native image not separate from text: %+v", starts)
	}
}

func TestOriginalFileReachesNativeTurnWithoutInliningContent(t *testing.T) {
	h := testkit.NewHarness()
	h.Turn = func(th *testkit.FakeThread, turn string, in harness.TurnInput) string {
		th.Say(turn, "answer", "file received")
		return "completed"
	}
	e := newEnv(t, h)
	content := "original-private-content"
	upload, err := e.app.UploadConversationAttachment(context.Background(), owner, e.ids.New(protocol.PrefixRequest), "report.txt", "text/plain", strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	var file protocol.ConversationAttachment
	if err := json.Unmarshal(upload.Result, &file); err != nil {
		t.Fatal(err)
	}
	e.exec(owner, "conversation.submit_message", protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{Attachments: []string{file.FileID}}, app.SubmitMessage{})
	eventually(t, "file turn completes", func() bool {
		return e.count(`SELECT COUNT(*) FROM main_turns WHERE request_id NOT IN (SELECT request_id FROM logical_turn_executions WHERE repair=1) AND status='completed'`) == 1
	})
	starts := businessStarts(h)
	if len(starts) != 1 || len(starts[0].ImageURLs) != 0 || strings.Contains(starts[0].Text, content) || !strings.Contains(starts[0].Text, file.FileID) || !strings.Contains(starts[0].Text, `"local_path":`) {
		t.Fatalf("native file reference: %+v", starts)
	}
}
