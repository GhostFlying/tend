package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/testkit"
)

// scriptedSessions adapts native interruption to the lifetime of each script.
// Testkit's generic fake behavior remains unchanged for production-package tests.
type scriptedSessions struct {
	harness    *testkit.Harness
	nativeRoot string
	ctx        context.Context
	mu         sync.Mutex
	threads    map[*testkit.FakeThread]*scriptedThread
	scripts    chan struct{}
}

type scriptedSession struct {
	*testkit.FakeSession
	owner    *scriptedSessions
	resumeMu sync.Mutex
	mu       sync.Mutex
	threads  []*scriptedThread
}

type scriptedThread struct {
	*testkit.FakeThread
	ctx          context.Context
	mu           sync.Mutex
	turns        map[string]*scriptedTurnLifetime
	nativePath   string
	nativeEvents chan harness.Event
	nativeDone   chan struct{}
	nativeCancel context.CancelFunc
	nativeErr    error
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

func (s *scriptedSession) wrap(native harness.Thread) (harness.Thread, error) {
	s.owner.mu.Lock()
	if s.owner.threads == nil {
		s.owner.threads = map[*testkit.FakeThread]*scriptedThread{}
	}
	fake := native.(*testkit.FakeThread)
	thread := s.owner.threads[fake]
	if thread == nil {
		thread = &scriptedThread{FakeThread: fake, ctx: s.owner.ctx, turns: map[string]*scriptedTurnLifetime{}}
		if err := thread.retainEvents(s.owner.nativeRoot); err != nil {
			s.owner.mu.Unlock()
			return nil, err
		}
		s.owner.threads[fake] = thread
	}
	s.owner.mu.Unlock()
	s.mu.Lock()
	s.threads = append(s.threads, thread)
	s.mu.Unlock()
	return thread, nil
}

func (s *scriptedSession) StartThread(ctx context.Context, config harness.ThreadConfig) (harness.Thread, error) {
	native, err := s.FakeSession.StartThread(ctx, config)
	if err != nil {
		return nil, err
	}
	return s.wrap(native)
}

func (s *scriptedSession) ResumeThread(ctx context.Context, id string, config harness.ThreadConfig) (harness.Thread, error) {
	s.resumeMu.Lock()
	defer s.resumeMu.Unlock()
	// A native thread has one event pump within this session. The generic
	// fake allocates another pump on every resume and replaces its map entry,
	// which would orphan the prior pump when retained attachments resume twice.
	s.mu.Lock()
	for _, thread := range s.threads {
		if thread.ID() == id {
			s.mu.Unlock()
			return thread, nil
		}
	}
	s.mu.Unlock()

	native, err := s.FakeSession.ResumeThread(ctx, id, config)
	if err != nil {
		return nil, err
	}
	return s.wrap(native)
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
	t.mu.Lock()
	turn := t.turns[id]
	t.mu.Unlock()
	if turn == nil {
		return t.FakeThread.Interrupt(ctx, id)
	}
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
	closeErr := s.FakeSession.Close()
	for _, thread := range threads {
		if thread.nativeCancel != nil {
			thread.nativeCancel()
			<-thread.nativeDone
			closeErr = errors.Join(closeErr, thread.nativeErr)
		}
	}
	return closeErr
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

// PermissionDefaults reports the permission configuration actually accepted by
// the scripted thread, as required by the real runner coordination transport.
func (t *scriptedThread) PermissionDefaults() harness.PermissionSettings {
	return harness.PermissionSettings{Sandbox: t.Config.Sandbox, ApprovalPolicy: t.Config.ApprovalPolicy}
}

// RolloutPath is the native per-thread JSONL original, never the all-thread
// restart snapshot. Both the real archive transport and preview use this path.
func (t *scriptedThread) RolloutPath() string { return t.nativePath }
func (t *scriptedThread) Events() <-chan harness.Event {
	if t.nativeEvents != nil {
		return t.nativeEvents
	}
	return t.FakeThread.Events()
}
func (t *scriptedThread) retainEvents(root string) error {
	if root == "" {
		return nil
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	hash := sha256.Sum256([]byte(t.ID()))
	path := filepath.Join(root, "rollout-"+hex.EncodeToString(hash[:])+".jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return err
	}
	if info.Size() == 0 {
		if err = json.NewEncoder(file).Encode(map[string]any{"type": "session_meta", "payload": map[string]string{"id": t.ID()}}); err != nil {
			file.Close()
			return err
		}
	} else {
		var header struct {
			Type    string `json:"type"`
			Payload struct {
				ID string `json:"id"`
			} `json:"payload"`
		}
		if err = json.NewDecoder(file).Decode(&header); err != nil || header.Type != "session_meta" || header.Payload.ID != t.ID() {
			file.Close()
			return fmt.Errorf("sandbox native original identity differs: %w", errors.Join(err, errors.New("expected thread header")))
		}
	}
	t.nativePath = path
	t.nativeEvents = make(chan harness.Event)
	t.nativeDone = make(chan struct{})
	ctx, cancel := context.WithCancel(t.ctx)
	t.nativeCancel = cancel
	go func() {
		defer close(t.nativeDone)
		defer close(t.nativeEvents)
		defer file.Close()
		for {
			select {
			case <-ctx.Done():
				return
			case event, open := <-t.FakeThread.Events():
				if !open {
					return
				}
				if err := json.NewEncoder(file).Encode(map[string]any{"type": "sandbox_event", "payload": event}); err != nil {
					t.nativeErr = fmt.Errorf("persist sandbox native event: %w", err)
					return
				}
				select {
				case t.nativeEvents <- event:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return nil
}
