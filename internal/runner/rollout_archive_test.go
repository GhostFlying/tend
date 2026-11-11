package runner

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

type archiveTransportFixture struct {
	mu       sync.Mutex
	requests []protocol.Envelope
	files    map[string][]byte
	receipts map[string]protocol.NativeRolloutArchiveReceipt
	drop     bool
}

func (f *archiveTransportFixture) serve(t *testing.T, w http.ResponseWriter, q *http.Request) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if q.URL.Path != rpcPath || q.Header.Get(headerHost) != "host" || q.Header.Get(headerRun) != "" {
		t.Error("archive did not use host-scoped RPC")
	}
	var env protocol.Envelope
	if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
		t.Error(err)
		return
	}
	f.requests = append(f.requests, env)
	var chunk protocol.NativeRolloutChunk
	if err := json.Unmarshal(env.Payload, &chunk); err != nil {
		t.Error(err)
		return
	}
	if env.Operation != protocol.OpRunnerRolloutArchive || env.Target.Type != "run" || env.Target.ID != chunk.RunID {
		t.Error("wrong archive request", env)
	}
	hash := sha256.Sum256(chunk.Data)
	if hex.EncodeToString(hash[:]) != chunk.SHA256 || len(chunk.Data) > rolloutArchiveChunkBytes || bytes.Contains(env.Payload, []byte(`"path"`)) {
		t.Error("original range changed or private locator leaked")
	}
	receipt, duplicate := f.receipts[env.RequestID]
	if !duplicate {
		if int64(len(f.files[chunk.SourceID])) != chunk.Offset {
			t.Errorf("noncontiguous chunk: %d vs %d", len(f.files[chunk.SourceID]), chunk.Offset)
		}
		f.files[chunk.SourceID] = append(f.files[chunk.SourceID], chunk.Data...)
		body := f.files[chunk.SourceID]
		receipt = protocol.NativeRolloutArchiveReceipt{SourceID: chunk.SourceID, Offset: chunk.Offset, NextOffset: int64(len(body)), SHA256: chunk.SHA256, IndexedOffset: int64(bytes.LastIndexByte(body, '\n') + 1)}
		f.receipts[env.RequestID] = receipt
	}
	if f.drop {
		f.drop = false
		_, _ = w.Write([]byte("receipt lost"))
		return
	}
	result, _ := json.Marshal(receipt)
	_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: result}})
}

func archiveRunner(t *testing.T) (*Runner, protocol.Assignment, string, *archiveTransportFixture) {
	t.Helper()
	r := controlRunner(t)
	r.cfg.IDs = testkit.NewIDGen(testkit.NewClock(), 991)
	root := nativeFixture(t, r)
	if err := r.cfg.Journal.Close(); err != nil {
		t.Fatal(err)
	}
	j, err := journal.Open(t.Context(), filepath.Join(root, "archive-journal.db"), r.cfg.Now)
	if err != nil {
		t.Fatal(err)
	}
	r.cfg.Journal = j
	t.Cleanup(func() { _ = j.Close() })
	r.runnerProtocol = 3
	r.c.hostID, r.c.token = "host", "host-token"
	as := protocol.Assignment{Protocol: 3, RunID: "run"}
	if err := r.registerRollout(t.Context(), as.RunID, protocol.RolloutPreviewSource{SessionID: "session", TurnID: "first", Path: filepath.Join(root, "rollout-session.jsonl")}); err != nil {
		t.Fatal(err)
	}
	f := &archiveTransportFixture{files: map[string][]byte{}, receipts: map[string]protocol.NativeRolloutArchiveReceipt{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) { f.serve(t, w, q) }))
	t.Cleanup(server.Close)
	r.c.base, r.c.http = server.URL, server.Client()
	return r, as, root, f
}

