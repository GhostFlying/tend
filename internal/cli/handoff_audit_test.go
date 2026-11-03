package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestHandoffAuditCommandKeepsExplicitInstanceAndCursor(t *testing.T) {
	f := newFakeServer(t)
	f.on("GET /api/v1/conversation/handoff-audit", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if q := r.URL.Query(); q.Get("instance") != "original" || q.Get("after") != "17" || q.Get("limit") != "2" {
			t.Error("audit selectors changed", q)
		}
		_ = json.NewEncoder(w).Encode(protocol.HandoffAuditPage{Instance: "original", Items: []protocol.HandoffAuditEvent{}, NextAfter: 17})
	})
	e := newTestEnv(t, f.srv.URL)
	if code := e.main("conversation", "handoff-audit", "--instance", "original", "--after", "17", "--limit", "2", "--json"); code != exitOK {
		t.Fatal(code, e.errOut)
	}
	requests := f.seen("GET /api/v1/conversation/handoff-audit")
	if len(requests) != 1 || requests[0].auth != "Bearer owner-token" || !strings.Contains(e.out.String(), `"history_complete": false`) {
		t.Fatal(requests, e.out)
	}
	if len(f.seen("POST /api/v1/ops")) != 0 {
		t.Fatal("audit command wrote business state")
	}
	if code := e.main("conversation", "handoff-audit", "--help"); code != exitOK || !strings.Contains(e.out.String(), "instance") || !strings.Contains(e.out.String(), "after") || !strings.Contains(e.out.String(), "limit") {
		t.Fatal(code, e.out, e.errOut)
	}
}
