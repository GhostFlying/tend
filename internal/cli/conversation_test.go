package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestConversationHandoffOwnerAndPrepareOnly(t *testing.T) {
	for _, prepare := range []bool{false, true} {
		t.Run(map[bool]string{false: "handoff", true: "prepare"}[prepare], func(t *testing.T) {
			f := newFakeServer(t)
			f.answer("POST /api/v1/ops", protocol.APIResponse{Operation: &protocol.Operation{RequestID: "request", Status: protocol.OpAccepted}})
			te := newTestEnv(t, f.srv.URL)
			args := []string{"conversation", "handoff"}
			if prepare {
				args = append(args, "--prepare-only")
			}
			if code := te.main(args...); code != exitOK {
				t.Fatalf("handoff=%d %s", code, te.errOut)
			}
			sent := f.seen("POST /api/v1/ops")
			var env protocol.Envelope
			if len(sent) != 1 || json.Unmarshal(sent[0].body, &env) != nil || sent[0].auth != "Bearer owner-token" || env.Operation != "conversation.handoff" || env.RequestID == "" || env.ExpectedControlRevision != nil {
				t.Fatalf("sent=%+v env=%+v", sent, env)
			}
			var payload protocol.ConversationHandoffPayload
			if err := json.Unmarshal(env.Payload, &payload); err != nil || payload.PrepareOnly != prepare {
				t.Fatalf("payload=%s %v", env.Payload, err)
			}
		})
	}
}

func TestConversationHandoffLostAnswerKeepsRequestAndUnknownIsNotRepeated(t *testing.T) {
	f := newFakeServer(t)
	calls := 0
	f.on("POST /api/v1/ops", func(w http.ResponseWriter, r *http.Request, body []byte) {
		calls++
		if calls == 1 {
			c, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Fatal(err)
			}
			c.Close()
			return
		}
		_ = json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &protocol.Operation{RequestID: "request", Status: protocol.OpUnknown}})
	})
	te := newTestEnv(t, f.srv.URL)
	if code := te.main("conversation", "handoff"); code != exitError {
		t.Fatalf("unknown handoff=%d %s", code, te.errOut)
	}
	sent := f.seen("POST /api/v1/ops")
	if len(sent) != 2 || string(sent[0].body) != string(sent[1].body) || !strings.Contains(te.out.String(), "unknown") {
		t.Fatalf("lost answer replay: %+v %s", sent, te.out)
	}
}

func TestConversationHandoffStatusReadsOnly(t *testing.T) {
	f := newFakeServer(t)
	f.answer("GET /api/v1/conversation/handoff", protocol.ConversationHandoffView{Enabled: true, IdleMinutes: 50, Revision: 4})
	te := newTestEnv(t, f.srv.URL)
	if code := te.main("conversation", "handoff", "--status", "--json"); code != exitOK || !strings.Contains(te.out.String(), `"idle_minutes": 50`) {
		t.Fatalf("status=%d %s %s", code, te.out, te.errOut)
	}
	if len(f.seen("POST /api/v1/ops")) != 0 || f.seen("GET /api/v1/conversation/handoff")[0].auth != "Bearer owner-token" {
		t.Fatal("status was not an owner read")
	}
	if code := te.main("conversation", "handoff", "--status", "--prepare-only"); code != exitUsage {
		t.Fatalf("conflicting flags accepted: %d", code)
	}
}

func TestConversationHandoffStatusShowsMaintenanceFailure(t *testing.T) {
	f := newFakeServer(t)
	f.answer("GET /api/v1/conversation/handoff", map[string]any{"enabled": true, "idle_minutes": 50, "preparing": false, "pending": false, "maintenance": map[string]any{"request_id": "req_original", "operation": "conversation.handoff", "status": "failed", "reason": "Summary timed out"}})
	te := newTestEnv(t, f.srv.URL)
	if code := te.main("conversation", "handoff", "--status"); code != exitOK || !strings.Contains(te.out.String(), "failed") || !strings.Contains(te.out.String(), "Summary timed out") || !strings.Contains(te.out.String(), "req_original") {
		t.Fatalf("status=%d %s %s", code, te.out, te.errOut)
	}
	if len(f.seen("POST /api/v1/ops")) != 0 {
		t.Fatal("status retried a failed maintenance operation")
	}
}
