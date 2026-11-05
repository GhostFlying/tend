package sandbox

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

type replyDeltaIdentity struct {
	ThreadID string
	TurnID   string
	SourceID string
}

// interruptionGate is local to one test's interruption request.
type interruptionGate struct {
	armed    atomic.Bool
	idReady  chan struct{}
	sourceID string // Published by closing idReady.
	arrivals chan replyDeltaIdentity
	held     chan replyDeltaIdentity
	mu       sync.Mutex
	claimed  replyDeltaIdentity
	repeated []replyDeltaIdentity
}

func newInterruptionGate() *interruptionGate {
	return &interruptionGate{idReady: make(chan struct{}), arrivals: make(chan replyDeltaIdentity, 16), held: make(chan replyDeltaIdentity, 1)}
}

func (g *interruptionGate) bind(sourceID string) {
	g.sourceID = sourceID
	close(g.idReady)
}

func (g *interruptionGate) onDelta(ctx context.Context, thread, turn string, input harness.TurnInput) {
	if !g.armed.Load() {
		return
	}
	identity := replyDeltaIdentity{ThreadID: thread, TurnID: turn, SourceID: input.UserMessageID}
	select {
	case g.arrivals <- identity:
	default:
	}
	if input.Repair {
		return
	}
	// The first delta can arrive before the owner POST returns its source ID.
	select {
	case <-g.idReady:
	case <-ctx.Done():
		return
	}
	if input.UserMessageID != g.sourceID || ctx.Err() != nil {
		return
	}
	g.mu.Lock()
	if g.claimed.TurnID == "" {
		g.claimed = identity
		g.held <- identity
	} else if g.claimed != identity {
		g.repeated = append(g.repeated, identity)
		g.mu.Unlock()
		return
	}
	g.mu.Unlock()
	// Only native interruption or cleanup releases the selected turn.
	<-ctx.Done()
}

func (g *interruptionGate) duplicates() []replyDeltaIdentity {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]replyDeltaIdentity(nil), g.repeated...)
}

const conversationWindowMaxPages = 16

func readConversationWindow(after int64, fetch func(int64) (protocol.ConversationPage, error)) (protocol.ConversationPage, error) {
	var window protocol.ConversationPage
	cursor := after
	for pageNumber := 0; pageNumber < conversationWindowMaxPages; pageNumber++ {
		page, err := fetch(cursor)
		if err != nil {
			return protocol.ConversationPage{}, fmt.Errorf("read conversation after %d: %w", cursor, err)
		}
		if page.More && len(page.Items) == 0 {
			return protocol.ConversationPage{}, fmt.Errorf("conversation has More without items after %d", cursor)
		}
		for _, item := range page.Items {
			if item.Seq <= cursor {
				return protocol.ConversationPage{}, fmt.Errorf("conversation cursor did not advance: after=%d seq=%d", cursor, item.Seq)
			}
			cursor = item.Seq
			window.Items = append(window.Items, item)
		}
		if !page.More {
			return window, nil
		}
	}
	return protocol.ConversationPage{}, fmt.Errorf("conversation still has More after %d pages: baseline=%d cursor=%d", conversationWindowMaxPages, after, cursor)
}
