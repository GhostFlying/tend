package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

func TestFault_ArchiveWorkerIsolatesRunsAndResumesDoneRunOriginalAfterRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	r, bad, root, f := archiveRunner(t)
	r.cfg.Log = slog.New(slog.DiscardHandler)
	for _, as := range []protocol.Assignment{bad, {Protocol: 3, RunID: "z-good"}} {
		if err := r.cfg.Journal.AcceptAssignment(ctx, as, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.registerRollout(ctx, bad.RunID, protocol.RolloutPreviewSource{SessionID: "session", TurnID: "first"}); err != nil {
		t.Fatal(err)
	}
	goodPath := filepath.Join(root, "good.jsonl")
	if err := os.WriteFile(goodPath, []byte("{\"type\":\"session_meta\",\"payload\":{\"id\":\"good\"}}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.registerRollout(ctx, "z-good", protocol.RolloutPreviewSource{SessionID: "good", Path: goodPath}); err != nil {
		t.Fatal(err)
	}
	// Completed execution is no reason to discard its outstanding archive tail.
	if err := r.cfg.Journal.Done(ctx, bad.RunID); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var original, replay protocol.Envelope
	var recovered atomic.Bool
	entered := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		data, _ := io.ReadAll(q.Body)
		var env protocol.Envelope
		var chunk protocol.NativeRolloutChunk
		_ = json.Unmarshal(data, &env)
		_ = json.Unmarshal(env.Payload, &chunk)
		if chunk.ThreadID == "session" {
			mu.Lock()
			if original.RequestID == "" {
				original = env
			} else {
				replay = env
			}
			mu.Unlock()
			if !recovered.Load() {
				once.Do(func() { close(entered) })
				<-q.Context().Done()
				return
			}
		}
		q.Body = io.NopCloser(bytes.NewReader(data))
		f.serve(t, w, q)
	}))
	defer server.Close()
	r.c.base, r.c.http = server.URL, server.Client()
	start := func(r *Runner) func() {
		c, stop := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { defer close(done); r.rolloutArchives(c) }()
		return func() { stop(); <-done }
	}
	stop := start(r)
	select {
	case <-entered:
	case <-ctx.Done():
		stop()
		t.Fatal(ctx.Err())
	}
	await := func(onlyBad bool) {
		t.Helper()
		for {
			pending, err := r.cfg.Journal.PendingArchives(ctx, "", 10)
			if err != nil {
				t.Fatal(err)
			}
			if onlyBad && len(pending) == 1 && pending[0].RunID == bad.RunID || !onlyBad && len(pending) == 0 {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatal("bad source blocked an unrelated completed upload", pending, ctx.Err())
			case <-time.After(time.Millisecond):
			}
		}
	}
	await(true)
	stop()
	path := filepath.Join(root, "archive-journal.db")
	if err := r.cfg.Journal.Close(); err != nil {
		t.Fatal(err)
	}
	j, err := journal.Open(ctx, path, r.cfg.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	r.cfg.Journal = j
	restarted := New(r.cfg)
	restarted.c, restarted.runnerProtocol, restarted.previewDeployment = r.c, 3, "deployment"
	recovered.Store(true)
	stop = start(restarted)
	await(false)
	stop()
	mu.Lock()
	defer mu.Unlock()
	if original.RequestID == "" || replay.RequestID != original.RequestID || !bytes.Equal(replay.Payload, original.Payload) {
		t.Fatal("restart replaced an uncertain archive request", original.RequestID, replay.RequestID)
	}
}

func TestArchiveMissingOriginalDoesNotClearCoverage(t *testing.T) {
	r, as, _, _ := archiveRunner(t)
	if err := r.cfg.Journal.AcceptAssignment(t.Context(), as, ""); err != nil {
		t.Fatal(err)
	}
	if err := r.registerRollout(t.Context(), as.RunID, protocol.RolloutPreviewSource{SessionID: "missing"}); err != nil {
		t.Fatal(err)
	}
	if err := r.syncRolloutArchive(t.Context(), as); err == nil {
		t.Fatal("missing source was treated as a completed archive")
	}
	pending, err := r.cfg.Journal.PendingArchives(t.Context(), "", 10)
	if err != nil || len(pending) != 1 {
		t.Fatal("missing source lost its pending watermark", pending, err)
	}
}
