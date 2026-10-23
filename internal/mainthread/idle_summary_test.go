package mainthread

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/dispatch"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func seedIdleConversation(t *testing.T, f *handoffFixture) {
	t.Helper()
	ctx := context.Background()
	err := f.e.store.Write(ctx, func(tx *store.Tx) error {
		if _, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: "seed-message", Author: "agent", Body: "Earlier conversation", CreatedAt: f.clock.Now().UnixMilli()}); err != nil {
			return err
		}
		_, err := tx.InsertEvent(ctx, store.EventRow{EventID: "seed-event", Source: "test", ObjectType: "conversation", ObjectID: "main", Kind: "conversation.message", BodyJSON: `{}`, CreatedAt: f.clock.Now().UnixMilli()})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	f.e.m.mu.Lock()
	f.e.m.session = f.session
	f.e.m.thread = f.source
	f.e.m.mu.Unlock()
}
func setIdleOverride(t *testing.T, f *handoffFixture, minutes int64) {
	t.Helper()
	ctx := context.Background()
	settings, err := f.e.app.HandoffSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(protocol.SetConversationHandoffSettingsPayload{IdleMinutes: minutes})
	revision := settings.Revision
	res, err := f.e.app.Execute(ctx, owner, &protocol.Envelope{RequestID: f.e.ids.New(protocol.PrefixRequest), Operation: "conversation.set_handoff_settings", Target: protocol.Target{Type: "conversation"}, ExpectedControlRevision: &revision, Payload: b}, app.SetConversationHandoffSettings{})
	if err != nil || res.Status == protocol.OpFailed {
		t.Fatalf("set idle override: %+v %v", res, err)
	}
}

