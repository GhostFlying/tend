package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestOrdinaryNotifyCLIOptionalMarkerAndDeliveryActionError(t *testing.T) {
	f := newFakeServer(t)
	e := newTestEnv(t, f.srv.URL)
	if code := e.main("message", "notify", "--data", `{"attachments":["original-file"]}`, "--dry-run"); code != exitOK {
		t.Fatal(e.errOut.String())
	}
	var env protocol.Envelope
	var p protocol.MessagingPublication
	if json.Unmarshal(e.out.Bytes(), &env) != nil || json.Unmarshal(env.Payload, &p) != nil || p.Purpose != "" || len(p.Attachments) != 1 {
		t.Fatal("CLI changed ordinary message", e.out.String())
	}
	e.out.Reset()
	e.errOut.Reset()
	if code := e.main("issue", "update-delivery", "issue", "--revision", "2", "--epoch", "3", "--data", `{"action":"bind","task_id":"source","execution_task_id":"same","reason":"Continue actual Task"}`, "--dry-run"); code == exitOK || !strings.Contains(e.errOut.String(), "expected_task_control_revision") {
		t.Fatal("CLI hid action input condition", code, e.errOut.String())
	}
}
