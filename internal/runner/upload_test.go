package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestEventUploadFailureLoggedOncePerOutage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var failing atomic.Bool
		var attempts atomic.Int64
		client := &http.Client{Transport: uploadRoundTripper(func(req *http.Request) (*http.Response, error) {
			defer req.Body.Close()
			var env protocol.Envelope
			if err := json.NewDecoder(req.Body).Decode(&env); err != nil {
				return nil, err
			}
			attempts.Add(1)
			status := http.StatusOK
			response := rpcResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded}}
			if failing.Load() {
				status = http.StatusServiceUnavailable
				response = rpcResponse{Error: protocol.Errorf(protocol.CodeInternal, "down")}
			}
			body, err := json.Marshal(response)
			if err != nil {
				return nil, err
			}
			return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}, nil
		})}
		ctx, cancel := context.WithCancel(t.Context())
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
		r := New(Config{ServerURL: "http://runner.invalid", HostID: "linux-1", HTTPClient: client, Journal: j, Now: clock.Now, IDs: testkit.NewIDGen(clock, 1), Log: slog.New(slog.NewTextHandler(&logs, nil)), EventFlush: time.Millisecond})
		done := make(chan struct{})
		go func() { defer close(done); r.uploadEvents(ctx, as) }()
		outage := func() {
			t.Helper()
			start := attempts.Load()
			failing.Store(true)
			if err := j.AppendEvent(ctx, as.RunID, protocol.RunEvent{Kind: "item"}); err != nil {
				t.Fatal(err)
			}
			for attempts.Load() < start+5 {
				time.Sleep(time.Millisecond)
				synctest.Wait()
			}
			failing.Store(false)
			time.Sleep(time.Millisecond)
			// Wait until uploadEvents has returned from flushEvents and blocks on its
			// next timer. An empty journal alone precedes the failing flag reset.
			synctest.Wait()
			pending, err := j.PendingEvents(ctx, as.RunID, 1)
			if err != nil {
				t.Fatal(err)
			}
			if len(pending) != 0 {
				t.Fatal("successful outage recovery left pending events")
			}
		}
		outage()
		outage()
		cancel()
		<-done
		if n := strings.Count(logs.String(), "run event upload failed"); n != 2 {
			t.Errorf("upload failure logged %d times over two outages, want 2:\n%s", n, logs.String())
		}
	})
}

type uploadRoundTripper func(*http.Request) (*http.Response, error)

func (f uploadRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

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
