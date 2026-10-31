package sandbox

import (
	"context"
	"sync"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/testkit"
)

// scriptedSessions adapts native interruption to the lifetime of each script.
// Testkit's generic fake behavior remains unchanged for production-package tests.
type scriptedSessions struct {
	harness *testkit.Harness
	ctx     context.Context
	mu      sync.Mutex
	threads map[*testkit.FakeThread]*scriptedThread
	scripts chan struct{}
}

type scriptedSession struct {
	*testkit.FakeSession
	owner   *scriptedSessions
	mu      sync.Mutex
	threads []*scriptedThread
}

type scriptedThread struct {
	*testkit.FakeThread
	ctx   context.Context
	mu    sync.Mutex
	turns map[string]*scriptedTurnLifetime
}

type scriptedTurnLifetime struct {
	ctx        context.Context
	cancel     context.CancelFunc
	done       chan struct{}
	completion sync.Mutex
}

func (s *scriptedSessions) open(ctx context.Context) (harness.Session, error) {
	s.mu.Lock()
	if s.scripts == nil {
		s.scripts = make(chan struct{}, 1)
		s.scripts <- struct{}{}
	}
	s.mu.Unlock()
	native, err := s.harness.Open(ctx)
	if err != nil {
		return nil, err
	}
	return &scriptedSession{FakeSession: native.(*testkit.FakeSession), owner: s}, nil
}

func (s *scriptedSession) wrap(native harness.Thread) harness.Thread {
	thread := &scriptedThread{FakeThread: native.(*testkit.FakeThread), ctx: s.owner.ctx, turns: map[string]*scriptedTurnLifetime{}}
	s.owner.mu.Lock()
	if s.owner.threads == nil {
		s.owner.threads = map[*testkit.FakeThread]*scriptedThread{}
	}
	s.owner.threads[thread.FakeThread] = thread
	s.owner.mu.Unlock()
	s.mu.Lock()
	s.threads = append(s.threads, thread)
	s.mu.Unlock()
	return thread
}

func (s *scriptedSession) StartThread(ctx context.Context, config harness.ThreadConfig) (harness.Thread, error) {
	native, err := s.FakeSession.StartThread(ctx, config)
	if err != nil {
		return nil, err
	}
	return s.wrap(native), nil
}

func (s *scriptedSession) ResumeThread(ctx context.Context, id string, config harness.ThreadConfig) (harness.Thread, error) {
	native, err := s.FakeSession.ResumeThread(ctx, id, config)
	if err != nil {
		return nil, err
	}
	return s.wrap(native), nil
}

func (t *scriptedThread) lifetime(id string) *scriptedTurnLifetime {
	t.mu.Lock()
	defer t.mu.Unlock()
	if turn := t.turns[id]; turn != nil {
		return turn
	}
	ctx, cancel := context.WithCancel(t.ctx)
	turn := &scriptedTurnLifetime{ctx: ctx, cancel: cancel, done: make(chan struct{})}
	t.turns[id] = turn
	return turn
}

func (t *scriptedThread) Interrupt(ctx context.Context, id string) error {
	turn := t.lifetime(id)
	turn.completion.Lock()
	turn.cancel()
	turn.completion.Unlock()
	select {
	case <-turn.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	// The fake emits completion only after the script has stopped producing
	// events. Its event queue preserves this ordering for the real coordinator.
	return t.FakeThread.Interrupt(ctx, id)
}

func (s *scriptedSession) Close() error {
	s.mu.Lock()
	threads := append([]*scriptedThread{}, s.threads...)
	s.mu.Unlock()
	var turns []*scriptedTurnLifetime
	for _, thread := range threads {
		thread.mu.Lock()
		for _, turn := range thread.turns {
			turn.cancel()
			turns = append(turns, turn)
		}
		thread.mu.Unlock()
	}
	for _, turn := range turns {
		<-turn.done
	}
	return s.FakeSession.Close()
}

func (s *scriptedSessions) play(native *testkit.FakeThread, id string, input harness.TurnInput, play func(context.Context, *testkit.FakeThread, string, harness.TurnInput) string) string {
	s.mu.Lock()
	thread := s.threads[native]
	s.mu.Unlock()
	turn := thread.lifetime(id)
	defer close(turn.done)
	defer turn.cancel()
	status := play(turn.ctx, native, id, input)
	turn.completion.Lock()
	if turn.ctx.Err() == nil && status != "" {
		native.Emit(harness.Event{Kind: harness.EventTurnCompleted, TurnID: id, TurnStatus: status})
	}
	turn.completion.Unlock()
	// Completion belongs to this adapter, not FakeThread's post-callback emit.
	return ""
}
