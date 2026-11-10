package mainthread

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// replyDraft belongs to serve's event loop, never to a delivery goroutine.
// Only cumulative text committed to the ledger is visible to clients.
type replyDraft struct {
	event     harness.Event
	text      strings.Builder
	state     protocol.AssistantState
	dirty     bool
	order     int
	persisted bool
	replyTo   string
	sources   []string
	consumed  []int64
}

type replyKey struct{ turn, item string }
type replyDrafts map[replyKey]*replyDraft

// replyPrefixAcknowledger confirms deltas only after their cumulative terminal
// reply is durable. Streaming frames remain replayable until that boundary.
type replyPrefixAcknowledger interface {
	AcknowledgeReplyPrefix(context.Context, harness.Event, []int64) error
}

// Internal repair prose is retained as raw events, never a user reply.
type repairPrefixAcknowledger interface {
	AcknowledgeRepairPrefix(context.Context, harness.Event, []int64) error
}

// Reply persistence needs only its original identity, not a live native thread.
type replyThread interface{ ID() string }

func (d replyDrafts) begin(ev harness.Event, m *Manager) {
	k := replyKey{ev.TurnID, ev.ItemID}
	if d[k] != nil {
		return
	}
	replyTo, sources := m.replyContext(ev.TurnID)
	d[k] = &replyDraft{event: ev, state: protocol.AssistantStreaming, order: len(d), replyTo: replyTo, sources: sources}
}

func (d replyDrafts) consume(ev harness.Event) {
	k := replyKey{ev.TurnID, ev.ItemID}
	r := d[k]
	if r == nil {
		r = &replyDraft{event: ev, state: protocol.AssistantStreaming, order: len(d)}
		d[k] = r
	}
	if r.state != protocol.AssistantStreaming {
		return
	}
	if ev.Kind == harness.EventAgentMessageDelta {
		r.text.WriteString(ev.Text)
		if ev.CoordinatorSequence != 0 {
			r.consumed = append(r.consumed, ev.CoordinatorSequence)
		}
	} else {
		r.text.Reset()
		r.text.WriteString(ev.Text)
		r.state = protocol.AssistantCompleted
	}
	r.event = ev
	r.event.Text, r.dirty = r.text.String(), true
}

func (m *Manager) flushReplies(ctx context.Context, th replyThread, drafts replyDrafts, turn string, state protocol.AssistantState) error {
	keys := make([]replyKey, 0, len(drafts))
	for key := range drafts {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return drafts[keys[i]].order < drafts[keys[j]].order })
	for _, key := range keys {
		r := drafts[key]
		if turn != "" && key.turn != turn {
			continue
		}
		if state != "" && r.state == protocol.AssistantStreaming {
			r.state, r.dirty = state, true
		}
		if !r.dirty || (r.event.Text == "" && !r.persisted) {
			continue
		}
		if err := m.recordReplySources(ctx, th, r.event, r.state, r.replyTo, r.sources); err != nil {
			return err
		}
		r.dirty, r.persisted = false, true
	}
	return nil
}

// Real native terminal events retain their ordinary acknowledgement path. Only
// attachment shutdown needs a separate boundary for otherwise unterminated text.
func (m *Manager) flushClosingReplies(ctx context.Context, th replyThread, drafts replyDrafts, state protocol.AssistantState) error {
	if err := m.flushReplies(ctx, th, drafts, "", state); err != nil {
		return err
	}
	acknowledger, ok := th.(replyPrefixAcknowledger)
	if !ok {
		return nil
	}
	for _, r := range drafts {
		if r.state == protocol.AssistantStreaming || r.event.Kind != harness.EventAgentMessageDelta {
			continue
		}
		draftCtx := replyWriteContext(ctx, r.event)
		repair, err := m.isRepairEvent(draftCtx, th, r.event)
		if err != nil {
			return err
		}
		if repair {
			if internal, ok := th.(repairPrefixAcknowledger); ok {
				if err := internal.AcknowledgeRepairPrefix(draftCtx, r.event, r.consumed); err != nil {
					return err
				}
			}
			continue
		}
		if !r.persisted {
			continue
		}
		if err := acknowledger.AcknowledgeReplyPrefix(draftCtx, r.event, r.consumed); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) recordReplyState(ctx context.Context, th harness.Thread, ev harness.Event, state protocol.AssistantState) error {
	replyTo, sources := m.replyContext(ev.TurnID)
	return m.recordReplySources(ctx, th, ev, state, replyTo, sources)
}

func (m *Manager) recordReplySources(ctx context.Context, th replyThread, ev harness.Event, state protocol.AssistantState, replyTo string, sources []string) error {
	ctx = replyWriteContext(ctx, ev)
	repair, err := m.isRepairEvent(ctx, th, ev)
	if err != nil {
		return err
	}
	if repair {
		return nil
	}
	if prior, refs, found, err := m.app.NativeItemSources(ctx, th.ID(), ev.TurnID, ev.ItemID); err != nil {
		return err
	} else if found {
		replyTo, sources = prior, refs
	}
	if replyTo == "" {
		rec, ok, err := m.app.TurnDelivery(ctx, th.ID(), ev.TurnID)
		if err != nil {
			return err
		}
		if ok {
			replyTo = rec.MessageID
		}
	}
	payload := map[string]any{"body": ev.Text, "reply_to": replyTo, "turn_id": ev.TurnID, "item_id": ev.ItemID}
	if len(sources) > 1 {
		payload["reply_sources"] = sources
	}
	native := th.ID() + "/" + ev.TurnID + "/item:" + ev.ItemID
	// Keep the completed item's existing identity and payload for receipts
	// created before streaming was enabled. Other writes include the body
	// digest so a retried cumulative frame has exactly the same request.
	if state != protocol.AssistantCompleted {
		payload["status"] = string(state)
		native += fmt.Sprintf("/%s:%x", state, sha256.Sum256([]byte(ev.Text)))
	}
	_, replay := th.(*databaseReplyThread)
	return m.record(ctx, th, native, "conversation.record_agent_message", payload, app.RecordAgentMessage{PreservePrefix: replay})
}

// A ticker, closing drain, or another event may flush retained drafts. Always
// scope each persistence/ACK to the draft, including explicitly unknown origin,
// rather than inheriting the event that happened to trigger this flush.
func replyWriteContext(ctx context.Context, ev harness.Event) context.Context {
	ctx = store.WithWriteAttachment(ctx, ev.AttachmentRunID, ev.CoordinatorSequence)
	return store.WithWriteOperation(store.WithWriteEvent(ctx, ev.OriginRunID, ev.OriginSequence), "manager.reply.persist")
}

func (m *Manager) isRepairEvent(ctx context.Context, th replyThread, ev harness.Event) (bool, error) {
	// A delayed item can belong to the previous native turn while a repair
	// is starting. Persisted native identity takes precedence over admission.
	rec, ok, err := m.app.TurnDelivery(ctx, th.ID(), ev.TurnID)
	if err != nil {
		return false, err
	}
	request := ""
	if ok {
		request = rec.RequestID
	} else {
		m.mu.Lock()
		if a := m.active; a != nil && (a.turnID == "" || a.turnID == ev.TurnID) {
			request = a.requestID
		}
		m.mu.Unlock()
	}
	if request == "" {
		return false, nil
	}
	return m.app.IsRepairExecution(ctx, request)
}