func TestRolloutArchiveLostReceiptRestartAndSharedSessionAppend(t *testing.T) {
	r, as, root, f := archiveRunner(t)
	f.drop = true
	if err := r.syncRolloutArchive(t.Context(), as); !isRetryable(err) {
		t.Fatalf("lost receipt not retained: %v", err)
	}
	before, err := r.cfg.Journal.RolloutArchive(t.Context(), "deployment", "session")
	if err != nil || before.Pending == nil || before.Cursor.Offset != 0 {
		t.Fatal(before, err)
	}
	// A new runner process uses the same durable cursor and exact original bytes.
	// Reopen the journal rather than merely retaining an in-memory pending request.
	journalPath := filepath.Join(root, "archive-journal.db")
	if err := r.cfg.Journal.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := journal.Open(t.Context(), journalPath, r.cfg.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	r.cfg.Journal = reopened
	restarted := New(r.cfg)
	restarted.runnerProtocol, restarted.previewDeployment = 3, "deployment"
	restarted.c = r.c
	if err := restarted.syncRolloutArchive(t.Context(), as); err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 2 || f.requests[0].RequestID != f.requests[1].RequestID || !bytes.Equal(f.requests[0].Payload, f.requests[1].Payload) {
		t.Fatal("restart replaced original upload", f.requests)
	}
	path := filepath.Join(root, "rollout-session.jsonl")
	original, _ := os.ReadFile(path)
	appendix := []byte("{\"type\":\"event_msg\",\"payload\":{\"text\":\"next run\"}}\n")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.Write(appendix)
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	as.RunID = "next-run"
	if err := restarted.registerRollout(t.Context(), as.RunID, protocol.RolloutPreviewSource{SessionID: "session", TurnID: "second", Path: path}); err != nil {
		t.Fatal(err)
	}
	if err := restarted.syncRolloutArchive(t.Context(), as); err != nil {
		t.Fatal(err)
	}
	after, _ := reopened.RolloutArchive(t.Context(), "deployment", "session")
	if len(f.requests) != 3 || len(f.files) != 1 || !bytes.Equal(f.files[after.Cursor.SourceID], append(original, appendix...)) {
		t.Fatal("reused session duplicated or lost original prefix", f.files)
	}
	var last protocol.NativeRolloutChunk
	_ = json.Unmarshal(f.requests[2].Payload, &last)
	if last.RunID != as.RunID || last.Offset != int64(len(original)) || !bytes.Equal(last.Data, appendix) {
		t.Fatal("new run did not continue shared source", last)
	}
}

func TestRolloutArchiveLargeIncompleteJSONLAndFileGeneration(t *testing.T) {
	r, as, root, f := archiveRunner(t)
	path := filepath.Join(root, "rollout-session.jsonl")
	original := []byte("{\"type\":\"session_meta\",\"payload\":{\"id\":\"session\"}}\n{\"text\":\"" + strings.Repeat("世", rolloutArchiveChunkBytes) + "\"")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.syncRolloutArchive(t.Context(), as); err != nil {
		t.Fatal(err)
	}
	first, _ := r.cfg.Journal.RolloutArchive(t.Context(), "deployment", "session")
	if len(f.requests) < 3 || !bytes.Equal(f.files[first.Cursor.SourceID], original) || first.Cursor.Offset != int64(len(original)) {
		t.Fatal("large JSONL record was clipped or skipped", first)
	}
	// Atomic replacement is another original generation, even for the same thread.
	replacement := []byte("{\"type\":\"session_meta\",\"payload\":{\"id\":\"session\"}}\n")
	if err := os.WriteFile(path+".next", replacement, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".next", path); err != nil {
		t.Fatal(err)
	}
	if err := r.syncRolloutArchive(t.Context(), as); err != nil {
		t.Fatal(err)
	}
	second, _ := r.cfg.Journal.RolloutArchive(t.Context(), "deployment", "session")
	if first.Cursor.SourceID == second.Cursor.SourceID || len(f.files) != 2 || !bytes.Equal(f.files[second.Cursor.SourceID], replacement) || !bytes.Equal(f.files[first.Cursor.SourceID], original) {
		t.Fatal("replacement overwrote old immutable original", second)
	}
}

func TestRolloutArchiveSkipsProtocol2AndExecutionsWithoutOriginals(t *testing.T) {
	r, as, _, f := archiveRunner(t)
	as.Protocol = 2
	if err := r.syncRolloutArchive(t.Context(), as); err != nil || len(f.requests) != 0 {
		t.Fatal("changed protocol 2 behavior", err)
	}
	as.Protocol = 3
	as.RunID = "without-original"
	if err := r.syncRolloutArchive(t.Context(), as); err != nil || len(f.requests) != 0 {
		t.Fatal("invented native source", err)
	}
}

func TestRolloutArchiveRejectsForeignHeaderAndTruncateRegrow(t *testing.T) {
	r, as, root, f := archiveRunner(t)
	path := filepath.Join(root, "rollout-session.jsonl")
	original, _ := os.ReadFile(path)
	if err := r.syncRolloutArchive(t.Context(), as); err != nil {
		t.Fatal(err)
	}
	first, _ := r.cfg.Journal.RolloutArchive(t.Context(), "deployment", "session")
	// A native rewrite can retain an inode and regrow past the acknowledged
	// offset. The saved prefix/anchor still detects a new file generation.
	replacement := bytes.ReplaceAll(original, []byte("first run"), []byte("replaced original"))
	if err := os.WriteFile(path, replacement, 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.syncRolloutArchive(t.Context(), as); err != nil {
		t.Fatal(err)
	}
	second, _ := r.cfg.Journal.RolloutArchive(t.Context(), "deployment", "session")
	if first.Cursor.SourceID == second.Cursor.SourceID || !bytes.Equal(f.files[second.Cursor.SourceID], replacement) {
		t.Fatal("truncate/regrow reused old original", first, second)
	}
	foreign := bytes.ReplaceAll(replacement, []byte(`"id":"session"`), []byte(`"id":"foreign"`))
	if err := os.WriteFile(path, foreign, 0600); err != nil {
		t.Fatal(err)
	}
	count := len(f.requests)
	if err := r.syncRolloutArchive(t.Context(), as); err == nil || len(f.requests) != count {
		t.Fatal("foreign native session was uploaded", err)
	}
}

func TestFault_RolloutArchiveUnavailableDoesNotBlockWorkerAndManagerEvents(t *testing.T) {
	for _, manager := range []bool{false, true} {
		t.Run(map[bool]string{false: "worker", true: "manager"}[manager], func(t *testing.T) {
			r, as, _, _ := archiveRunner(t)
			if err := r.cfg.Journal.AcceptAssignment(t.Context(), as, ""); err != nil {
				t.Fatal(err)
			}
			operation := protocol.OpRunnerEvents
			if manager {
				operation = protocol.OpManagerNativeEvent
				payload, _ := json.Marshal(protocol.ManagerNativeEvent{Sequence: 1})
				if err := r.cfg.Journal.PutRequest(t.Context(), journal.Request{RunID: as.RunID, Envelope: protocol.Envelope{RequestID: "original-event", Operation: operation, Target: runTarget(as), Payload: payload}}); err != nil {
					t.Fatal(err)
				}
			} else if err := r.cfg.Journal.AppendEvent(t.Context(), as.RunID, protocol.RunEvent{NativeID: "native", Kind: "native.event", Body: json.RawMessage(`{"text":"body"}`)}); err != nil {
				t.Fatal(err)
			}
			var delivered int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				data, _ := io.ReadAll(q.Body)
				var env protocol.Envelope
				_ = json.Unmarshal(data, &env)
				if env.Operation == protocol.OpRunnerRolloutArchive {
					t.Error("event flush synchronously attempted archival")
					http.Error(w, "archive unavailable", http.StatusForbidden)
					return
				}
				if env.Operation != operation {
					t.Error("wrong business event operation", env.Operation)
				}
				delivered++
				_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: json.RawMessage(`{}`)}})
			}))
			defer server.Close()
			r.c.base, r.c.http = server.URL, server.Client()
			flush := r.flushEvents
			if manager {
				flush = r.flushManagerEvents
			}
			if err := flush(t.Context(), as); err != nil || delivered != 1 {
				t.Fatal("archive failure blocked business event", err, delivered)
			}
		})
	}
}

