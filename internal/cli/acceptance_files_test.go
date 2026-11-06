package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestAcceptanceEvidenceCLIShowsProvenanceAndDownloadsOriginal(t *testing.T) {
	original := []byte("original independent failure output\n\x00\xff")
	sum := sha256.Sum256(original)
	file := protocol.ArtifactFile{FileID: "file_acceptance", Name: "full-check.log", Size: int64(len(original)), SHA256: hex.EncodeToString(sum[:])}
	a := protocol.AcceptanceView{AcceptanceID: "acc_original", ArtifactID: "art_original", SpecVersion: 1, Verdict: protocol.VerdictBlocked, Files: []protocol.ArtifactFile{file}, Evidence: json.RawMessage(`{"path":"ephemeral.log"}`), Issues: json.RawMessage(`[]`)}
	spec, _ := json.Marshal(map[string]any{"inputs": []protocol.TaskInput{{FileID: file.FileID, Name: file.Name, Size: file.Size, SHA256: file.SHA256, SourceTaskID: "task_original", SourceAcceptanceID: a.AcceptanceID}}})
	task := protocol.TaskDetail{TaskSummary: protocol.TaskSummary{TaskID: "task_repair", Title: "repair"}, Spec: spec, Acceptances: []protocol.AcceptanceView{a}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/tasks/task_repair":
			_ = json.NewEncoder(w).Encode(task)
		case "/api/v1/artifacts/art_original":
			_ = json.NewEncoder(w).Encode(protocol.ArtifactDetail{TaskID: "task_original", Artifact: protocol.ArtifactView{ArtifactID: a.ArtifactID}, Acceptances: []protocol.AcceptanceView{a}})
		case "/api/v1/files/" + file.FileID + "/metadata":
			_ = json.NewEncoder(w).Encode(file)
		case "/api/v1/files/" + file.FileID:
			w.Header().Set("Content-Length", strconv.Itoa(len(original)))
			w.Header().Set("X-Content-SHA256", file.SHA256)
			_, _ = w.Write(original)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	for _, args := range [][]string{{"task", "task_repair"}, {"task", "show", "task_repair", "--json"}, {"artifact", "show", "art_original"}, {"file", "metadata", file.FileID, "--json"}} {
		e := newTestEnv(t, server.URL)
		if code := e.main(args...); code != exitOK {
			t.Fatalf("%v: %d %s", args, code, e.errOut)
		}
		for _, want := range []string{file.FileID, file.Name, file.SHA256} {
			if !strings.Contains(e.out.String(), want) {
				t.Fatalf("%v hides immutable evidence %q: %s", args, want, e.out)
			}
		}
		if args[0] != "file" && !strings.Contains(e.out.String(), a.AcceptanceID) {
			t.Fatal("acceptance source was omitted", args, e.out)
		}
	}
	out := filepath.Join(t.TempDir(), file.Name)
	e := newTestEnv(t, server.URL)
	if code := e.main("file", "download", file.FileID, "--out", out, "--json"); code != exitOK {
		t.Fatalf("download: %d %s", code, e.errOut)
	}
	got, err := os.ReadFile(out)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatal("downloaded acceptance bytes changed", err)
	}
	for _, command := range []string{"task", "artifact show", "file metadata", "file download"} {
		if !strings.Contains(commandUsage(strings.Fields(command)), "Acceptance evidence:") {
			t.Fatal("acceptance file capability missing from help", command)
		}
	}
}
