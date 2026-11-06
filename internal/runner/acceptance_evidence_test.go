package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func acceptanceEvidenceFixture(t *testing.T, handler http.HandlerFunc) (*acceptanceEvidence, Config) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	clock := testkit.NewClock()
	cfg := Config{ServerURL: server.URL, HTTPClient: server.Client(), Now: clock.Now, IDs: testkit.NewIDGen(clock, 1)}
	p := Prepared{Dir: t.TempDir(), StateDir: t.TempDir()}
	as := protocol.Assignment{RunID: "run_acceptance", TaskID: "task_original", ActionID: "action_original", SpecVersion: 2, Role: protocol.RoleAccept, RunToken: "run-only", AcceptanceEvidenceFiles: true}
	return &acceptanceEvidence{r: New(cfg), as: as, prepared: p}, cfg
}

func evidenceToolInput(t *testing.T, selections []harness.VerdictFileSelection) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]any{"verdict": "blocked", "evidence": map[string]string{"check": "external prerequisite unavailable"}, "files": selections})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func replyEvidence(w http.ResponseWriter, name string, body []byte) {
	file := protocol.ArtifactFile{FileID: "file_" + strings.ReplaceAll(name, ".", "_"), Name: name, Size: int64(len(body)), SHA256: evidenceDigest(body)}
	raw, _ := json.Marshal(file)
	_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: raw}})
}

