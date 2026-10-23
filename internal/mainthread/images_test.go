package mainthread

import (
	"bytes"
	"encoding/base64"
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
	eventually(t, "image turn completes", func() bool { return e.count(`SELECT COUNT(*) FROM main_turns WHERE status='completed'`) == 1 })
	starts := h.Starts()
	if len(starts) != 1 || len(starts[0].ImageURLs) != 1 || starts[0].ImageURLs[0] != "data:image/png;base64,"+encoded || strings.Contains(starts[0].Text, encoded) {
		t.Fatalf("native image not separate from text: %+v", starts)
	}
}