func TestRolloutArchiveTimeoutRetainsOriginalAndResumes(t *testing.T) {
	r, as, _, f := archiveRunner(t)
	r.cfg.RequestTimeout = 20 * time.Millisecond
	r.c.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	client := r.c.http
	var original protocol.Envelope
	r.c.http = &http.Client{Transport: uploadRoundTripper(func(q *http.Request) (*http.Response, error) {
		_ = json.NewDecoder(q.Body).Decode(&original)
		<-q.Context().Done()
		return nil, q.Context().Err()
	})}
	if err := r.syncRolloutArchive(t.Context(), as); !isRetryable(err) {
		t.Fatal("uncertain timeout was discarded", err)
	}
	pending, err := r.cfg.Journal.RolloutArchive(t.Context(), "deployment", "session")
	if err != nil || pending.Pending == nil || pending.Pending.RequestID != original.RequestID || pending.Cursor.Offset != 0 {
		t.Fatal("timed out original request lost", pending, err)
	}
	r.c.http = client
	r.cfg.RequestTimeout = time.Second
	if err := r.syncRolloutArchive(t.Context(), as); err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 1 || f.requests[0].RequestID != original.RequestID || !bytes.Equal(f.requests[0].Payload, original.Payload) {
		t.Fatal("timeout resumed under a new request", f.requests)
	}
}

func TestRolloutArchiveBindsResumedRunWithoutCopyingOriginal(t *testing.T) {
	r, as, root, f := archiveRunner(t)
	if err := r.syncRolloutArchive(t.Context(), as); err != nil {
		t.Fatal(err)
	}
	first, _ := r.cfg.Journal.RolloutArchive(t.Context(), "deployment", "session")
	as.RunID = "next-run"
	if err := r.registerRollout(t.Context(), as.RunID, protocol.RolloutPreviewSource{SessionID: "session", TurnID: "second", Path: filepath.Join(root, "rollout-session.jsonl")}); err != nil {
		t.Fatal(err)
	}
	f.drop = true
	if err := r.syncRolloutArchive(t.Context(), as); !isRetryable(err) {
		t.Fatal(err)
	}
	if err := r.syncRolloutArchive(t.Context(), as); err != nil {
		t.Fatal(err)
	}
	// Repeating the flush needs neither another binding nor another byte range.
	if err := r.syncRolloutArchive(t.Context(), as); err != nil {
		t.Fatal(err)
	}
	last, _ := r.cfg.Journal.RolloutArchive(t.Context(), "deployment", "session")
	if len(f.files) != 1 || len(f.requests) != 3 || last.Cursor.SourceID != first.Cursor.SourceID || last.Cursor.Offset != first.Cursor.Offset || last.Cursor.LastRunID != as.RunID {
		t.Fatal("resumed run copied the original", first, last, len(f.requests))
	}
	var binding protocol.NativeRolloutChunk
	_ = json.Unmarshal(f.requests[1].Payload, &binding)
	if len(binding.Data) != 0 || binding.Offset != first.Cursor.Offset || f.requests[1].RequestID != f.requests[2].RequestID || !bytes.Equal(f.requests[1].Payload, f.requests[2].Payload) {
		t.Fatal("binding retry changed original identity or bytes", binding)
	}
}
