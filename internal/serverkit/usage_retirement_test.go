package serverkit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness/nativeusage"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestUnavailableHistoryRetiresAcrossRestartAndSleepsForAnHour(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clock := testkit.NewClock()
		s, err := nativeUsageFixture.Open(t.Context(), filepath.Join(t.TempDir(), "ledger.db"), clock.Now)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		a := app.New(s, testkit.NewIDGen(clock, 61), clock.Now)
		home := t.TempDir()
		for i := range 170 {
			thread := fmt.Sprint("thread-", i)
			raw, _ := json.Marshal(nativeusage.Source{Provider: "codex", Path: filepath.Join(home, thread+".jsonl"), ThreadID: thread, SessionID: thread})
			if err := a.RegisterLocalUsage(t.Context(), app.LocalUsageSource{ID: thread, Source: raw, WriterStopped: true}); err != nil {
				t.Fatal(err)
			}
		}
		first := centralUsage{app: a, now: clock.Now, log: slog.New(slog.DiscardHandler), discovery: nativeusage.NewDiscovery()}
		if delay, err := first.syncWork(t.Context()); err != nil || delay != 2*time.Second {
			t.Fatal(delay, err)
		}
		clock.Advance(5 * time.Minute)
		recovered := centralUsage{app: a, now: clock.Now, log: slog.New(slog.DiscardHandler), discovery: nativeusage.NewDiscovery()}
		if delay, err := recovered.syncWork(t.Context()); err != nil || delay != 0 {
			t.Fatal("restart renewed failure budget", delay, err)
		}
		all, _ := a.LocalUsageSources(t.Context(), "")
		for _, source := range all {
			if source.Collection.RetirementReason != "unavailable" || source.Coverage != "pending" {
				t.Fatal(source)
			}
		}
		if err := s.Write(t.Context(), func(tx *store.Tx) error {
			_, err := tx.Exec(t.Context(), `CREATE TRIGGER reject_retired_updates BEFORE UPDATE ON token_usage_local_sources BEGIN SELECT RAISE(ABORT,'retired history write'); END`)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		var queries atomic.Int64
		recovered.sources = func(ctx context.Context) ([]app.LocalUsageSource, error) {
			queries.Add(1)
			return a.PendingLocalUsageSources(ctx, "")
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan struct{})
		go func() { defer close(done); recovered.run(ctx) }()
		synctest.Wait()
		before := queries.Load()
		clock.Advance(time.Hour)
		time.Sleep(time.Hour)
		synctest.Wait()
		if queries.Load() != before || recovered.discovery.Stats() != (nativeusage.IOStats{}) {
			t.Fatal("idle loop performed historical work", queries.Load(), before, recovered.discovery.Stats())
		}
		cancel()
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("collector did not exit")
		}
	})
}

func TestUsageCommitRetryDoesNotReopenHistoryOrLoseReadSamples(t *testing.T) {
	for _, count := range []int{1, 600} {
		t.Run(fmt.Sprint(count), func(t *testing.T) { testUsageCommitRetry(t, count) })
	}
}

func testUsageCommitRetry(t *testing.T, count int) {
	clock := testkit.NewClock()
	s, err := nativeUsageFixture.Open(t.Context(), filepath.Join(t.TempDir(), "ledger.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a := app.New(s, testkit.NewIDGen(clock, 62), clock.Now)
	path := filepath.Join(t.TempDir(), "history.jsonl")
	body := `{"type":"session_meta","payload":{"id":"thread","session_id":"thread"}}` + "\n"
	for i := range count {
		body += fmt.Sprintf(`{"type":"token_usage_record","ordinal":%d,"payload":{"thread_id":"thread","session_id":"thread","turn_id":"turn","response_id":"request-%d","usage":{"total_tokens":12}}}`+"\n", i+1, i)
	}
	if err = os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(nativeusage.Source{Provider: "codex", Path: path, ThreadID: "thread", SessionID: "thread"})
	if err = a.RegisterLocalUsage(t.Context(), app.LocalUsageSource{ID: "source", Source: raw, WriterStopped: true}); err != nil {
		t.Fatal(err)
	}
	if err = s.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `CREATE TRIGGER fail_usage_commit BEFORE UPDATE ON token_usage_local_sources BEGIN SELECT RAISE(ABORT,'injected usage commit'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	c := centralUsage{app: a, now: clock.Now, log: slog.New(slog.DiscardHandler), discovery: nativeusage.NewDiscovery()}
	if _, err = c.syncWork(t.Context()); err == nil {
		t.Fatal("injected commit succeeded")
	}
	stats := c.discovery.Stats()
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = s.Write(t.Context(), func(tx *store.Tx) error { _, err := tx.Exec(t.Context(), `DROP TRIGGER fail_usage_commit`); return err }); err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Second)
	delay, err := c.syncWork(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if count > 256 && delay == 0 {
		t.Fatal("commit retry parked with unread pages")
	}
	if c.discovery.Stats() != stats {
		t.Fatal("commit retry reopened a removed history", stats, c.discovery.Stats())
	}
	if err = s.Read(t.Context(), func(tx *store.ReadTx) error {
		var facts int
		err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM token_usage_facts`).Scan(&facts)
		if facts != min(count, 256) {
			t.Fatal("read samples were lost", facts)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if count > 256 {
		if err = os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		c.drain(t.Context())
		if err = s.Read(t.Context(), func(tx *store.ReadTx) error {
			var facts int
			err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM token_usage_facts`).Scan(&facts)
			if facts != count {
				t.Fatal("pagination lost or duplicated usage", facts, count)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	all, _ := a.LocalUsageSources(t.Context(), "")
	if all[0].Collection.State != protocol.UsageRetired || all[0].Coverage != "caught_up" {
		t.Fatal(all)
	}
}
