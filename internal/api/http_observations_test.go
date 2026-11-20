package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/httpdiag"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type diagnosticRows chan map[string]any

func (rows diagnosticRows) Write(p []byte) (int, error) {
	var row map[string]any
	if err := json.Unmarshal(p, &row); err != nil {
		return 0, err
	}
	rows <- row
	return len(p), nil
}

func (rows diagnosticRows) next(t *testing.T) map[string]any {
	t.Helper()
	select {
	case row := <-rows:
		t.Logf("diagnostic: %v", row)
		return row
	case <-time.After(10 * time.Second):
		t.Fatal("no diagnostic summary")
		return nil
	}
}

func observedOwner(t *testing.T, e *env) (*httptest.Server, diagnosticRows) {
	t.Helper()
	rows := make(diagnosticRows, 100)
	tracker := httpdiag.New(time.Now, func() string { return e.ids.New(protocol.PrefixTrace) }, slog.New(slog.NewJSONHandler(rows, nil)))
	server := httptest.NewServer(tracker.Wrap(e.server.Handler()))
	t.Cleanup(server.Close)
	t.Cleanup(e.server.EndStreams)
	return server, rows
}

func TestHTTPReadObservesSlowHistoryAndCancelledSnapshot(t *testing.T) {
	e := newEnv(t)
	token := e.login(protocol.ScopeOwner)
	e.say("private-conversation-body")
	server, rows := observedOwner(t, e)
	ctx, stop := context.WithTimeout(t.Context(), 20*time.Second)
	defer stop()

	fetch := func(ctx context.Context, path string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+path, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		return server.Client().Do(req)
	}
	checkHistory := func(resp *http.Response) string {
		t.Helper()
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || err != nil || !strings.Contains(string(body), "private-conversation-body") {
			t.Fatalf("history response status=%d, read error=%v, expected message=%t", resp.StatusCode, err, strings.Contains(string(body), "private-conversation-body"))
		}
		return resp.Header.Get(httpdiag.TraceHeader)
	}
	const history = "/api/v1/conversation?limit=50&projection=issues&conversation_id=main&ignored=private-query-value"
	fast, err := fetch(ctx, history)
	if err != nil {
		t.Fatal(err)
	}
	checkHistory(fast)

	// Take every real SQLite reader while both production GETs enter BeginTx.
	busy, release := make(chan struct{}), make(chan struct{})
	var readers sync.WaitGroup
	readErrors := make(chan error, e.store.PoolStats().Reader.MaxOpenConnections)
	var releaseOnce sync.Once
	free := func() { releaseOnce.Do(func() { close(release) }) }
	defer func() {
		free()
		readers.Wait()
		close(readErrors)
		for err := range readErrors {
			t.Error(err)
		}
	}()
	for range cap(readErrors) {
		readers.Go(func() {
			if err := e.store.Read(ctx, func(*store.ReadTx) error {
				busy <- struct{}{}
				<-release
				return nil
			}); err != nil {
				readErrors <- err
			}
		})
	}
	for range cap(readErrors) {
		select {
		case <-busy:
		case err := <-readErrors:
			t.Fatal(err)
		case <-time.After(10 * time.Second):
			t.Fatal("the reader pool was not occupied")
		}
	}

	type result struct {
		response *http.Response
		err      error
	}
	historyResult, snapshotResult := make(chan result, 1), make(chan result, 1)
	snapshotCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	before := e.store.PoolStats()
	t.Logf("fixture pool occupied: readers=%d/%d writer_in_use=%d", before.Reader.InUse, before.Reader.MaxOpenConnections, before.Writer.InUse)
	go func() {
		response, err := fetch(ctx, history)
		historyResult <- result{response, err}
	}()
	go func() {
		response, err := fetch(snapshotCtx, "/api/v1/snapshot?project=private-query-value")
		snapshotResult <- result{response, err}
	}()
	for deadline := time.Now().Add(10 * time.Second); e.store.PoolStats().Reader.WaitCount < before.Reader.WaitCount+2; {
		if time.Now().After(deadline) {
			t.Fatal("both HTTP requests did not enter the reader pool queue")
		}
		time.Sleep(time.Millisecond)
	}
	started := time.Now()
	cancel()
	if got := <-snapshotResult; !errors.Is(got.err, context.Canceled) {
		t.Fatalf("snapshot did not end on client cancellation: %v", got.err)
	}
	canceled := rows.next(t)
	if canceled["route"] != "GET /api/v1/snapshot" || canceled["context_result"] != "cancelled" ||
		canceled["http_status"] != float64(500) || canceled["trace_id"] == "" ||
		canceled["transaction_started"] != false || canceled["query_projection_ms"] != nil {
		t.Fatal("cancelled snapshot did not identify the boundary", canceled)
	}

	// Hold at least the production threshold after confirming pool contention.
	if remaining := time.Second + 20*time.Millisecond - time.Since(started); remaining > 0 {
		time.Sleep(remaining)
	}
	free()
	slow := <-historyResult
	if slow.err != nil {
		t.Fatal(slow.err)
	}
	trace := checkHistory(slow.response)
	after := e.store.PoolStats()
	t.Logf("fixture pool wait delta: reader_count=%d reader_duration=%v writer_count=%d",
		after.Reader.WaitCount-before.Reader.WaitCount, after.Reader.WaitDuration-before.Reader.WaitDuration, after.Writer.WaitCount-before.Writer.WaitCount)
	row := rows.next(t)
	if trace == "" || row["trace_id"] != trace || row["route"] != "GET /api/v1/conversation" ||
		row["http_status"] != float64(200) || row["context_result"] != "active" ||
		row["transaction_started"] != true || row["reader_begin_ms"].(float64) < 1000 ||
		row["query_projection_ms"] == nil || row["reader_release_ms"] == nil ||
		row["encode_write_ms"] == nil || row["write_ms"] == nil {
		t.Fatal("slow history did not report its measured phases", row)
	}
	for _, observed := range []map[string]any{row, canceled} {
		text, _ := json.Marshal(observed)
		for _, secret := range []string{"private-query-value", "private-conversation-body", token, "projection="} {
			if strings.Contains(string(text), secret) {
				t.Fatal("read diagnostic included request input")
			}
		}
	}
}

