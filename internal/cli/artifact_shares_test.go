package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestArtifactShareCLIUsesExactOriginalControlAndPagination(t *testing.T) {
	for _, action := range []string{"share-artifact", "revoke-artifact-share"} {
		t.Run(action, func(t *testing.T) {
			f := newFakeServer(t)
			e := newTestEnv(t, f.srv.URL)
			request := e.IDs.New(protocol.PrefixRequest)
			payload := `{"artifact_id":"art_original","ref_sha256":"` + strings.Repeat("a", 64) + `","reason":"Use exact original"}`
			operation := protocol.OpGrantArtifactShare
			if action == "revoke-artifact-share" {
				payload = `{"share_id":"shr_original","expected_share_revision":4,"reason":"End new reads"}`
				operation = protocol.OpRevokeArtifactShare
			}
			if code := e.main("issue", action, "wi_target", "--request-id", request, "--revision", "7", "--epoch", "3", "--data", payload, "--dry-run"); code != exitOK {
				t.Fatal(code, e.errOut.String())
			}
			var original protocol.Envelope
			if err := json.Unmarshal(e.out.Bytes(), &original); err != nil {
				t.Fatal(err)
			}
			var control struct {
				Epoch         int64  `json:"expected_assignment_epoch"`
				ShareRevision int64  `json:"expected_share_revision"`
				Ref           string `json:"ref_sha256"`
			}
			if err := json.Unmarshal(original.Payload, &control); err != nil {
				t.Fatal(err)
			}
			if original.Operation != operation || original.RequestID != request || original.Target.Type != "issue" || original.Target.ID != "wi_target" || original.ExpectedControlRevision == nil || *original.ExpectedControlRevision != 7 || control.Epoch != 3 {
				t.Fatal(original, control)
			}
			if action == "share-artifact" && control.Ref != strings.Repeat("a", 64) || action == "revoke-artifact-share" && control.ShareRevision != 4 {
				t.Fatal(control)
			}
			if len(f.requests) != 0 {
				t.Fatal("explicit recovery values caused implicit network reads")
			}
			file := filepath.Join(t.TempDir(), "original.json")
			if err := os.WriteFile(file, e.out.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			f.on("POST /api/v1/ops", func(w http.ResponseWriter, r *http.Request, body []byte) {
				var got protocol.Envelope
				if err := json.Unmarshal(body, &got); err != nil {
					t.Error(err)
				}
				wantHash, _ := protocol.PayloadHash(&original)
				gotHash, _ := protocol.PayloadHash(&got)
				if got.RequestID != request || gotHash != wantHash {
					t.Error("replay changed original request", got)
				}
				json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &protocol.Operation{RequestID: request, Status: protocol.OpSucceeded, Result: json.RawMessage(`{"share_id":"shr_original","revision":4}`)}})
			})
			e.out.Reset()
			e.errOut.Reset()
			if code := e.main("issue", "replay", "--file", file); code != exitOK || len(f.seen("POST /api/v1/ops")) != 1 {
				t.Fatal(code, e.errOut.String())
			}
		})
	}
	f := newFakeServer(t)
	e := newTestEnv(t, f.srv.URL)
	f.on("GET /api/v1/issues/wi_target/artifact-shares", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.URL.Query().Get("status") != "active" || r.URL.Query().Get("after") != "shr_before" || r.URL.Query().Get("limit") != "2" {
			t.Error(r.URL)
		}
		json.NewEncoder(w).Encode(protocol.ArtifactSharePage{Items: []protocol.ArtifactShare{{ShareID: "shr_item", RefSHA256: strings.Repeat("b", 64)}}, Next: "shr_item"})
	})
	if code := e.main("artifact-share", "list", "wi_target", "--status", "active", "--after", "shr_before", "--limit", "2"); code != exitOK || !strings.Contains(e.out.String(), `"next": "shr_item"`) {
		t.Fatal(code, e.out.String(), e.errOut.String())
	}
	e.out.Reset()
	e.errOut.Reset()
	f.answer("GET /api/v1/artifact-shares/shr_item", protocol.ArtifactShare{ShareID: "shr_item", Revision: 4, Status: "revoked"})
	if code := e.main("artifact-share", "show", "shr_item"); code != exitOK || !strings.Contains(e.out.String(), `"status": "revoked"`) {
		t.Fatal(code, e.out.String(), e.errOut.String())
	}
}
