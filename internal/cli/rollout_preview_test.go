package cli

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestCapabilityRunPreviewPreservesNativePageAndContentCursors(t *testing.T) {
	f := newFakeServer(t)
	env := newTestEnv(t, f.srv.URL)
	var seen bool
	f.on("GET /api/v1/runs/run/preview", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		seen = true
		q := r.URL.Query()
		if q.Get("record") == "" {
			if q.Get("before") != "101" || q.Get("generation") != "native-generation" {
				t.Error(q)
			}
			_ = json.NewEncoder(w).Encode(protocol.RolloutPreview{State: "ready", Origin: "runner", Generation: "native-generation", Page: &protocol.RunRolloutPage{RunID: "run", Before: 1, After: 100, MoreAfter: true, Logs: []protocol.RunLogView{}}})
			return
		}
		if q.Get("record") != "17" || q.Get("field") != "output" || q.Get("reference") != "2" || q.Get("offset") != "65536" || q.Get("generation") != "native-generation" {
			t.Error(q)
		}
		_ = json.NewEncoder(w).Encode(protocol.RolloutPreview{State: "ready", Origin: "runner", Generation: "native-generation", Content: &protocol.RolloutPreviewContent{Text: "native output", NextOffset: 65549, More: true, References: []protocol.RolloutPreviewReference{{ID: 2, Name: "output.txt"}}}})
	})
	if code := env.main("run", "preview", "run", "--before", "101", "--generation", "native-generation"); code != 0 || !seen {
		t.Fatal(code, env.errOut)
	}
	var page protocol.RolloutPreview
	if err := json.Unmarshal(env.out.Bytes(), &page); err != nil || page.Page == nil || page.Page.Before != 1 || page.Page.After != 100 || !page.Page.MoreAfter || page.Generation != "native-generation" {
		t.Fatal(page, err)
	}
	env.out.Reset()
	if code := env.main("run", "preview", "run", "--record", "17", "--generation", "native-generation", "--field", "output", "--reference", "2", "--offset", "65536"); code != 0 {
		t.Fatal(code, env.errOut)
	}
	var content protocol.RolloutPreview
	if err := json.Unmarshal(env.out.Bytes(), &content); err != nil || content.Content == nil || content.Content.Text != "native output" || content.Content.NextOffset != 65549 || !content.Content.More || len(content.Content.References) != 1 {
		t.Fatal(content, err)
	}
}
func TestCapabilityRunPreviewRejectsUnboundCursorsBeforeReading(t *testing.T) {
	f := newFakeServer(t)
	env := newTestEnv(t, f.srv.URL)
	f.on("GET /api/v1/runs/run/preview", func(_ http.ResponseWriter, _ *http.Request, _ []byte) {
		t.Error("invalid preview cursor reached server")
	})
	for _, args := range [][]string{{"--record", "1"}, {"--offset", "1"}, {"--before", "1", "--after", "2"}, {"--after", "-1"}, {"--record", "1", "--generation", "g", "--field", "path"}} {
		if code := env.main(append([]string{"run", "preview", "run"}, args...)...); code == 0 {
			t.Fatal(args, "accepted")
		}
	}
}
