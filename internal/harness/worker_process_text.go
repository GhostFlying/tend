package harness

import (
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/GhostFlying/tend/internal/protocol"
)

// WorkerProcessTextBuffer keeps only actual uncompleted native text. Readers
// take snapshots without changing the event cursor or creating new facts.
type WorkerProcessTextBuffer struct {
	mu    sync.Mutex
	items []*processTextItem
}

type processTextItem struct {
	thread, turn, item string
	text               strings.Builder
	truncated          bool
}

// Observe returns partial items left at a native turn boundary.
func (p *WorkerProcessTextBuffer) Observe(thread string, native Event) []protocol.WorkerProcessText {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch native.Kind {
	case EventAgentMessageDelta:
		if thread != "" && native.TurnID != "" && native.ItemID != "" && native.Text != "" {
			p.append(thread, native.TurnID, native.ItemID, native.Text)
		}
	case EventItemCompleted:
		if native.ItemType != ItemAgentMessage {
			break
		}
		for i, item := range p.items {
			if item.matches(thread, native.TurnID) && item.item == native.ItemID {
				copy(p.items[i:], p.items[i+1:])
				p.items[len(p.items)-1] = nil
				p.items = p.items[:len(p.items)-1]
				break
			}
		}
	case EventTurnCompleted:
		return p.drain(thread, native.TurnID)
	}
	return nil
}

func (p *WorkerProcessTextBuffer) Snapshot(thread, turn string) []protocol.WorkerProcessText {
	p.mu.Lock()
	defer p.mu.Unlock()
	var result []protocol.WorkerProcessText
	for _, item := range p.items {
		if item.matches(thread, turn) {
			result = append(result, item.snapshot())
		}
	}
	return result
}

func (p *WorkerProcessTextBuffer) Drain(thread, turn string) []protocol.WorkerProcessText {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.drain(thread, turn)
}

func (p *WorkerProcessTextBuffer) drain(thread, turn string) []protocol.WorkerProcessText {
	kept := p.items[:0]
	var result []protocol.WorkerProcessText
	for _, item := range p.items {
		if item.matches(thread, turn) {
			result = append(result, item.snapshot())
		} else {
			kept = append(kept, item)
		}
	}
	clear(p.items[len(kept):])
	p.items = kept
	return result
}

func (i *processTextItem) matches(thread, turn string) bool {
	return (thread == "" || i.thread == thread) && (turn == "" || i.turn == turn)
}

func (i *processTextItem) snapshot() protocol.WorkerProcessText {
	return protocol.WorkerProcessText{
		ThreadID: i.thread, TurnID: i.turn, ItemID: i.item, Text: i.text.String(), Truncated: i.truncated,
	}
}

func (p *WorkerProcessTextBuffer) append(thread, turn, id, text string) {
	for _, item := range p.items {
		if item.matches(thread, turn) && item.item == id {
			item.append(text)
			return
		}
	}
	if len(p.items) == protocol.MaxWorkerProcessTextItems {
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
	available := protocol.MaxWorkerProcessTextBytes - i.text.Len()
	if len(text) > available {
		for available > 0 && !utf8.RuneStart(text[available]) {
			available--
		}
		text, i.truncated = text[:available], true
	}
	i.text.WriteString(text)
}
