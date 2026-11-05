package sandbox

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

type gateScripts struct {
	t        *testing.T
	ctx      context.Context
	session  harness.Session
	returned chan string
}

func newGateScripts(t *testing.T, gate *interruptionGate) *gateScripts {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	h := testkit.NewHarness()
	sessions := &scriptedSessions{harness: h, ctx: ctx}
	returned := make(chan string, 8)
	h.Turn = func(thread *testkit.FakeThread, turn string, input harness.TurnInput) string {
		return sessions.play(thread, turn, input, func(ctx context.Context, native *testkit.FakeThread, id string, input harness.TurnInput) string {
			// Enter the actual first-delta hook inside the real script lifetime.
			native.Emit(harness.Event{Kind: harness.EventAgentMessageDelta, TurnID: id, ItemID: id + "_reply", ItemType: harness.ItemAgentMessage, Text: "fixture delta"})
			gate.onDelta(ctx, native.ID(), id, input)
			returned <- input.UserMessageID
			if ctx.Err() != nil {
				return "interrupted"
			}
			return "completed"
		})
	}
	session, err := sessions.open(ctx)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		if err := session.Close(); err != nil {
			t.Error(err)
		}
		h.Wait()
	})
	return &gateScripts{t: t, ctx: ctx, session: session, returned: returned}
}

func (s *gateScripts) start(source string, repair bool) (harness.Thread, string) {
	s.t.Helper()
	thread, err := s.session.StartThread(s.ctx, harness.ThreadConfig{})
	if err != nil {
		s.t.Fatal(err)
	}
	turn, err := thread.StartTurn(s.ctx, harness.TurnInput{UserMessageID: source, Repair: repair})
	if err != nil {
		s.t.Fatal(err)
	}
	return thread, turn
}

func (s *gateScripts) awaitReturn(source string) {
	s.t.Helper()
	select {
	case got := <-s.returned:
		if err := s.ctx.Err(); err != nil {
			s.t.Fatalf("source %q returned only after fixture cancellation: %v", source, err)
		}
		if got != source {
			s.t.Fatalf("script returned for %q, want %q", got, source)
		}
	case <-s.ctx.Done():
		s.t.Fatalf("source %q stayed blocked: %v", source, s.ctx.Err())
	}
}

func (s *gateScripts) arrival(gate *interruptionGate) replyDeltaIdentity {
	s.t.Helper()
	select {
	case got := <-gate.arrivals:
		return got
	case <-s.ctx.Done():
		s.t.Fatal("script did not reach its first delta")
		return replyDeltaIdentity{}
	}
}

func TestInterruptionGateSelectsOneNativeTurn(t *testing.T) {
	gate := newInterruptionGate()
	gate.armed.Store(true)
	gate.bind("target")
	scripts := newGateScripts(t, gate)
	scripts.start("other-source", false)
	scripts.arrival(gate)
	scripts.awaitReturn("other-source")
	scripts.start("target", true)
	scripts.arrival(gate)
	scripts.awaitReturn("target")
	thread, turn := scripts.start("target", false)
	scripts.arrival(gate)
	select {
	case got := <-gate.held:
		want := replyDeltaIdentity{ThreadID: thread.ID(), TurnID: turn, SourceID: "target"}
		if got != want {
			t.Fatalf("held %+v, want exact native turn %+v", got, want)
		}
	case <-scripts.ctx.Done():
		t.Fatal("target did not enter the gate")
	}
	// A second native turn for the same source is visible, but never trapped.
	other, otherTurn := scripts.start("target", false)
	scripts.arrival(gate)
	scripts.awaitReturn("target")
	wantDuplicate := replyDeltaIdentity{ThreadID: other.ID(), TurnID: otherTurn, SourceID: "target"}
	duplicates := gate.duplicates()
	if len(duplicates) != 1 || duplicates[0] != wantDuplicate {
		t.Fatalf("duplicate native turns: %+v", duplicates)
	}
	select {
	case <-scripts.returned:
		t.Fatal("target returned before native interruption")
	default:
	}
	if err := thread.Interrupt(scripts.ctx, turn); err != nil {
		t.Fatal(err)
	}
	scripts.awaitReturn("target")
	for {
		select {
		case event := <-thread.Events():
			if event.Kind == harness.EventTurnCompleted {
				if event.TurnID != turn || event.TurnStatus != "interrupted" {
					t.Fatalf("unexpected native completion: %+v", event)
				}
				return
			}
		case <-scripts.ctx.Done():
			t.Fatal("native interruption completion missing")
		}
	}
}

