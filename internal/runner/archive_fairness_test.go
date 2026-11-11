package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/GhostFlying/tend/internal/protocol"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFault_MissingArchiveSourceDoesNotBlockOtherOriginals(t *testing.T) {
	r, as, root, f := archiveRunner(t)
	as.RunID = "multiple-sources"
	if err := r.cfg.Journal.AcceptAssignment(t.Context(), as, ""); err != nil {
		t.Fatal(err)
	}
	if err := r.registerRollout(t.Context(), as.RunID, protocol.RolloutPreviewSource{SessionID: "missing"}); err != nil {
		t.Fatal(err)
	}
	if err := r.registerRollout(t.Context(), as.RunID, protocol.RolloutPreviewSource{SessionID: "session", Path: filepath.Join(root, "rollout-session.jsonl")}); err != nil {
		t.Fatal(err)
	}
	err := r.syncRolloutArchive(t.Context(), as)
	if err == nil {
		t.Fatal("missing source incorrectly completed")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.files) == 0 {
		t.Fatalf("healthy second source was never attempted: %v", err)
	}
}
func TestFault_ArchiveQueueAdvancesPastBusyRuns(t *testing.T) {
	r, _, root, _ := archiveRunner(t)
	r.cfg.Log = slog.New(slog.DiscardHandler)
	for _, id := range []string{"a-busy", "b-busy", "z-done"} {
		as := protocol.Assignment{Protocol: 3, RunID: id}
		if err := r.cfg.Journal.AcceptAssignment(t.Context(), as, ""); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, id+".jsonl")
		if err := os.WriteFile(path, []byte(fmt.Sprintf("{\"type\":\"session_meta\",\"payload\":{\"id\":%q}}\n", id)), 0600); err != nil {
			t.Fatal(err)
		}
		if err := r.registerRollout(t.Context(), id, protocol.RolloutPreviewSource{SessionID: id, Path: path}); err != nil {
			t.Fatal(err)
		}
	}
	var count atomic.Int64
	var z atomic.Bool
	reached := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var env protocol.Envelope
		var chunk protocol.NativeRolloutChunk
		if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
			t.Error(err)
			return
		}
		if err := json.Unmarshal(env.Payload, &chunk); err != nil {
			t.Error(err)
			return
		}
		if chunk.RunID == "z-done" {
			z.Store(true)
		} else {
			path := filepath.Join(root, chunk.RunID+".jsonl")
			file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				t.Error(err)
				return
			}
			_, err = file.WriteString("{\"type\":\"response_item\",\"payload\":{\"text\":\"new tail\"}}\n")
			file.Close()
			if err != nil {
				t.Error(err)
				return
			}
			if err = r.cfg.Journal.AppendEvent(t.Context(), chunk.RunID, protocol.RunEvent{Kind: "native.item_completed", Body: json.RawMessage(`{}`)}); err != nil {
				t.Error(err)
				return
			}
			if count.Add(1) >= 30 {
				once.Do(func() { close(reached) })
			}
		}
		receipt := protocol.NativeRolloutArchiveReceipt{SourceID: chunk.SourceID, Offset: chunk.Offset, NextOffset: chunk.Offset + int64(len(chunk.Data)), SHA256: chunk.SHA256, IndexedOffset: chunk.Offset + int64(len(chunk.Data))}
		raw, _ := json.Marshal(receipt)
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Status: protocol.OpSucceeded, Result: raw}})
	}))
	defer server.Close()
	r.c.base, r.c.http = server.URL, server.Client()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); r.rolloutArchives(ctx) }()
	select {
	case <-reached:
	case <-time.After(4 * time.Second):
		t.Error("queue failed to run enough attempts")
	}
	cancel()
	<-done
	if !z.Load() {
		t.Fatalf("later pending Run never scheduled after %d successful busy-Run uploads", count.Load())
	}
}
