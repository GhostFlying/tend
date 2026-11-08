package mainthread

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/nativeusage"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

type usageHistoryThread struct {
	harness.Thread
	source nativeusage.Source
}

func (t usageHistoryThread) UsageSource() nativeusage.Source { return t.source }

func TestUsageIdleReadSleepsAndResumesWithoutRewritingCursor(t *testing.T) {
	clock := testkit.NewClock()
	s, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "ledger.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a := app.New(s, testkit.NewIDGen(clock, 47), clock.Now)
	m := New(a, nil, nil, Config{Now: clock.Now, Log: slog.New(slog.DiscardHandler)})
	path := filepath.Join(t.TempDir(), "history.jsonl")
	header := `{"type":"session_meta","payload":{"id":"thread","session_id":"session"}}` + "\n"
	if err := os.WriteFile(path, []byte(header), 0600); err != nil {
		t.Fatal(err)
	}
	m.registerNativeUsage(t.Context(), usageHistoryThread{source: nativeusage.Source{Provider: "traex", Path: path, ThreadID: "thread", SessionID: "session"}}, "")
	if delay := m.syncNativeUsageState(t.Context(), false); delay != 0 {
		t.Fatal(delay)
	}
	// Updating a cursor in stable idle is a defect, even if it writes identical bytes.
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `CREATE TRIGGER reject_idle_cursor BEFORE UPDATE ON token_usage_local_sources BEGIN SELECT RAISE(ABORT,'idle cursor write'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Minute)
	if delay := m.syncNativeUsageState(t.Context(), false); delay != 0 {
		t.Fatal("idle read attempted persistence", delay)
	}
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `DROP TRIGGER reject_idle_cursor`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	sample := `{"type":"token_usage_record","ordinal":1,"payload":{"thread_id":"thread","session_id":"session","turn_id":"turn","response_id":"request","usage":{"total_tokens":12}}}` + "\n"
	if err := os.WriteFile(path, []byte(header+sample), 0600); err != nil {
		t.Fatal(err)
	}
	if delay := m.syncNativeUsageState(t.Context(), false); delay != 0 {
		t.Fatal(delay)
	}
	if err := s.Read(t.Context(), func(tx *store.ReadTx) error {
		var count int
		err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM token_usage_facts`).Scan(&count)
		if count != 1 {
			t.Fatal("resume did not ingest late record", count)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// A failed close may read but must not mark the writer stopped.
	m.syncNativeUsageState(t.Context(), false)
	sources, err := a.LocalUsageSources(t.Context(), "main")
	if err != nil || len(sources) != 1 || sources[0].WriterStopped {
		t.Fatal(sources, err)
	}
	// Only the confirmed stop delegates final collection to the central owner.
	m.syncNativeUsageState(t.Context(), true)
	sources, err = a.LocalUsageSources(t.Context(), "main")
	if err != nil || !sources[0].WriterStopped {
		t.Fatal(sources, err)
	}
	var cursors map[string]nativeusage.Cursor
	if err := json.Unmarshal(sources[0].Cursors, &cursors); err != nil || cursors[path].Offset != int64(len(header+sample)) {
		t.Fatal(cursors, err)
	}
}

func idleUsageManager(t *testing.T) (*Manager, *app.App, *store.Store, *testkit.Clock, string) {
	t.Helper()
	clock := testkit.NewClock()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "ledger.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	a := app.New(db, testkit.NewIDGen(clock, 27), clock.Now)
	m := New(a, nil, nil, Config{Now: clock.Now, Log: slog.New(slog.DiscardHandler)})
	path := filepath.Join(t.TempDir(), "root.jsonl")
	if err := os.WriteFile(path, []byte("{\"type\":\"session_meta\",\"payload\":{\"id\":\"root\",\"session_id\":\"root\"}}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m.registerNativeUsage(t.Context(), usageHistoryThread{source: nativeusage.Source{Path: path, ThreadID: "root", SessionID: "root", Provider: "traex"}}, "")
	if delay := m.syncNativeUsageState(t.Context(), false); delay != 0 {
		t.Fatal(delay)
	}
	return m, a, db, clock, path
}

func TestUsageSuccessfulRetryDisarmsIdleCollector(t *testing.T) {
	m, _, _, clock, path := idleUsageManager(t)
	if err := os.Rename(path, path+".away"); err != nil {
		t.Fatal(err)
	}
	if delay := m.syncNativeUsageState(t.Context(), false); delay != 2*time.Second {
		t.Fatal(delay)
	}
	if err := os.Rename(path+".away", path); err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Second)
	delay := m.syncNativeUsageState(t.Context(), false)
	if delay != 0 {
		t.Fatalf("successful unchanged read left retry armed: delay=%v, retries=%v", delay, m.usageRetries)
	}
}

func TestUsageFinalIdlePassDiscoversNewChild(t *testing.T) {
	m, _, db, clock, path := idleUsageManager(t)
	child := `{"type":"session_meta","payload":{"id":"child","session_id":"root"}}` + "\n" + `{"type":"token_usage_record","ordinal":1,"payload":{"thread_id":"child","session_id":"root","turn_id":"turn","response_id":"child-request","usage":{"total_tokens":12}}}` + "\n"
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "child.jsonl"), []byte(child), 0600); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Second)
	m.wakeNativeUsage()
	m.syncNativeUsageState(t.Context(), false)
	clock.Advance(2 * time.Second)
	delay := m.syncNativeUsageState(t.Context(), false)
	count := 0
	if err := db.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM token_usage_facts`).Scan(&count)
	}); err != nil {
		t.Fatal(err)
	}
	t.Logf("after completion and settling: delay=%v, child facts=%d", delay, count)
	if count != 1 {
		t.Fatalf("idle collector disarmed before discovering child: facts=%d delay=%v", count, delay)
	}
}

func TestBufferedCompletionWakePreservesSettlingDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, _, db, _, path := idleUsageManager(t)
		m.cfg.Now = time.Now
		wake := make(chan struct{}, 1)
		m.usageWake = wake
		// Both the completion flag and its notification predate the collector.
		m.wakeNativeUsage()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan struct{})
		go func() { defer close(done); m.nativeUsageLoop(ctx, wake) }()
		synctest.Wait()
		time.Sleep(100 * time.Millisecond)
		child := `{"type":"session_meta","payload":{"id":"late-child","session_id":"root"}}` + "\n" + `{"type":"token_usage_record","ordinal":1,"payload":{"thread_id":"late-child","session_id":"root","turn_id":"turn","response_id":"late-response","usage":{"total_tokens":12}}}` + "\n"
		if err := os.WriteFile(filepath.Join(filepath.Dir(path), "late-child.jsonl"), []byte(child), 0600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		var count int
		if err := db.Read(t.Context(), func(tx *store.ReadTx) error {
			return tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM token_usage_facts`).Scan(&count)
		}); err != nil || count != 1 {
			t.Fatal("completion wake consumed delayed final obligation", count, err)
		}
		cancel()
		<-done
	})
}
