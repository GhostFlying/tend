package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

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
	entries, err := os.ReadDir(filepath.Join(p.ScratchDir, "deliverables"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("corrupt download became visible: %v %v", entries, err)
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

func TestFileTransfersOutliveRPCDeadline(t *testing.T) {
	content := []byte("slow but progressing file")
	digest := sha256.Sum256(content)
	file := protocol.ArtifactFile{FileID: "file_slow", Name: "slow.txt", Size: int64(len(content)), SHA256: hex.EncodeToString(digest[:])}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		time.Sleep(20 * time.Millisecond)
		if q.Method == http.MethodPost {
			_, _ = io.Copy(io.Discard, q.Body)
			result, _ := json.Marshal(file)
			_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: result}})
			return
		}
		_, _ = w.Write(content)
	}))
	defer srv.Close()
	clock := testkit.NewClock()
	r := New(Config{ServerURL: srv.URL, HTTPClient: srv.Client(), Now: clock.Now, IDs: testkit.NewIDGen(clock, 1), RequestTimeout: time.Millisecond})
	p := Prepared{Dir: t.TempDir(), StateDir: t.TempDir(), ScratchDir: t.TempDir()}
	if err := os.WriteFile(filepath.Join(p.Dir, file.Name), content, 0o600); err != nil {
		t.Fatal(err)
	}
	artifact, _ := json.Marshal(protocol.ArtifactPayload{Files: []protocol.ArtifactFile{file}})
	as := protocol.Assignment{RunID: "run_1", RunToken: "credential", Artifact: artifact}
	if got, err := r.uploadFile(t.Context(), as, p, file.Name, file.Name); err != nil || got != file {
		t.Fatalf("upload inherited short RPC timeout: %+v %v", got, err)
	}
	path, err := r.downloadFile(t.Context(), as, p, file.FileID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(content) {
		t.Fatalf("download: %q %v", got, err)
	}
}

func TestFileHashingHonorsRunCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	p := Prepared{Dir: t.TempDir(), StateDir: t.TempDir()}
	if err := os.WriteFile(filepath.Join(p.Dir, "file"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := New(Config{})
	if _, err := r.uploadFile(ctx, protocol.Assignment{}, p, "file", "file"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled hashing: %v", err)
	}
	entries, err := os.ReadDir(p.StateDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("cancelled upload left intent: %v %v", entries, err)
	}
}

// The runner has only run credentials, just as on a different host from the
// server. The worker receives the original bytes rather than a model preview.
func TestTaskInputsDownloadOriginalBytesForBothWorkerRoles(t *testing.T) {
	var animated bytes.Buffer
	frames := []*image.Paletted{
		image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.Black, color.White}),
		image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.White, color.Black}),
	}
	if err := gif.EncodeAll(&animated, &gif.GIF{Image: frames, Delay: []int{3, 7}}); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		name, mediaType string
		body            []byte
	}{
		{"animation.gif", "image/gif", animated.Bytes()},
		{"opaque.bin", "application/octet-stream", []byte{0, 255, 128, 1, 0, 13, 10, 200}},
	} {
		for _, role := range []protocol.RunRole{protocol.RoleImplement, protocol.RoleAccept} {
			t.Run(fixture.name+"/"+string(role), func(t *testing.T) {
				digest := sha256.Sum256(fixture.body)
				input := protocol.TaskInput{FileID: "file_input", Name: fixture.name, MediaType: fixture.mediaType, Size: int64(len(fixture.body)), SHA256: hex.EncodeToString(digest[:]), SourceMessageID: "msg_owner"}
				calls := 0
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
					calls++
					if q.URL.Path != "/runner/v1/files/file_input" || q.Header.Get(headerRun) != "run_input" || q.Header.Get("Authorization") != "Bearer run-only" {
						t.Error("input download did not use its run identity")
					}
					_, _ = w.Write(fixture.body)
				}))
				defer srv.Close()
				spec, _ := json.Marshal(struct {
					Inputs []protocol.TaskInput `json:"inputs"`
				}{[]protocol.TaskInput{input}})
				as := protocol.Assignment{Role: role, RunID: "run_input", RunToken: "run-only", Spec: spec}
				r := New(Config{ServerURL: srv.URL, HTTPClient: srv.Client()})
				p := Prepared{ScratchDir: t.TempDir()}
				if _, err := r.downloadFile(t.Context(), as, p, "file_unrelated"); err == nil || calls != 0 {
					t.Fatalf("unrelated input reached server: calls=%d err=%v", calls, err)
				}
				path, err := r.downloadFile(t.Context(), as, p, input.FileID)
				if err != nil {
					t.Fatal(err)
				}
				got, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(got, fixture.body) || filepath.Dir(path) != filepath.Join(p.ScratchDir, "inputs") {
					t.Fatalf("original input changed: path=%s err=%v bytes=%x", path, err, got)
				}
				if fixture.mediaType == "image/gif" {
					decoded, err := gif.DecodeAll(bytes.NewReader(got))
					if err != nil || len(decoded.Image) != 2 {
						t.Fatalf("GIF frames lost: %v", err)
					}
				}
			})
		}
	}
}

func TestTaskInputFailedDownloadPreservesPreviouslyVerifiedFile(t *testing.T) {
	original := []byte("original input")
	digest := sha256.Sum256(original)
	input := protocol.TaskInput{FileID: "file_input", Name: "input.txt", Size: int64(len(original)), SHA256: hex.EncodeToString(digest[:])}
	spec, _ := json.Marshal(struct {
		Inputs []protocol.TaskInput `json:"inputs"`
	}{[]protocol.TaskInput{input}})
	for _, content := range [][]byte{[]byte("short"), []byte("corrupt! input"), append(bytes.Clone(original), '!')} {
		t.Run(string(content), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(content) }))
			defer srv.Close()
			p := Prepared{ScratchDir: t.TempDir()}
			dir := filepath.Join(p.ScratchDir, "inputs")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, input.FileID)
			if err := os.WriteFile(path, original, 0o600); err != nil {
				t.Fatal(err)
			}
			r := New(Config{ServerURL: srv.URL, HTTPClient: srv.Client()})
			if _, err := r.downloadFile(t.Context(), protocol.Assignment{Spec: spec}, p, input.FileID); err == nil {
				t.Fatal("corrupted input accepted")
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, original) {
				t.Fatalf("previous input replaced: %q %v", got, err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 {
				t.Fatalf("partial download retained: %v %v", entries, err)
			}
		})
	}
}
