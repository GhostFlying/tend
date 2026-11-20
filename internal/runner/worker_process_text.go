package runner

import (
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/coordination"
	"github.com/GhostFlying/tend/internal/protocol"
)

const (
	maxProcessTextItems = 4
	maxProcessTextBytes = 8 << 10
)

type processTextItem struct {
	thread, turn, item string
	text               strings.Builder
	truncated          bool
}

// workerProcessText coalesces fragments in native arrival order. Complete
// items already carry the text; unfinished items share the next turn boundary.
type workerProcessText struct {
	mu    sync.Mutex
	items []*processTextItem
}

func (p *workerProcessText) capture(event *coordination.Event) (fragment bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if event.Disconnected {
		p.drain(event, "")
		return false
	}
	native := event.Native
	if native == nil {
		return false
	}
	switch native.Kind {
	case harness.EventAgentMessageDelta:
		if event.ThreadID != "" && native.TurnID != "" && native.ItemID != "" && native.Text != "" {
			p.append(event.ThreadID, native.TurnID, native.ItemID, native.Text)
		}
		return true
	case harness.EventItemCompleted:
		if native.ItemType != harness.ItemAgentMessage {
			break
		}
		for i, item := range p.items {
			if item.thread == event.ThreadID && item.turn == native.TurnID && item.item == native.ItemID {
				p.items = append(p.items[:i], p.items[i+1:]...)
				break
			}
		}
	case harness.EventTurnCompleted:
		p.drain(event, native.TurnID)
	}
	return false
}

func (p *workerProcessText) live() []protocol.WorkerProcessText {
	p.mu.Lock()
	defer p.mu.Unlock()
	var result []protocol.WorkerProcessText
	for _, item := range p.items {
		result = append(result, item.snapshot())
	}
	return result
}

func (p *workerProcessText) reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.items = nil
}

func (i *processTextItem) snapshot() protocol.WorkerProcessText {
	return protocol.WorkerProcessText{
		ThreadID: i.thread, TurnID: i.turn, ItemID: i.item, Text: i.text.String(), Truncated: i.truncated,
	}
}

func (p *workerProcessText) append(thread, turn, id, text string) {
	for _, item := range p.items {
		if item.thread == thread && item.turn == turn && item.item == id {
			item.append(text)
			return
		}
	}
	if len(p.items) == maxProcessTextItems {
		return
	}
	item := &processTextItem{thread: thread, turn: turn, item: id}
	item.append(text)
	p.items = append(p.items, item)
}

func (i *processTextItem) append(text string) {
	if i.truncated {
		return
	}
	available := maxProcessTextBytes - i.text.Len()
	if len(text) > available {
		for available > 0 && !utf8.RuneStart(text[available]) {
			available--
		}
		text, i.truncated = text[:available], true
	}
	i.text.WriteString(text)
}

func (p *workerProcessText) drain(event *coordination.Event, turn string) {
	kept := p.items[:0]
	for _, item := range p.items {
		if (event.ThreadID != "" && item.thread != event.ThreadID) || (turn != "" && item.turn != turn) {
			kept = append(kept, item)
			continue
		}
		event.ProcessText = append(event.ProcessText, item.snapshot())
	}
	clear(p.items[len(kept):])
	p.items = kept
}