func TestInterruptionGateWaitsForSourcePublication(t *testing.T) {
	gate := newInterruptionGate()
	gate.armed.Store(true)
	scripts := newGateScripts(t, gate)
	thread, turn := scripts.start("target", false)
	scripts.arrival(gate)
	scripts.start("other-source", false)
	scripts.arrival(gate)
	select {
	case got := <-gate.held:
		t.Fatalf("claimed a native turn before POST result: %+v", got)
	case <-scripts.returned:
		t.Fatal("first delta escaped before source publication")
	default:
	}
	gate.bind("target")
	scripts.awaitReturn("other-source")
	select {
	case got := <-gate.held:
		want := replyDeltaIdentity{ThreadID: thread.ID(), TurnID: turn, SourceID: "target"}
		if got != want {
			t.Fatalf("published source held %+v, want %+v", got, want)
		}
	case <-scripts.ctx.Done():
		t.Fatal("published target did not enter the gate before interruption")
	}
	if err := thread.Interrupt(scripts.ctx, turn); err != nil {
		t.Fatal(err)
	}
	scripts.awaitReturn("target")
}

func TestInterruptionGateCancellationBeforeSourcePublication(t *testing.T) {
	gate := newInterruptionGate()
	gate.armed.Store(true)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		gate.onDelta(ctx, "thread", "turn", harness.TurnInput{UserMessageID: "source"})
		close(done)
	}()
	select {
	case <-gate.arrivals:
	case <-ctx.Done():
		t.Fatal("unpublished callback did not arrive")
	}
	cancel() // The POST failed or test cleanup ran without publishing an ID.
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("unpublished source callback did not exit on cancellation")
	}
}

func TestConversationWindowReadsAllPagesAndRefreshesStates(t *testing.T) {
	first := protocol.ConversationPage{More: true}
	for seq := int64(11); seq <= 210; seq++ {
		first.Items = append(first.Items, protocol.ConversationItem{ConversationContent: protocol.ConversationContent{Seq: seq, MessageID: fmt.Sprint(seq)}})
	}
	first.Items[0].AssistantState = protocol.AssistantStreaming
	last := protocol.ConversationItem{ConversationContent: protocol.ConversationContent{Seq: 211, MessageID: "later-target"}}
	calls := []int64{}
	fetch := func(after int64) (protocol.ConversationPage, error) {
		calls = append(calls, after)
		switch after {
		case 10:
			return first, nil
		case 210:
			return protocol.ConversationPage{Items: []protocol.ConversationItem{last}}, nil
		default:
			return protocol.ConversationPage{}, fmt.Errorf("unexpected cursor %d", after)
		}
	}
	for _, state := range []protocol.AssistantState{protocol.AssistantStreaming, protocol.AssistantInterrupted} {
		first.Items[0].AssistantState = state // Same seq, updated persisted state.
		page, err := readConversationWindow(10, fetch)
		if err != nil || page.More || len(page.Items) != 201 {
			t.Fatalf("incomplete conversation window: count=%d more=%v err=%v", len(page.Items), page.More, err)
		}
		if page.Items[200].MessageID != "later-target" || page.Items[0].AssistantState != state {
			t.Fatalf("lost later target or same-seq update: first=%+v last=%+v", page.Items[0], page.Items[200])
		}
	}
	if fmt.Sprint(calls) != "[10 210 10 210]" {
		t.Fatalf("each read must revisit the original cursor: %v", calls)
	}
}

func TestConversationWindowRejectsBrokenPagination(t *testing.T) {
	for _, test := range []struct {
		name string
		page protocol.ConversationPage
	}{
		{name: "more without items", page: protocol.ConversationPage{More: true}},
		{name: "cursor does not advance", page: protocol.ConversationPage{Items: []protocol.ConversationItem{{ConversationContent: protocol.ConversationContent{Seq: 10}}}, More: true}},
		{name: "unordered rows", page: protocol.ConversationPage{Items: []protocol.ConversationItem{{ConversationContent: protocol.ConversationContent{Seq: 12}}, {ConversationContent: protocol.ConversationContent{Seq: 11}}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := readConversationWindow(10, func(int64) (protocol.ConversationPage, error) { return test.page, nil }); err == nil {
				t.Fatal("invalid pagination was accepted as complete")
			}
		})
	}
	t.Run("provider error", func(t *testing.T) {
		want := errors.New("fixture read error")
		_, err := readConversationWindow(10, func(int64) (protocol.ConversationPage, error) { return protocol.ConversationPage{}, want })
		if !errors.Is(err, want) {
			t.Fatalf("lost read error: %v", err)
		}
	})
	t.Run("bounded concurrent appends", func(t *testing.T) {
		calls := 0
		_, err := readConversationWindow(10, func(after int64) (protocol.ConversationPage, error) {
			calls++
			return protocol.ConversationPage{More: true, Items: []protocol.ConversationItem{{ConversationContent: protocol.ConversationContent{Seq: after + 1}}}}, nil
		})
		if err == nil || calls != conversationWindowMaxPages {
			t.Fatalf("unbounded or silently incomplete window: calls=%d err=%v", calls, err)
		}
	})
}