func TestAcceptanceEvidenceSealsWholeSelectionBeforeUploadAndReopensOriginalBytes(t *testing.T) {
	var evidence *acceptanceEvidence
	var ids []string
	original := map[string]string{"first.txt": "original first evidence", "second.txt": "original second evidence"}
	evidence, cfg := acceptanceEvidenceFixture(t, func(w http.ResponseWriter, q *http.Request) {
		if q.Header.Get(headerRun) != "run_acceptance" || q.Header.Get("Authorization") != "Bearer run-only" {
			t.Error("evidence upload lost its original run credential")
		}
		name := q.URL.Query().Get("name")
		body, _ := io.ReadAll(q.Body)
		if string(body) != original[name] {
			t.Error("upload replaced originally selected bytes", name, string(body))
		}
		ids = append(ids, q.URL.Query().Get("request_id"))
		if len(ids) == 1 {
			dir := filepath.Join(evidence.prepared.StateDir, "acceptance-evidence", evidenceDigest([]byte("original-native-binding")))
			for i, name := range []string{"first.txt", "second.txt"} {
				sealed, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("%06d.raw", i)))
				if err != nil || string(sealed) != original[name] {
					t.Error("first upload started before the whole selection was sealed", name, err)
				}
				_ = os.WriteFile(filepath.Join(evidence.prepared.Dir, name), []byte("changed original path"), 0o600)
			}
			_, _ = w.Write([]byte("lost upload receipt"))
			return
		}
		replyEvidence(w, name, body)
	})
	selections := []harness.VerdictFileSelection{{Path: "first.txt", Name: "first.txt"}, {Path: "second.txt", Name: "second.txt"}}
	for name, body := range original {
		if err := os.WriteFile(filepath.Join(evidence.prepared.Dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	input := evidenceToolInput(t, selections)
	if _, err := evidence.identity(t.Context(), "original-native-binding", input); err == nil {
		t.Fatal("missing intent claimed a fully sealed original selection")
	}
	if _, err := evidence.seal(t.Context(), "original-native-binding", input, selections); err == nil {
		t.Fatal("lost receipt acknowledged the verdict files")
	}
	sealedIdentity, err := evidence.identity(t.Context(), "original-native-binding", input)
	if err != nil || sealedIdentity == "" {
		t.Fatal("unknown upload lost its fully sealed original identity", err)
	}
	if err := os.RemoveAll(evidence.prepared.Dir); err != nil {
		t.Fatal(err)
	}
	reopened := &acceptanceEvidence{r: New(cfg), as: evidence.as, prepared: evidence.prepared}
	files, err := reopened.seal(t.Context(), "original-native-binding", input, selections)
	if err != nil || len(files) != 2 || len(ids) != 3 || ids[0] == "" || ids[0] != ids[1] || ids[1] == ids[2] {
		t.Fatalf("original files/IDs not recovered: files=%+v ids=%v err=%v", files, ids, err)
	}
	for _, f := range files {
		if f.SHA256 != evidenceDigest([]byte(original[f.Name])) {
			t.Fatal("recovered manifest changed original evidence", f)
		}
	}
	if _, err := reopened.seal(t.Context(), "original-native-binding", input, selections); err != nil || len(ids) != 3 {
		t.Fatal("completed evidence upload was repeated", err)
	}
	if current, err := reopened.identity(t.Context(), "original-native-binding", input); err != nil || current != sealedIdentity {
		t.Fatal("receipt confirmation changed the fully sealed input identity", current, err)
	}
	changed := json.RawMessage(strings.Replace(string(input), "blocked", "passed", 1))
	if _, err := reopened.seal(t.Context(), "original-native-binding", changed, selections); err == nil || len(ids) != 3 {
		t.Fatal("same native binding retargeted the original verdict")
	}
	if _, err := reopened.seal(t.Context(), "original-native-binding", evidenceToolInput(t, nil), nil); err == nil || len(ids) != 3 {
		t.Fatal("dropping selected files bypassed original evidence intent")
	}
}

func TestAcceptanceEvidencePartialSealKeepsOriginalPinsAndSendsNoUpload(t *testing.T) {
	calls := 0
	e, _ := acceptanceEvidenceFixture(t, func(w http.ResponseWriter, q *http.Request) {
		calls++
		body, _ := io.ReadAll(q.Body)
		replyEvidence(w, q.URL.Query().Get("name"), body)
	})
	selected := []harness.VerdictFileSelection{{Path: "first", Name: "first"}, {Path: "second", Name: "second"}}
	for _, s := range selected {
		if err := os.WriteFile(filepath.Join(e.prepared.Dir, s.Path), []byte(s.Path), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(e.prepared.StateDir, "acceptance-evidence", evidenceDigest([]byte("binding")))
	blocked := filepath.Join(dir, "000001.raw")
	if err := os.MkdirAll(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	input := evidenceToolInput(t, selected)
	if _, err := e.seal(t.Context(), "binding", input, selected); err == nil || calls != 0 {
		t.Fatal("partial sealing sent an upload or confirmed evidence")
	}
	if _, err := e.identity(t.Context(), "binding", input); err == nil {
		t.Fatal("partially sealed selection could mint a closure binding")
	}
	before, err := os.ReadFile(filepath.Join(dir, "intent.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	for _, s := range selected {
		if err := os.WriteFile(filepath.Join(e.prepared.Dir, s.Path), []byte("changed "+s.Path), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.seal(t.Context(), "binding", input, selected); err == nil || calls != 0 {
		t.Fatal("partial recovery substituted new bytes or uploaded an incomplete set")
	}
	after, err := os.ReadFile(filepath.Join(dir, "intent.json"))
	if err != nil || string(after) != string(before) {
		t.Fatal("failed recovery replaced original IDs or content pins", err)
	}
	if err := os.WriteFile(filepath.Join(e.prepared.Dir, "second"), []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	files, err := e.seal(t.Context(), "binding", input, selected)
	if err != nil || len(files) != 2 || files[0].SHA256 != evidenceDigest([]byte("first")) || calls != 2 {
		t.Fatal("original sealed first file was not retained", files, calls, err)
	}
}

func TestAcceptanceEvidencePartialUploadReopensOnlyOriginalPendingRequest(t *testing.T) {
	var names, ids []string
	e, cfg := acceptanceEvidenceFixture(t, func(w http.ResponseWriter, q *http.Request) {
		name := q.URL.Query().Get("name")
		body, _ := io.ReadAll(q.Body)
		names, ids = append(names, name), append(ids, q.URL.Query().Get("request_id"))
		if string(body) != "original "+name {
			t.Error("pending upload replaced its original bytes", name)
		}
		if len(names) == 2 {
			_, _ = w.Write([]byte("second upload receipt lost"))
			return
		}
		replyEvidence(w, name, body)
	})
	selections := []harness.VerdictFileSelection{{Path: "first", Name: "first"}, {Path: "second", Name: "second"}}
	for _, selected := range selections {
		if err := os.WriteFile(filepath.Join(e.prepared.Dir, selected.Path), []byte("original "+selected.Name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	input := evidenceToolInput(t, selections)
	if _, err := e.seal(t.Context(), "binding", input, selections); err == nil || len(names) != 2 {
		t.Fatal("partial upload unexpectedly acknowledged", names, err)
	}
	if err := os.RemoveAll(e.prepared.Dir); err != nil {
		t.Fatal(err)
	}
	reopened := &acceptanceEvidence{r: New(cfg), as: e.as, prepared: e.prepared}
	files, err := reopened.seal(t.Context(), "binding", input, selections)
	if err != nil || len(files) != 2 || len(names) != 3 || names[2] != "second" || ids[1] != ids[2] {
		t.Fatal("reopen repeated a confirmed upload or changed pending identity", names, ids, files, err)
	}
}

func TestAcceptanceEvidenceRequiresConfirmedExactManifest(t *testing.T) {
	for _, kind := range []string{"unknown", "missing ID", "name", "size", "hash"} {
		t.Run(kind, func(t *testing.T) {
			e, _ := acceptanceEvidenceFixture(t, func(w http.ResponseWriter, q *http.Request) {
				body, _ := io.ReadAll(q.Body)
				file := protocol.ArtifactFile{FileID: "file_original", Name: "evidence", Size: int64(len(body)), SHA256: evidenceDigest(body)}
				status := protocol.OpSucceeded
				switch kind {
				case "unknown":
					status = protocol.OpUnknown
				case "missing ID":
					file.FileID = ""
				case "name":
					file.Name = "another-name"
				case "size":
					file.Size++
				case "hash":
					file.SHA256 = strings.Repeat("a", 64)
				}
				raw, _ := json.Marshal(file)
				_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: status, Result: raw}})
			})
			if err := os.WriteFile(filepath.Join(e.prepared.Dir, "source"), []byte("original"), 0o600); err != nil {
				t.Fatal(err)
			}
			selected := []harness.VerdictFileSelection{{Path: "source", Name: "evidence"}}
			if files, err := e.seal(t.Context(), "binding", evidenceToolInput(t, selected), selected); err == nil || len(files) != 0 {
				t.Fatal("unconfirmed or mismatched manifest acknowledged", files, err)
			}
		})
	}
}

func TestAcceptanceEvidenceNoFilesPreservesOriginalInputAndCapability(t *testing.T) {
	calls := 0
	e, _ := acceptanceEvidenceFixture(t, func(http.ResponseWriter, *http.Request) { calls++ })
	input := evidenceToolInput(t, nil)
	if _, err := e.seal(t.Context(), "binding", input, nil); err != nil || calls != 0 {
		t.Fatal(err)
	}
	for _, kind := range []string{"changed input", "disabled", "wrong role", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			copy := &acceptanceEvidence{r: e.r, as: e.as, prepared: e.prepared}
			ctx := t.Context()
			body := input
			switch kind {
			case "changed input":
				body = json.RawMessage(strings.Replace(string(input), "blocked", "passed", 1))
			case "disabled":
				copy.as.AcceptanceEvidenceFiles = false
			case "wrong role":
				copy.as.Role = protocol.RoleImplement
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if _, err := copy.seal(ctx, "binding", body, nil); err == nil || calls != 0 {
				t.Fatal("unnegotiated or changed original input accepted")
			}
		})
	}
}

func TestAcceptanceFeedbackDownloadsOnlyDeclaredOriginalManifest(t *testing.T) {
	body := []byte("original blocked acceptance log")
	file := protocol.ArtifactFile{FileID: "file_acceptance", Name: "native.log", Size: int64(len(body)), SHA256: evidenceDigest(body)}
	e, _ := acceptanceEvidenceFixture(t, func(w http.ResponseWriter, q *http.Request) {
		if q.URL.Path != "/runner/v1/files/"+file.FileID {
			t.Error("undeclared feedback file reached the server")
		}
		_, _ = w.Write(body)
	})
	e.as.Role = protocol.RoleImplement
	e.as.Feedback = &protocol.Feedback{AcceptanceID: "acceptance_original", Verdict: protocol.VerdictBlocked, Files: []protocol.ArtifactFile{file}, Evidence: json.RawMessage(`{"path":"/opaque/not-a-file-reference"}`)}
	e.prepared.ScratchDir = t.TempDir()
	if _, err := e.r.downloadFile(t.Context(), e.as, e.prepared, "undeclared"); err == nil {
		t.Fatal("opaque evidence or undeclared file was promoted into download authority")
	}
	path, err := e.r.downloadFile(t.Context(), e.as, e.prepared, file.FileID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(body) {
		t.Fatal("feedback did not recover original bytes", err)
	}
	e.as.Feedback.AcceptanceID = ""
	if _, _, err := assignmentFile(e.as, file.FileID); err == nil {
		t.Fatal("feedback without original acceptance identity authorized a file")
	}
}
