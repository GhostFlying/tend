package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/httpdiag"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

// eventBatch is the most events the stream reads at once.
const eventBatch = 200

// events streams ledger events to an owner's client as server-sent events
// (D69): first a "start" event with the cursor the stream continues from,
// then a "ledger" event for each ledger event after it, whose id is its
// event_seq. ?after=<event_seq> resumes after the last event a client
// received; without it the stream starts at the newest event. A quiet
// stream sends a comment as a heartbeat. Clients read it with fetch, which
// can send the bearer token, unlike EventSource.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	if _, err := s.owner(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	if r.Method == http.MethodHead {
		// ServeMux routes HEAD to GET handlers; a stream without a body
		// would only hold the connection.
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		return
	}
	ctx := r.Context()
	observed := httpdiag.SetTraceHeader(ctx, w)
	diagnostic := eventStreamObservation{started: started}
	if observed {
		defer diagnostic.finish(ctx, s.cfg.Heartbeat)
	}
	query := r.URL.Query()
	hasAfter := query.Has("after")
	after, err := intParam(query.Get("after"))
	if err != nil {
		diagnostic.reason = "invalid_cursor"
		diagnostic.status = s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "after must be an event_seq"))
		return
	}
	diagnostic.requested, diagnostic.hasRequested = after, hasAfter
	_, ok := w.(http.Flusher)
	if !ok {
		diagnostic.reason = "stream_unsupported"
		diagnostic.status = s.writeError(w, r, fmt.Errorf("api: the response cannot stream"))
		return
	}
	response := http.NewResponseController(w)
	var latest int64
	var expired bool
	if err := s.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		latest, err = store.LatestEventSeq(ctx, tx.Q())
		if err == nil && hasAfter {
			expired, err = store.RefreshCursorExpired(ctx, tx.Q(), after)
		}
		return err
	}); err != nil {
		diagnostic.reason = "initial_read_failed"
		if ctx.Err() != nil {
			diagnostic.reason = "request_ended"
		}
		if observed {
			err = protocol.Errorf(protocol.CodeInternal, "internal error")
		}
		diagnostic.status = s.writeError(w, r, err)
		return
	}
	diagnostic.initialWatermark, diagnostic.hasInitialWatermark = latest, true
	switch {
	case !hasAfter:
		after = latest
	case after > latest:
		// A cursor ahead of current state can belong to a restored ledger.
		diagnostic.reason = "cursor_ahead"
		diagnostic.status = s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest,
			"after %d is past the ledger's newest event %d; reload and connect without after", after, latest))
		return
	case expired:
		diagnostic.reason = "cursor_expired"
		diagnostic.status = s.writeError(w, r, protocol.Errorf(protocol.CodeStaleRevision,
			"refresh cursor expired; reload authoritative views and connect without after"))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	diagnostic.status = http.StatusOK
	w.WriteHeader(diagnostic.status)
	if writeEvent(w, "start", 0, protocol.StreamStart{EventSeq: after}) != nil || response.Flush() != nil {
		diagnostic.reason = "start_write_failed"
		return
	}
	diagnostic.cursorFlushed(after)
	httpdiag.Waiting(ctx)
	heartbeat := time.NewTicker(s.cfg.Heartbeat)
	defer heartbeat.Stop()
	for {
		// Wait on the change before reading, so a change made during the
		// read is not missed.
		changed := s.app.Changes().Wait()
		var batch []protocol.LedgerEvent
		var resetAt int64
		if err := s.store.Read(ctx, func(tx *store.ReadTx) error {
			expired, err := store.RefreshCursorExpired(ctx, tx.Q(), after)
			if err != nil {
				return err
			}
			if expired {
				resetAt, err = store.LatestEventSeq(ctx, tx.Q())
				return err
			}
			batch, err = views.Events(ctx, tx.Q(), after, eventBatch)
			return err
		}); err != nil {
			diagnostic.reason = "read_failed"
			if ctx.Err() != nil {
				diagnostic.reason = "request_ended"
			} else if !observed {
				s.log.ErrorContext(ctx, "event stream read failed", "after", after, "err", err)
			}
			return
		}
		if resetAt > 0 {
			// A resync is explicit even after response headers were committed.
			// Older clients reconnect and receive the same expired-cursor error.
			diagnostic.reason, diagnostic.reset = "cursor_expired", resetAt
			writeErr := writeEvent(w, "reset", 0, protocol.StreamStart{EventSeq: resetAt})
			flushErr := response.Flush()
			diagnostic.resetWriteFailed = writeErr != nil || flushErr != nil
			if !diagnostic.resetWriteFailed {
				diagnostic.frameFlushed()
			}
			return
		}
		var refreshThrough, writtenThrough int64
		for _, ev := range batch {
			after = ev.EventSeq
			var payload any = ev
			if strings.HasPrefix(r.URL.Path, "/api/v1/messaging/") {
				kind := ev.Kind
				if kind == "conversation.message" {
					message, err := s.app.MessagingMessage(ctx, ev.ObjectID)
					if err != nil || message.Direction != "input" {
						continue
					}
					kind = "messaging.input"
				} else if kind == "conversation.associations_changed" {
					if _, err := s.app.MessagingMessage(ctx, ev.ObjectID); err != nil {
						continue
					}
					kind = "messaging.associations_changed"
				} else if kind == "conversation.message_state" || kind == "conversation.urgent_consumed" || kind == "conversation.turn_reviewed" || strings.HasPrefix(kind, "conversation.processing_") || strings.HasPrefix(kind, "conversation.clarification_") {
					message, err := s.app.MessagingMessage(ctx, ev.ObjectID)
					if err != nil || message.Direction != "input" {
						continue
					}
					kind = "messaging.processing"
				} else if strings.HasPrefix(kind, "task.") || strings.HasPrefix(kind, "issue.") || kind == "run.observed" {
					kind = "messaging.work_changed"
				} else if !strings.HasPrefix(kind, "messaging.") {
					continue
				}
				payload = protocol.MessagingEvent{EventSeq: ev.EventSeq, Kind: kind, ObjectType: ev.ObjectType, ObjectID: ev.ObjectID}
			}
			if writeEvent(w, "ledger", ev.EventSeq, payload) != nil {
				diagnostic.reason = "ledger_write_failed"
				return
			}
			writtenThrough = ev.EventSeq
			if store.RefreshKind(ev.Kind) {
				refreshThrough = ev.EventSeq
			}
		}

		if len(batch) > 0 {
			if err := response.Flush(); err != nil {
				diagnostic.reason = "ledger_write_failed"
				return
			}
			if writtenThrough > 0 {
				diagnostic.cursorFlushed(writtenThrough)
			}
			if refreshThrough > 0 {
				s.refreshAck.offer(refreshThrough)
			}
			if len(batch) == eventBatch {
				continue
			}
		}
		select {
		case <-ctx.Done():
			diagnostic.reason = "request_ended"
			return
		case <-s.ending:
			diagnostic.reason = "server_shutdown"
			httpdiag.WaitStopped(ctx)
			return
		case <-changed:
		case <-heartbeat.C:
			if _, err := io.WriteString(w, ": heartbeat\n\n"); err != nil || response.Flush() != nil {
				diagnostic.reason = "heartbeat_write_failed"
				return
			}
			diagnostic.heartbeatFlushed()
		}
	}
}

// writeEvent writes one server-sent event; an id of 0 is left out.
func writeEvent(w io.Writer, name string, id int64, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if id > 0 {
		_, err = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", id, name, data)
	} else {
		_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, data)
	}
	return err
}
