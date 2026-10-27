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
}

type replyKey struct{ turn, item string }
type replyDrafts map[replyKey]*replyDraft

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
	} else {
		r.text.Reset()
		r.text.WriteString(ev.Text)
		r.state = protocol.AssistantCompleted
	}
	r.event.Text, r.dirty = r.text.String(), true
}

func (m *Manager) flushReplies(ctx context.Context, th harness.Thread, drafts replyDrafts, turn string, state protocol.AssistantState) error {
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
		if err := m.recordReplyState(ctx, th, r.event, r.state); err != nil {
			return err
		}
		r.dirty, r.persisted = false, true
	}
	return nil
}

func (m *Manager) recordReplyState(ctx context.Context, th harness.Thread, ev harness.Event, state protocol.AssistantState) error {
	repair, err := m.isRepairEvent(ctx, th, ev)
	if err != nil {
		return err
	}
	if repair {
		return nil
	}
	replyTo := m.replyTo(ev.TurnID)
	if replyTo == "" {
		rec, ok, err := m.app.TurnDelivery(ctx, th.ID(), ev.TurnID)
		if err != nil {
			return err
		}
		if ok {
			replyTo = rec.MessageID
		}
	}
	payload := map[string]string{"body": ev.Text, "reply_to": replyTo, "turn_id": ev.TurnID, "item_id": ev.ItemID}
	native := th.ID() + "/" + ev.TurnID + "/item:" + ev.ItemID
	// Keep the completed item's existing identity and payload for receipts
	// created before streaming was enabled. Other writes include the body
	// digest so a retried cumulative frame has exactly the same request.
	if state != protocol.AssistantCompleted {
		payload["status"] = string(state)
		native += fmt.Sprintf("/%s:%x", state, sha256.Sum256([]byte(ev.Text)))
	}
	return m.record(ctx, th, native, "conversation.record_agent_message", payload, app.RecordAgentMessage{})
}

func (m *Manager) isRepairEvent(ctx context.Context, th harness.Thread, ev harness.Event) (bool, error) {
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
