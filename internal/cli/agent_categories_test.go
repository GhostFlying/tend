package cli

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestAgentCategoryCLIUsesCASAndOriginalRetryIdentity(t *testing.T) {
	f := newFakeServer(t)
	f.answer("GET /api/v1/categories/implementer", protocol.AgentCategory{CategoryID: "implementer", Revision: 7, Title: "Implementation", State: "active"})
	f.on("POST /api/v1/ops", func(w http.ResponseWriter, _ *http.Request, raw []byte) {
		var env protocol.Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Error(err)
		}
		if env.Operation != "categories.put" || env.Target.Type != "category" || env.Target.ID != "implementer" || env.ExpectedControlRevision == nil {
			t.Errorf("invalid category envelope %+v", env)
		}
		_ = json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: json.RawMessage(`{"category_id":"implementer","revision":8,"title":"Implementation","description":"Build changes","state":"active"}`)}})
	})
	te := newTestEnv(t, f.srv.URL)
	args := []string{"category", "put", "implementer", "--title", "Implementation", "--description", "Build changes"}
	if code := te.main(args...); code != exitOK {
		t.Fatalf("put=%d %s", code, te.errOut)
	}
	requests := f.seen("POST /api/v1/ops")
	var original protocol.Envelope
	json.Unmarshal(requests[0].body, &original)
	if *original.ExpectedControlRevision != 7 {
		t.Fatal("did not auto-read current CAS")
	}
	retry := append(append([]string{}, args...), "--request-id", original.RequestID, "--revision", "7")
	if code := te.main(retry...); code != exitOK {
		t.Fatalf("retry=%d %s", code, te.errOut)
	}
	requests = f.seen("POST /api/v1/ops")
	var repeated protocol.Envelope
	json.Unmarshal(requests[1].body, &repeated)
	a, _ := protocol.PayloadHash(&original)
	b, _ := protocol.PayloadHash(&repeated)
	if a != b || repeated.RequestID != original.RequestID {
		t.Fatal("retry changed original request")
	}
	if code := te.main(append(args, "--request-id", original.RequestID)...); code != exitUsage || len(f.seen("POST /api/v1/ops")) != 2 {
		t.Fatal("retry without original CAS sent")
	}
	if code := te.main("category", "show", "implementer"); code != exitOK {
		t.Fatal(code, te.errOut)
	}
}
