package app

import (
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"testing"
)

func usageNotifications(t *testing.T, h *harness) int {
	t.Helper()
	var count int
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM events WHERE kind='token_usage.changed'`).Scan(&count)
	}); err != nil {
		t.Fatal(err)
	}
	return count
}
func TestUsageChangeNotifiesOncePerTransactionAndNotOnReplay(t *testing.T) {
	h := newHarness(t)
	s := nativeSample(h.clock.Now().UnixMilli())
	second := s
	second.RecordID = "other"
	second.RequestID = "other"
	before := usageNotifications(t, h)
	for range 2 {
		if err := h.app.RecordNativeConversationUsage(t.Context(), "main", []protocol.TokenUsageSample{s, second}); err != nil {
			t.Fatal(err)
		}
	}
	if got := usageNotifications(t, h); got != before+1 {
		t.Fatal("batch or replay duplicated notification", got, before)
	}
}
func TestUsageNotificationFailureRollsBackFactsAndDoesNotWake(t *testing.T) {
	h := newHarness(t)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `CREATE TRIGGER reject_usage_notification BEFORE INSERT ON events WHEN NEW.kind='token_usage.changed' BEGIN SELECT RAISE(ABORT,'injected event failure'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	wake := h.app.Changes().Wait()
	before := h.count("token_usage_facts")
	if err := h.app.RecordNativeConversationUsage(t.Context(), "main", []protocol.TokenUsageSample{nativeSample(h.clock.Now().UnixMilli())}); err == nil {
		t.Fatal("injection did not fail")
	}
	if h.count("token_usage_facts") != before {
		t.Fatal("failed notification leaked usage")
	}
	select {
	case <-wake:
		t.Fatal("rollback published a change")
	default:
	}
}
func TestStaleCollectorCannotAdvanceFactsAfterWriterStop(t *testing.T) {
	h := newHarness(t)
	src := LocalUsageSource{ID: "local", Source: []byte(`{}`), Cursors: []byte(`{}`)}
	if err := h.app.RegisterLocalUsage(t.Context(), src); err != nil {
		t.Fatal(err)
	}
	if err := h.app.StopLocalUsage(t.Context(), "main", ""); err != nil {
		t.Fatal(err)
	}
	before := h.count("token_usage_facts")
	if err := h.app.CommitLocalUsage(t.Context(), src, []protocol.TokenUsageSample{nativeSample(h.clock.Now().UnixMilli())}); protocol.CodeOf(err) != protocol.CodeStaleRevision {
		t.Fatal(err)
	}
	if h.count("token_usage_facts") != before {
		t.Fatal("stale lifecycle advanced usage")
	}
}

func TestUsageCoverageOnlyChangeNotifiesReaders(t *testing.T) {
	h := newHarness(t)
	src := LocalUsageSource{ID: "review-source", WriterStopped: true, Source: []byte(`{"thread_id":"thread","session_id":"session"}`), Cursors: []byte(`{}`), Coverage: "syncing"}
	if err := h.app.RegisterLocalUsage(t.Context(), src); err != nil {
		t.Fatal(err)
	}
	refreshLocalUsageFixture(t, h.app, &src)
	if err := h.app.CommitLocalUsage(t.Context(), src, nil); err != nil {
		t.Fatal(err)
	}
	before := usageNotifications(t, h)
	src.Coverage = "caught_up"
	refreshLocalUsageFixture(t, h.app, &src)
	if err := h.app.CommitLocalUsage(t.Context(), src, nil); err != nil {
		t.Fatal(err)
	}
	if after := usageNotifications(t, h); after != before+1 {
		t.Fatalf("coverage changed without usage notification: events before=%d after=%d", before, after)
	}
}

func TestPrivateSummaryUsageDoesNotInvalidateFrozenSummaryInput(t *testing.T) {
	h := newHarness(t)
	if _, err := h.app.BindMainThread(t.Context(), "main", "source", "schema", "source.rollout"); err != nil {
		t.Fatal(err)
	}
	before, err := h.app.HandoffSnapshot(t.Context(), "main")
	if err != nil {
		t.Fatal(err)
	}
	source := LocalUsageSource{ID: "private-summary", Source: []byte(`{"thread_id":"summary","session_id":"summary","purpose":"internal_summary"}`)}
	if err := h.app.RegisterLocalUsage(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	sample := nativeSample(h.clock.Now().UnixMilli())
	sample.Purpose = "internal_summary"
	if err := h.app.RecordNativeConversationUsage(t.Context(), "main", []protocol.TokenUsageSample{sample}); err != nil {
		t.Fatal(err)
	}
	after, err := h.app.HandoffSnapshot(t.Context(), "main")
	if err != nil || !before.Covers(after) {
		t.Fatal("accounting changed unchanged summary input", before, after, err)
	}
}
