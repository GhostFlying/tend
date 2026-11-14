// Package httpdiag keeps bounded shutdown diagnostics for in-flight HTTP work.
// It never stores request bodies, URLs, credentials or business authority.
package httpdiag

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"
)

const snapshotLimit = 20

type requestKey struct{}

type request struct {
	ctx                                                     context.Context
	tracker                                                 *Tracker
	started                                                 time.Time
	trace, id, method, route, operation, kind, cancellation string
}

// Tracker belongs to one server lifetime. Completed requests are discarded.
type Tracker struct {
	mu              sync.Mutex
	now             func() time.Time
	id              func() string
	log             *slog.Logger
	active          map[*request]struct{}
	shutdownStarted time.Time
	stopping        bool
	completed       int
}

func New(now func() time.Time, id func() string, log *slog.Logger) *Tracker {
	return &Tracker{now: now, id: id, log: log, active: make(map[*request]struct{})}
}

func (t *Tracker) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		kind := "write"
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			kind = "read"
		}
		q := &request{tracker: t, ctx: r.Context(), started: t.now(), trace: t.id(), method: r.Method, route: "unmatched", kind: kind}
		t.mu.Lock()
		t.active[q] = struct{}{}
		t.mu.Unlock()
		defer func() {
			t.mu.Lock()
			delete(t.active, q)
			logCompletion := t.stopping && t.completed < snapshotLimit
			if logCompletion {
				t.completed++
			}
			var attrs []any
			if logCompletion {
				attrs = q.attrs(t.now())
			}
			t.mu.Unlock()
			if logCompletion {
				t.log.InfoContext(context.WithoutCancel(r.Context()), "HTTP request drained", attrs...)
			}
		}()
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestKey{}, q)))
	})
}

// Routes records the registered pattern, never the client-controlled URL.
func Routes(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pattern := mux.Handler(r)
		update(r.Context(), func(q *request) {
			if pattern != "" {
				q.route = pattern
			}
		})
		mux.ServeHTTP(w, r)
	})
}

// Identity is called after envelope validation; payloads are never inspected.
func Identity(ctx context.Context, requestID, traceID, operation string) {
	update(ctx, func(q *request) {
		q.id, q.operation = requestID, operation
		if traceID != "" {
			q.trace = traceID
		}
	})
}

func Waiting(ctx context.Context) { update(ctx, func(q *request) { q.kind = "read_wait" }) }
func WaitStopped(ctx context.Context) {
	update(ctx, func(q *request) { q.cancellation = "server_shutdown" })
}

func update(ctx context.Context, f func(*request)) {
	if q, ok := ctx.Value(requestKey{}).(*request); ok {
		q.tracker.mu.Lock()
		defer q.tracker.mu.Unlock()
		f(q)
	}
}

func (q *request) attrs(now time.Time) []any {
	cause := q.cancellation
	if cause == "" && q.ctx.Err() != nil {
		cause = "request_context"
	}
	return []any{"trace_id", q.trace, "request_id", q.id, "method", q.method, "route", q.route, "operation", q.operation, "request_kind", q.kind, "started_at", q.started, "elapsed_ms", now.Sub(q.started).Milliseconds(), "cancellation_source", cause, "cancellation_observed", cause != ""}
}

// Snapshot logs at most twenty oldest requests at lifecycle boundaries.
// The mutex protects only short metadata copies, never logging or HTTP work.
func (t *Tracker) Snapshot(ctx context.Context, phase string) {
	t.mu.Lock()
	if !t.stopping {
		t.shutdownStarted = t.now()
	}
	t.stopping = true
	elapsed := t.now().Sub(t.shutdownStarted).Milliseconds()
	requests := make([]request, 0, len(t.active))
	for q := range t.active {
		requests = append(requests, *q)
	}
	t.mu.Unlock()
	slices.SortFunc(requests, func(a, b request) int { return a.started.Compare(b.started) })
	t.log.InfoContext(ctx, "HTTP shutdown requests", "shutdown_phase", phase, "elapsed_ms", elapsed, "active_requests", len(requests), "sampled_requests", min(len(requests), snapshotLimit))
	for _, q := range requests[:min(len(requests), snapshotLimit)] {
		t.log.InfoContext(ctx, "HTTP shutdown active request", append(q.attrs(t.now()), "shutdown_phase", phase)...)
	}
}
