package httpdiag

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

// TraceHeader carries the server-generated correlation ID of a read or stream.
const TraceHeader = "X-Tend-Trace-ID"

const (
	slowReadThreshold = time.Second
	summaryWindow     = 10 * time.Second
	summaryLimit      = 10
)

type summaryClass int

const (
	slowRead summaryClass = iota
	abnormalRead
	streamEnd
	abnormalStream
	streamReadFailure
	summaryClasses
)

type summaryBudget struct {
	start time.Time
	used  int
}

// report shares the request identity used by shutdown diagnostics. Budgets
// are process-local and separate so routine disconnects cannot crowd out resets.
func (q *request) report(ctx context.Context, class summaryClass, now time.Time, level slog.Level, message string, attrs ...any) {
	t := q.tracker
	t.mu.Lock()
	budget := &t.summaries[class]
	if budget.used == 0 || now.Sub(budget.start) >= summaryWindow {
		*budget = summaryBudget{start: now}
	}
	if budget.used == summaryLimit {
		t.mu.Unlock()
		return
	}
	budget.used++
	common := q.attrs(now)
	t.mu.Unlock()
	t.log.Log(context.WithoutCancel(ctx), level, message, append(common, attrs...)...)
}

// SetTraceHeader exposes only the request's server-generated trace. It does
// not derive any diagnostic identity from request input.
func SetTraceHeader(ctx context.Context, w http.ResponseWriter) bool {
	q, ok := ctx.Value(requestKey{}).(*request)
	if ok {
		w.Header().Set(TraceHeader, q.trace)
	}
	return ok
}

// StreamEnded reports a single terminal observation for an authenticated stream.
func StreamEnded(ctx context.Context, reason string, attrs ...any) {
	if q, ok := ctx.Value(requestKey{}).(*request); ok {
		class, level := abnormalStream, slog.LevelWarn
		switch reason {
		case "cancelled", "server_shutdown":
			class, level = streamEnd, slog.LevelInfo
		case "initial_read_failed", "read_failed":
			// A wave of rejected cursors must leave room for database failures.
			class = streamReadFailure
		}
		q.report(ctx, class, q.tracker.now(), level, "HTTP event stream ended", append(attrs, "end_reason", reason)...)
	}
}

// ContextResult identifies what the server observed on the request context.
func ContextResult(ctx context.Context) string {
	switch ctx.Err() {
	case context.Canceled:
		return "cancelled"
	case context.DeadlineExceeded:
		return "deadline_exceeded"
	default:
		return "active"
	}
}

// ReadObservation wraps just the bounded JSON read handlers. It times the
// application's writes to net/http, which may buffer independently of the peer.
type ReadObservation struct {
	http.ResponseWriter
	request               *request
	responseStarted       time.Time
	status                int
	writeTime             time.Duration
	writeFailed           bool
	Begin, Query, Release time.Duration
	TransactionStarted    bool
}

// ObserveRead starts a bounded GET observation when the server request
// tracker is present. HEAD and long-lived streaming handlers use other paths.
func ObserveRead(w http.ResponseWriter, r *http.Request) *ReadObservation {
	if r.Method != http.MethodGet || !SetTraceHeader(r.Context(), w) {
		return nil
	}
	q := r.Context().Value(requestKey{}).(*request)
	return &ReadObservation{ResponseWriter: w, request: q}
}

func (o *ReadObservation) Unwrap() http.ResponseWriter { return o.ResponseWriter }

func (o *ReadObservation) WriteHeader(status int) {
	start := time.Now()
	o.responseStarted, o.status = start, status
	o.ResponseWriter.WriteHeader(status)
	o.writeTime += time.Since(start)
}

func (o *ReadObservation) Write(p []byte) (int, error) {
	start := time.Now()
	n, err := o.ResponseWriter.Write(p)
	o.writeTime += time.Since(start)
	o.writeFailed = o.writeFailed || err != nil
	return n, err
}

func (o *ReadObservation) Finish(ctx context.Context) {
	q := o.request
	now := q.tracker.now()
	result := ContextResult(ctx)
	class := slowRead
	if result != "active" || o.writeFailed {
		class = abnormalRead
	} else if now.Sub(q.started) < slowReadThreshold || o.status == 0 {
		return
	}
	attrs := []any{
		"http_status", o.status,
		"context_result", result,
		"reader_begin_ms", o.Begin.Milliseconds(),
		"transaction_started", o.TransactionStarted,
		"write_failed", o.writeFailed,
	}
	if o.TransactionStarted {
		attrs = append(attrs, "query_projection_ms", o.Query.Milliseconds(), "reader_release_ms", o.Release.Milliseconds())
	}
	if !o.responseStarted.IsZero() {
		attrs = append(attrs, "encode_write_ms", time.Since(o.responseStarted).Milliseconds(), "write_ms", o.writeTime.Milliseconds())
	}
	q.report(ctx, class, now, slog.LevelInfo, "HTTP read observed", attrs...)
}