func TestIdleSummaryFiftyMinuteBoundaryAndCurrentCache(t *testing.T) {
	f := newHandoffFixture(t)
	seedIdleConversation(t, f)
	f.clock.Advance(50*time.Minute - time.Millisecond)
	if err := f.e.m.idleSummary(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, forks, _ := f.session.counts()
	if forks != 0 {
		t.Fatal("summary ran before configured idle boundary")
	}
	f.clock.Advance(time.Millisecond)
	if err := f.e.m.idleSummary(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, forks, _ = f.session.counts()
	if forks != 1 {
		t.Fatalf("forks at boundary=%d", forks)
	}
	f.clock.Advance(time.Hour)
	if err := f.e.m.idleSummary(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, forks, _ = f.session.counts()
	if forks != 1 {
		t.Fatal("current snapshot was summarized repeatedly")
	}
}

func TestIdleSummaryZeroAndSharedOwnerOverride(t *testing.T) {
	for _, kind := range []string{"server-off", "owner-off", "owner-shorter"} {
		t.Run(kind, func(t *testing.T) {
			f := newHandoffFixture(t)
			seedIdleConversation(t, f)
			switch kind {
			case "server-off":
				f.e.m.cfg.SummaryIdleMinutes = 0
			case "owner-off":
				setIdleOverride(t, f, 0)
			case "owner-shorter":
				setIdleOverride(t, f, 10)
			}
			f.clock.Advance(10 * time.Minute)
			if err := f.e.m.idleSummary(context.Background()); err != nil {
				t.Fatal(err)
			}
			_, forks, _ := f.session.counts()
			want := 0
			if kind == "owner-shorter" {
				want = 1
			}
			if forks != want {
				t.Fatalf("forks=%d want=%d", forks, want)
			}
			status, err := f.e.m.HandoffStatus(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if kind == "owner-off" && (status.IdleMinutes != 0 || status.Revision != 1) {
				t.Fatalf("owner override not reflected %+v", status)
			}
		})
	}
}

func TestIdleSummaryDoesNotForkWithActiveOrPendingOwnerWork(t *testing.T) {
	for _, kind := range []string{"active", "pending"} {
		t.Run(kind, func(t *testing.T) {
			f := newHandoffFixture(t)
			seedIdleConversation(t, f)
			f.clock.Advance(time.Hour)
			if kind == "active" {
				f.e.m.mu.Lock()
				f.e.m.active = &turn{turnID: "active-owner-turn"}
				f.e.m.mu.Unlock()
			} else {
				f.e.say("Queued owner work")
			}
			if err := f.e.m.idleSummary(context.Background()); err != nil {
				t.Fatal(err)
			}
			_, forks, _ := f.session.counts()
			if forks != 0 {
				t.Fatal("summary forked while source did not cover owner work")
			}
		})
	}
}

func TestIdleSummaryAllowsOwnerDeliveryAndRetainsStaleResult(t *testing.T) {
	f := newHandoffFixture(t)
	seedIdleConversation(t, f)
	hash, err := currentToolsetHash()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.e.store.Write(context.Background(), func(tx *store.Tx) error {
		_, err := tx.Exec(context.Background(), `UPDATE main_thread_bindings SET toolset_hash=? WHERE instance='main' AND thread_id=?`, hash, f.source.ID())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	f.session.summaryTurn = func(th *testkit.FakeThread, id string, _ harness.TurnInput) string {
		close(started)
		select {
		case <-release:
			th.Say(id, "summary", "Snapshot taken before new owner activity")
			return "completed"
		case <-f.session.Done():
			return ""
		}
	}
	f.e.h.Turn = func(th *testkit.FakeThread, id string, _ harness.TurnInput) string {
		th.Say(id, "owner-reply", "Owner message handled while summary waits")
		return "completed"
	}
	f.e.m.cfg.Open = func(context.Context) (harness.Session, error) { return f.session, nil }
	f.e.m.cfg.MaintenanceInterval = time.Hour
	f.e.signal = dispatch.NewSignal()
	f.e.start()
	defer f.e.stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := f.e.m.waitThread(ctx); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(time.Hour)
	finished := make(chan error, 1)
	go func() { finished <- f.e.m.idleSummary(ctx) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("summary did not start")
	}
	status, err := f.e.m.HandoffStatus(ctx)
	if err != nil || !status.Preparing || !status.Pending {
		t.Fatalf("running summary status=%+v err=%v", status, err)
	}
	if _, ok, err := f.e.m.prepareSummary(ctx, f.session, f.source); err != nil || ok {
		t.Fatalf("parallel manual preparation should defer to running summary: ok=%v err=%v", ok, err)
	}
	if err := f.e.m.idleSummary(ctx); err != nil {
		t.Fatal(err)
	}
	_, forks, _ := f.session.counts()
	if forks != 1 {
		t.Fatal("overlapping maintenance created another summary fork")
	}
	f.e.say("New owner message during private summary")
	eventually(t, "owner turn completes before summary", func() bool { return f.e.count(`SELECT COUNT(*) FROM main_turns WHERE status='completed'`) == 1 })
	select {
	case err := <-finished:
		t.Fatalf("summary ended before release: %v", err)
	default:
	}
	unblock()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("summary did not finish")
	}
	summary, exists, err := f.e.app.HandoffSummary(context.Background(), "main")
	if err != nil || !exists {
		t.Fatalf("stale summary not retained: %v", err)
	}
	current, err := f.e.app.HandoffSnapshot(context.Background(), "main")
	if err != nil {
		t.Fatal(err)
	}
	if summary.Snapshot.Covers(current) {
		t.Fatal("summary from before owner activity marked current")
	}
	if len(f.e.h.Starts()) != 1 {
		t.Fatal("private summary turn appeared in source harness")
	}
	if f.e.count(`SELECT COUNT(*) FROM conversation_messages WHERE body LIKE '%Snapshot taken before%'`) > 0 {
		t.Fatal("private summary leaked into Web history")
	}
}

func TestIdleSummaryFailureBackoffSurvivesManagerRestart(t *testing.T) {
	f := newHandoffFixture(t)
	seedIdleConversation(t, f)
	old := f.saveSummary(t)
	if err := f.e.store.Write(context.Background(), func(tx *store.Tx) error {
		_, err := tx.Exec(context.Background(), `UPDATE conversation_messages SET body='newer content' WHERE message_id='seed-message'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	f.session.summaryTurn = func(*testkit.FakeThread, string, harness.TurnInput) string { return "failed" }
	f.clock.Advance(50 * time.Minute)
	if err := f.e.m.idleSummary(context.Background()); err == nil {
		t.Fatal("summary failure was hidden")
	}
	saved, exists, err := f.e.app.HandoffSummary(context.Background(), "main")
	if err != nil || !exists || saved.ID != old.ID {
		t.Fatal("failed attempt discarded prior summary")
	}
	restarted := New(f.e.app, f.e.m.context, f.e.m.tools, f.e.m.cfg)
	restarted.mu.Lock()
	restarted.session = f.session
	restarted.thread = f.source
	restarted.mu.Unlock()
	if err := restarted.idleSummary(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, forks, _ := f.session.counts()
	if forks != 1 {
		t.Fatal("restart bypassed durable failure backoff")
	}
	f.clock.Advance(15*time.Minute - time.Millisecond)
	if err := restarted.idleSummary(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, forks, _ = f.session.counts()
	if forks != 1 {
		t.Fatal("retry occurred before backoff boundary")
	}
	f.clock.Advance(time.Millisecond)
	if err := restarted.idleSummary(context.Background()); err == nil {
		t.Fatal("expected retry to reach failing model")
	}
	_, forks, _ = f.session.counts()
	if forks != 2 {
		t.Fatalf("retry not attempted at backoff boundary: forks=%d", forks)
	}
}
