package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestDeliveryAnswerCLISeparatesAuditReasonFromPureOption(t *testing.T) {
	f := newFakeServer(t)
	e := newTestEnv(t, f.srv.URL)
	if code := e.main("decide", "original-question", "--data", `{"version":1,"option":"approve"}`, "--request-id", continuationCLIRequest, "--reason", "Confirm the exact existing scope", "--dry-run"); code != exitOK {
		t.Fatal(code, e.errOut.String())
	}
	var env protocol.Envelope
	var p protocol.AnswerDecisionPayload
	if err := json.Unmarshal(e.out.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(env.Payload, &p); err != nil || env.RequestID != continuationCLIRequest || env.Reason != "Confirm the exact existing scope" || p.Version != 1 || p.Option != "approve" || p.Text != "" || len(f.seen("POST /api/v1/ops")) != 0 {
		t.Fatal("audit context changed approval or recovery identity", env, p, err)
	}
	for _, args := range [][]string{{"decide"}, {"attention", "respond"}, {"issue", "update-delivery"}} {
		help := commandUsage(args)
		for _, want := range []string{"option+text", "--reason", "same exact candidate", "unknown", "defer may include text"} {
			if !strings.Contains(help, want) {
				t.Fatalf("%v help omits %q", args, want)
			}
		}
	}
}
