package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestFileSubmissionReplaysUploadIDAfterLostReply(t *testing.T) {
	content := []byte("investigation evidence\n")
	digest := sha256.Sum256(content)
	file := protocol.ArtifactFile{FileID: "file_1", Name: "evidence.txt", Size: int64(len(content)), SHA256: hex.EncodeToString(digest[:])}
	var ids []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if q.Header.Get(headerRun) != "run_1" || q.Header.Get("Authorization") != "Bearer credential" {
			t.Error("missing run authentication")
		}
		ids = append(ids, q.URL.Query().Get("request_id"))
		b, _ := io.ReadAll(q.Body)
		if string(b) != string(content) {
			t.Error("file content changed")
		}
		if len(ids) == 1 {
			_, _ = w.Write([]byte("lost reply"))
			return
		}
		result, _ := json.Marshal(file)
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: result}})
	}))
	defer srv.Close()
	clock := testkit.NewClock()
	cfg := Config{ServerURL: srv.URL, HTTPClient: srv.Client(), Now: clock.Now, IDs: testkit.NewIDGen(clock, 1), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	p := Prepared{Dir: t.TempDir(), StateDir: t.TempDir()}
	if err := os.WriteFile(filepath.Join(p.Dir, "evidence.txt"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	as := protocol.Assignment{RunID: "run_1", RunToken: "credential"}
	r := New(cfg)
	if _, err := r.uploadFile(context.Background(), as, p, "evidence.txt", file.Name); err == nil {
		t.Fatal("expected lost reply")
	}
	r = New(cfg)
	got, err := r.uploadFile(context.Background(), as, p, "evidence.txt", file.Name)
	if err != nil {
		t.Fatal(err)
	}
	if got != file || len(ids) != 2 || ids[0] == "" || ids[0] != ids[1] {
		t.Fatalf("file=%+v, ids=%v", got, ids)
	}
	if _, err := r.uploadFile(context.Background(), as, p, "evidence.txt", file.Name); err != nil || len(ids) != 2 {
		t.Fatal("uploaded file was not cached")
	}
}

func TestDownloadedArtifactVerifiesMembershipAndDigest(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) { called = true; _, _ = w.Write([]byte("tampered")) }))
	defer srv.Close()
	clock := testkit.NewClock()
	r := New(Config{ServerURL: srv.URL, HTTPClient: srv.Client(), Now: clock.Now})
	artifact, _ := json.Marshal(protocol.ArtifactPayload{Files: []protocol.ArtifactFile{{FileID: "file_1", Size: 8, SHA256: "expected"}}})
	as := protocol.Assignment{RunID: "run_accept", RunToken: "credential", Artifact: artifact}
	p := Prepared{ScratchDir: t.TempDir()}
	if _, err := r.downloadFile(context.Background(), as, p, "outside"); err == nil || called {
		t.Fatal("download accepted a file outside the artifact")
	}
	if _, err := r.downloadFile(context.Background(), as, p, "file_1"); err == nil || !called {
		t.Fatal("download accepted corrupted contents")
	}
}

func TestUploadStateWriteFaultPreservesAcceptedRequestIdentity(t *testing.T) {
	content := []byte("accepted evidence")
	digest := sha256.Sum256(content)
	hexDigest := hex.EncodeToString(digest[:])
	file := protocol.ArtifactFile{FileID: "file_accepted", Name: "evidence.txt", Size: int64(len(content)), SHA256: hexDigest}
	p := Prepared{Dir: t.TempDir(), StateDir: t.TempDir()}
	if err := os.WriteFile(filepath.Join(p.Dir, file.Name), content, 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(p.StateDir, "uploads")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	key := sha256.Sum256([]byte(file.Name + "\x00" + hexDigest))
	state := filepath.Join(dir, hex.EncodeToString(key[:])+".json")
	original := []byte(`{"request_id":"req_accepted"}`)
	writeFault := func(f *os.File) error {
		_, err := f.Write([]byte(`{"request_id":`))
		if err != nil {
			return err
		}
		return io.ErrShortWrite
	}
	// A failed initial intent is never exposed as replayable partial JSON.
	if err := replaceUploadState(state, writeFault); err == nil {
		t.Fatal("missing write fault")
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("partial initial intent visible: %v", err)
	}
	if err := replaceUploadState(state, func(f *os.File) error { _, err := f.Write(original); return err }); err != nil {
		t.Fatal(err)
	}
	// The server has accepted req_accepted. A torn acknowledgement must leave
	// its original intent intact so a restarted runner deduplicates that upload.
	if err := replaceUploadState(state, writeFault); err == nil {
		t.Fatal("missing acknowledgement write fault")
	}
	got, err := os.ReadFile(state)
	if err != nil || string(got) != string(original) {
		t.Fatalf("accepted request identity lost: %s %v", got, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary state leaked: %v %v", entries, err)
	}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls++
		if req.URL.Query().Get("request_id") != "req_accepted" {
			t.Error("accepted upload used a new request identity")
		}
		_, _ = io.Copy(io.Discard, req.Body)
		b, _ := json.Marshal(file)
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: b}})
	}))
	defer srv.Close()
	clock := testkit.NewClock()
	r := New(Config{ServerURL: srv.URL, HTTPClient: srv.Client(), Now: clock.Now, IDs: testkit.NewIDGen(clock, 1)})
	as := protocol.Assignment{RunID: "run_1", RunToken: "credential"}
	if got, err := r.uploadFile(t.Context(), as, p, file.Name, file.Name); err != nil || got != file {
		t.Fatalf("replay: %+v %v", got, err)
	}
	if _, err := r.uploadFile(t.Context(), as, p, file.Name, file.Name); err != nil || calls != 1 {
		t.Fatalf("acknowledgement not atomically cached: calls=%d err=%v", calls, err)
	}
}

func TestUploadDirectorySyncPropagatesSupportedPlatformErrors(t *testing.T) {
	err := syncUploadDirectory(filepath.Join(t.TempDir(), "missing"))
	if runtime.GOOS == "windows" {
		if err != nil {
			t.Fatalf("unsupported directory flush was attempted: %v", err)
		}
	} else if !os.IsNotExist(err) {
		t.Fatalf("directory flush error was swallowed: %v", err)
	}
}