func TestHTTPStreamObservesResetAndBoundsRoutineActivity(t *testing.T) {
	e := newEnv(t)
	token := e.login(protocol.ScopeOwner)
	e.say("private-event-body")
	e.server.cfg.Heartbeat = 5 * time.Millisecond
	server, rows := observedOwner(t, e)

	s := openStream(t, server.URL, token, "?after=1&ignored=private-query-value")
	if start := s.next(); start.name != "start" || s.trace == "" {
		t.Fatal("missing start or response trace", start, s.trace)
	}
	for range 3 {
		select {
		case beat := <-s.events:
			if !beat.heartbeat {
				t.Fatal("expected heartbeat", beat)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("no heartbeat")
		}
	}
	if len(rows) != 0 {
		t.Fatal("normal start or heartbeat emitted a diagnostic")
	}

	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		for range 10 {
			if _, err := tx.InsertEvent(t.Context(), store.EventRow{
				Kind: "token_usage.changed", ObjectType: "token_usage", ObjectID: "all",
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.app.Changes().Notify()
	if reset := s.next(); reset.name != "reset" || reset.data != `{"event_seq":11}` {
		t.Fatal(reset)
	}
	row := rows.next(t)
	if row["trace_id"] != s.trace || row["route"] != "GET /api/v1/events" ||
		row["http_status"] != float64(200) || row["end_reason"] != "cursor_expired" ||
		row["requested_cursor"] != float64(1) || row["initial_watermark"] != float64(1) ||
		row["flushed_cursor"] != float64(1) || row["reset_watermark"] != float64(11) ||
		row["reset_write_failed"] != false || row["heartbeats_flushed"].(float64) < 3 ||
		row["first_frame_ms"] == nil || row["last_heartbeat_age_ms"] == nil {
		t.Fatal("reset did not correlate with its SSE response", row)
	}
	data, _ := json.Marshal(row)
	if strings.Contains(string(data), "private-") || strings.Contains(string(data), token) {
		t.Fatal("stream diagnostic included request input")
	}

	// A real HTTP rejection has its own trace, and it has no flushed frame.
	rejected := openStream(t, server.URL, token, "?after=1")
	row = rows.next(t)
	if rejected.status != http.StatusConflict || rejected.trace == "" || rejected.trace == s.trace ||
		row["trace_id"] != rejected.trace || row["http_status"] != float64(409) ||
		row["end_reason"] != "cursor_expired" || row["first_frame_ms"] != nil {
		t.Fatal("refused cursor lost its own trace or reported a nonexistent frame", row)
	}

	// A real database read failure after headers committed leaves HTTP 200
	// intact and explains the close only in the terminal observation.
	failed := openStream(t, server.URL, token, "")
	if start := failed.next(); start.name != "start" {
		t.Fatal(start)
	}

	// Expired reconnects can fill the unexpected-end budget while a healthy
	// stream is open. The cap still applies after the first reset and refusal.
	for range 12 {
		attempt := openStream(t, server.URL, token, "?after=1")
		if attempt.status != http.StatusConflict {
			t.Fatalf("expected cursor rejection, got %d", attempt.status)
		}
		for range attempt.events {
		}
	}
	if got := len(rows) + 2; got != 10 {
		t.Fatalf("expected ten unexpected-end summaries, got %d", got)
	}
	for range len(rows) {
		<-rows
	}

	if err := e.store.Close(); err != nil {
		t.Fatal(err)
	}
	e.app.Changes().Notify()
	for range failed.events {
	}
	row = rows.next(t)
	if row["trace_id"] != failed.trace || row["http_status"] != float64(200) ||
		row["end_reason"] != "read_failed" || row["context_result"] != "active" ||
		row["flushed_cursor"] != float64(11) {
		t.Fatal("late read failure was not correlated to its open response", row)
	}

}
