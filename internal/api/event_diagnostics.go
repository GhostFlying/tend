package api

import (
	"context"
	"time"

	"github.com/GhostFlying/tend/internal/httpdiag"
)

type eventStreamObservation struct {
	started, firstFlush, lastFlush, lastHeartbeat time.Time
	requested, initialWatermark, flushed, reset   int64
	hasRequested, hasInitialWatermark             bool
	heartbeatCount                                int
	status                                        int
	reason                                        string
	resetWriteFailed                              bool
}

func (o *eventStreamObservation) frameFlushed() {
	o.lastFlush = time.Now()
	if o.firstFlush.IsZero() {
		o.firstFlush = o.lastFlush
	}
}

func (o *eventStreamObservation) cursorFlushed(seq int64) {
	o.frameFlushed()
	o.flushed = seq
}

func (o *eventStreamObservation) heartbeatFlushed() {
	o.frameFlushed()
	o.lastHeartbeat = o.lastFlush
	o.heartbeatCount++
}

func (o *eventStreamObservation) finish(ctx context.Context, heartbeat time.Duration) {
	now := time.Now()
	result := httpdiag.ContextResult(ctx)
	reason := o.reason
	if reason == "request_ended" {
		reason = result
	}
	attrs := []any{
		"http_status", o.status, "context_result", result,
		"heartbeat_interval_ms", heartbeat.Milliseconds(), "heartbeats_flushed", o.heartbeatCount,
	}
	if o.hasRequested {
		attrs = append(attrs, "requested_cursor", o.requested)
	}
	if o.hasInitialWatermark {
		attrs = append(attrs, "initial_watermark", o.initialWatermark)
	}
	if o.reset > 0 {
		attrs = append(attrs, "reset_watermark", o.reset, "reset_write_failed", o.resetWriteFailed)
	}
	if !o.firstFlush.IsZero() {
		attrs = append(attrs, "flushed_cursor", o.flushed, "first_frame_ms", o.firstFlush.Sub(o.started).Milliseconds(), "last_flush_age_ms", now.Sub(o.lastFlush).Milliseconds())
	}
	if !o.lastHeartbeat.IsZero() {
		attrs = append(attrs, "last_heartbeat_age_ms", now.Sub(o.lastHeartbeat).Milliseconds())
	}
	httpdiag.StreamEnded(ctx, reason, attrs...)
}
