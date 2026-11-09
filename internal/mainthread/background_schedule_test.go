package mainthread

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestBackgroundPoolSleepsAcrossIdleMinuteAndWakesForChange(t *testing.T) {
	clock := testkit.NewClock()
	s, err := mainFixtureTemplate.Open(t.Context(), filepath.Join(t.TempDir(), "ledger.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ids := testkit.NewIDGen(clock, 7)
	a := app.New(s, ids, clock.Now)
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		for i := range 1000 {
			if _, err := tx.Exec(t.Context(), `INSERT INTO token_usage_local_sources(source_key,conversation_id,source_json,coverage,writer_stopped,updated_at,collection_state,collection_json) VALUES(?,'main','{}','caught_up',1,?,'retired','{"state":"retired","epoch":1,"revision":1,"execution_evidence":"unknown"}')`, fmt.Sprint("historical-", i), clock.Now().UnixMilli()); err != nil {
				return err
			}
			if err := tx.InsertOutbox(t.Context(), store.OutboxRow{OutboxID: ids.New(protocol.PrefixOutbox), RequestID: ids.New(protocol.PrefixRequest), Destination: fmt.Sprint("background_thread:retired-", i), Operation: app.OpHandleEvent, PayloadJSON: `{}`, CreatedAt: clock.Now().UnixMilli()}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	scans, waits := 0, 0
	p := &BackgroundPool{App: a, scanned: func() { scans++ }}
	p.wait = func(ctx context.Context, changed, completed <-chan struct{}, delay time.Duration) bool {
		waits++
		if delay != 0 {
			t.Fatalf("idle history created a recurring timer: %v", delay)
		}
		if waits == 1 {
			clock.Advance(time.Minute)
			select {
			case <-changed:
				t.Fatal("virtual time alone created a fact change")
			case <-completed:
				t.Fatal("idle history attached a process")
			default:
			}
			a.SchedulingChanges().Notify()
			return waitBackground(ctx, changed, completed, delay)
		}
		return false
	}
	p.Run(t.Context())
	if scans != 2 || waits != 2 {
		t.Fatalf("scans=%d waits=%d; expected startup and one real wake", scans, waits)
	}
}

func TestBackgroundAttachmentFailureBackoffAndCapacityWake(t *testing.T) {
	clock := testkit.NewClock()
	s, err := mainFixtureTemplate.Open(t.Context(), filepath.Join(t.TempDir(), "ledger.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a := app.New(s, testkit.NewIDGen(clock, 4), clock.Now)
	p := &BackgroundPool{App: a, finished: app.NewBroadcast(), active: map[string]*Manager{"original": {}}, remote: map[string]bool{}, database: map[string]bool{}}
	for _, expected := range []time.Duration{2, 4, 8, 16, 30, 30} {
		wake := p.finished.Wait()
		p.finishAttachment("original", true)
		if got := p.retries["original"].at.Sub(clock.Now()); got != expected*time.Second {
			t.Fatalf("failure retry = %v, want %v", got, expected*time.Second)
		}
		select {
		case <-wake:
		default:
			t.Fatal("released capacity did not wake waiter")
		}
		clock.Advance(expected * time.Second)
	}
	p.finishAttachment("original", false)
	if len(p.retries) != 0 || len(p.active) != 0 {
		t.Fatal("successful attachment retained failed retry or occupied capacity")
	}
}

func TestBackgroundPoolIgnoresMinuteOfUnrelatedReplyUpdates(t *testing.T) {
	clock := testkit.NewClock()
	s, err := mainFixtureTemplate.Open(t.Context(), filepath.Join(t.TempDir(), "ledger.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ids := testkit.NewIDGen(clock, 7)
	a := app.New(s, ids, clock.Now)
	scans, waits := 0, 0
	p := &BackgroundPool{App: a, scanned: func() { scans++ }}
	p.wait = func(ctx context.Context, changed, completed <-chan struct{}, delay time.Duration) bool {
		waits++
		if waits > 1 {
			return false
		}
		for range 100 {
			clock.Advance(600 * time.Millisecond)
			op, err := a.Execute(ctx, protocol.Principal{Kind: protocol.PrincipalSystem, Thread: "foreground"}, &protocol.Envelope{RequestID: ids.New(protocol.PrefixRequest), Operation: "conversation.record_agent_message", Target: protocol.Target{Type: "message"}, Payload: json.RawMessage(`{"body":"streamed text"}`)}, app.RecordAgentMessage{})
			if err != nil || op.Error != nil {
				t.Fatal(op, err)
			}
			select {
			case <-changed:
				t.Fatal("reply text woke background eligibility scan")
			default:
			}
		}
		a.SchedulingChanges().Notify()
		return waitBackground(ctx, changed, completed, delay)
	}
	p.Run(t.Context())
	if scans != 2 || waits != 2 {
		t.Fatal("expected startup and eligibility change only", scans, waits)
	}
}

func TestBackgroundExpiredAttachmentRetryCannotAccelerateFailedScans(t *testing.T) {
	clock := testkit.NewClock()
	s, err := mainFixtureTemplate.Open(t.Context(), filepath.Join(t.TempDir(), "ledger.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a := app.New(s, testkit.NewIDGen(clock, 7), clock.Now)
	p := &BackgroundPool{App: a}
	waits := 0
	p.wait = func(ctx context.Context, _, _ <-chan struct{}, delay time.Duration) bool {
		waits++
		if waits == 1 {
			p.mu.Lock()
			p.deferRetryLocked("failed-conversation")
			p.mu.Unlock()
			clock.Advance(2 * time.Second)
			// Fail the candidate query while keeping the startup and timer reads
			// available, so the normal failed-scan path owns this retry.
			if err := s.Write(ctx, func(tx *store.Tx) error {
				_, err := tx.Exec(ctx, `ALTER TABLE agent_conversations RENAME TO unavailable_agent_conversations`)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			return true
		}
		if delay < 2*time.Second {
			t.Fatal("expired attachment defeated scan error backoff", delay)
		}
		return false
	}
	p.Run(t.Context())
	if waits != 2 {
		t.Fatal("unexpected retry count", waits)
	}
}
