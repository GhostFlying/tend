package coordination

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/testkit"
)

type disconnectTransport struct {
	*memoryTransport
	reading chan struct{}
	closes  atomic.Int64
}

func (t *disconnectTransport) Call(ctx context.Context, method string, input, output any) error {
	if method == "usage" {
		close(t.reading)
		<-ctx.Done()
		return ctx.Err()
	}
	if method == "close" {
		t.closes.Add(1)
	}
	return t.memoryTransport.Call(ctx, method, input, output)
}

func disconnectSession(t *testing.T) (context.Context, *Session, *disconnectTransport, harness.Thread, harness.Thread) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	fake := testkit.NewHarness()
	native, err := fake.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg := harness.ThreadConfig{Sandbox: "read-only", ApprovalPolicy: "never"}
	original, err := native.StartThread(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := native.Close(); err != nil {
		t.Fatal(err)
	}
	native, err = fake.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	transport := &disconnectTransport{memoryTransport: &memoryTransport{events: make(chan Event, 16), driverContext: ctx}, reading: make(chan struct{})}
	transport.driver = &Driver{Session: permissionSession{native.(*testkit.FakeSession)}, Emit: transport.emit}
	session := New(ctx, transport)
	t.Cleanup(func() { cancel(); _ = session.Close() })
	old, err := session.ResumeThread(ctx, original.ID(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	current, err := session.StartThread(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, session, transport, old, current
}

func TestConnectionDisconnectEndsMigratedThreadsAndPendingCalls(t *testing.T) {
	ctx, session, transport, old, current := disconnectSession(t)
	pending := make(chan error, 1)
	go func() { _, err := current.(harness.ContextManager).ContextUsage(ctx); pending <- err }()
	<-transport.reading
	for i, th := range []harness.Thread{old, current} {
		if err := transport.emit(ctx, Event{ThreadID: th.ID(), SourceRunID: "original-run", SourceSequence: int64(41 + i), Native: &harness.Event{Kind: harness.EventItemCompleted, ItemID: "retained-tail"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := transport.emit(ctx, Event{Disconnected: true, SourceRunID: "original-run", SourceSequence: 43}); err != nil {
		t.Fatal(err)
	}
	// Every attachment must see its own prior tail followed by the connection end.
	// Requiring both is deterministic even when map traversal picks either first.
	for threadIndex, th := range []harness.Thread{old, current} {
		for i := 0; i < 2; i++ {
			select {
			case event, ok := <-th.Events():
				if !ok {
					t.Fatal("connection discarded a queued tail", th.ID(), i)
				}
				if event.OriginRunID != "original-run" {
					t.Fatal("original run identity lost", event)
				}
				if i == 0 && (event.ItemID != "retained-tail" || event.OriginSequence != int64(41+threadIndex)) {
					t.Fatal("tail order changed", event)
				}
				if i == 1 && (event.Kind != harness.EventCoordinatorDisconnected || event.OriginSequence != 43) {
					t.Fatal("connection end lost", event)
				}
			case <-time.After(250 * time.Millisecond):
				t.Fatal("connection end was not delivered to every migrated attachment", th.ID(), i)
			}
		}
	}
	select {
	case <-session.Done():
	case <-time.After(250 * time.Millisecond):
		t.Fatal("connection retained its session after disconnect")
	}
	select {
	case err := <-pending:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("pending observation did not preserve cancellation", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("pending call outlived the connection")
	}
	for _, th := range []harness.Thread{old, current} {
		if _, ok := <-th.Events(); ok {
			t.Fatal("ended thread stream remained open")
		}
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if transport.closes.Load() != 1 {
		t.Fatal("repeated close repeated native cleanup", transport.closes.Load())
	}
}

func TestConnectionDisconnectDoesNotWaitForAbandonedFullThread(t *testing.T) {
	ctx, session, transport, old, _ := disconnectSession(t)
	for i := 0; i < 256; i++ {
		if err := transport.emit(ctx, Event{ThreadID: old.ID(), Native: &harness.Event{Kind: harness.EventItemStarted, ItemID: "queued-tail"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := transport.emit(ctx, Event{Disconnected: true}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-session.Done():
	case <-time.After(250 * time.Millisecond):
		t.Fatal("abandoned full thread retained the entire connection")
	}
	count := 0
	for event := range old.Events() {
		if event.ItemID != "queued-tail" {
			t.Fatal("changed queued tail", event)
		}
		count++
	}
	if count != 256 {
		t.Fatal("disconnect discarded queued events", count)
	}
}

func TestHistoricalDisconnectKeepsCurrentConnectionAndThreads(t *testing.T) {
	ctx, session, transport, old, current := disconnectSession(t)
	for _, id := range []string{old.ID(), ""} {
		if err := transport.emit(ctx, Event{Historical: true, ThreadID: id, Disconnected: true, SourceRunID: "previous-run", SourceSequence: 9}); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		thread harness.Thread
		count  int
	}{{old, 2}, {current, 1}} {
		for i := 0; i < test.count; i++ {
			select {
			case event := <-test.thread.Events():
				if event.Kind != harness.EventCoordinatorHistoryEnd || event.OriginRunID != "previous-run" || event.OriginSequence != 9 {
					t.Fatal("historical end changed identity or meaning", event)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
	}
	select {
	case <-session.Done():
		t.Fatal("historical end closed current connection")
	default:
	}
	id, err := current.StartTurn(ctx, harness.TurnInput{Text: "one actual current input"})
	if err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case event := <-current.Events():
			if event.Kind == harness.EventTurnCompleted && event.TurnID == id {
				return
			}
		case <-ctx.Done():
			t.Fatal("history blocked current native execution", ctx.Err())
		}
	}
}
