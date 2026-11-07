package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestQuestionInputsDownloadUsesFreshOriginalRunMetadata(t *testing.T) {
	raw := []byte("new original\x00evidence")
	digest := sha256.Sum256(raw)
	input := protocol.TaskInput{FileID: "fil_original", Name: "answer.bin", Size: int64(len(raw)), SHA256: hex.EncodeToString(digest[:])}
	as := protocol.Assignment{RunID: "run_question", RunToken: "run-only", TaskID: "task", ActionID: "action", SpecVersion: 1, Role: protocol.RoleImplement, WorkerQuestions: true}
	question := protocol.WorkerQuestion{RunID: as.RunID, ActionID: as.ActionID, SpecVersion: as.SpecVersion, Role: as.Role, Status: "answered", InputRevision: 1, Inputs: []protocol.TaskInput{input}}
	reads, downloads, metadataReads := 0, 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(headerRun) != as.RunID || r.Header.Get("Authorization") != "Bearer "+as.RunToken {
			t.Error("not original run credentials")
		}
		switch r.URL.Path {
		case "/runner/v1/questions":
			reads++
			_ = json.NewEncoder(w).Encode([]protocol.WorkerQuestion{question})
		case "/runner/v1/files/" + input.FileID:
			downloads++
			_, _ = w.Write(raw)
		case "/runner/v1/files/" + input.FileID + "/metadata":
			metadataReads++
			http.NotFound(w, r)
		default:
			t.Error(r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	q := questionRunner(t, srv)
	p := Prepared{ScratchDir: t.TempDir()}
	path, err := q.r.downloadFile(t.Context(), as, p, input.FileID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(raw) {
		t.Fatal(got, err)
	}
	question.Inputs = nil
	question.InputRevision = 2
	if _, err := q.r.downloadFile(t.Context(), as, p, input.FileID); err == nil {
		t.Fatal("revoked metadata trusted cached bytes")
	}
	question.Inputs = []protocol.TaskInput{input}
	question.RunID = "other"
	if _, err := q.r.downloadFile(t.Context(), as, p, input.FileID); err == nil {
		t.Fatal("inherited other run inputs")
	}
	if reads != 3 || downloads != 1 || metadataReads != 2 {
		t.Fatal("fresh question metadata or Issue fallback checks missing", reads, downloads, metadataReads)
	}
}
