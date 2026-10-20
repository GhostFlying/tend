package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestEventUploadFailureLoggedOncePerOutage(t *testing.T) {
	var failing atomic.Bool
	var attempts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var env protocol.Envelope
		_ = json.NewDecoder(r.Body).Decode(&env)
		attempts.Add(1)
		if failing.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(rpcResponse{Error: protocol.Errorf(protocol.CodeInternal, "down")})
			return
		}
		_ = json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded}})
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clock := testkit.NewClock()
	j, err := journal.Open(ctx, filepath.Join(t.TempDir(), "journal.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	as := protocol.Assignment{RunID: "run_1", LeaseID: "lease_1", RunToken: "token"}
	if err := j.AcceptAssignment(ctx, as, ""); err != nil {
		t.Fatal(err)
	}
	var logs syncBuffer
	r := New(Config{ServerURL: srv.URL, HostID: "linux-1", HTTPClient: srv.Client(), Journal: j, Now: clock.Now,
		IDs: testkit.NewIDGen(clock, 1), Log: slog.New(slog.NewTextHandler(&logs, nil)), EventFlush: time.Millisecond})

	// outage appends an event while tend fails and waits for several
	// upload attempts, then lets tend accept it and waits for the ack.
	outage := func() {
		t.Helper()
		failing.Store(true)
		if err := j.AppendEvent(ctx, as.RunID, protocol.RunEvent{Kind: "item"}); err != nil {
			t.Fatal(err)
		}
		for start := attempts.Load(); attempts.Load() < start+5; {
			time.Sleep(time.Millisecond)
		}
		failing.Store(false)
		for {
			pending, err := j.PendingEvents(ctx, as.RunID, 1)
			if err != nil {
				t.Fatal(err)
			}
			if len(pending) == 0 {
				return
			}
			time.Sleep(time.Millisecond)
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.uploadEvents(ctx, as)
	}()
	outage()
	outage()
	cancel()
	<-done
	if n := strings.Count(logs.String(), "run event upload failed"); n != 2 {
		t.Errorf("upload failure logged %d times over two outages, want 2:\n%s", n, logs.String())
	}
}

// syncBuffer is a bytes.Buffer safe for concurrent writers and readers.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
