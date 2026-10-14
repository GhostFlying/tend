package store

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/GhostFlying/tend/internal/testkit"
)

func openTest(t *testing.T) (*Store, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tend.db")
	s, err := Open(context.Background(), p, testkit.NewClock().Now)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, p
}

func exec(t *testing.T, s *Store, query string, args ...any) error {
	t.Helper()
	return s.Write(context.Background(), func(tx *Tx) error {
		_, err := tx.Exec(context.Background(), query, args...)
		return err
	})
}

func mustExec(t *testing.T, s *Store, query string, args ...any) {
	t.Helper()
	if err := exec(t, s, query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func insertTask(t *testing.T, s *Store, id string) {
	t.Helper()
	mustExec(t, s, `INSERT INTO tasks (task_id, title, lifecycle, control_mode, control_revision, spec_version, priority, block_reason, created_at, updated_at)
		VALUES (?, 't', 'draft', 'managed', 1, 1, 0, 'none', 0, 0)`, id)
}

func insertAction(t *testing.T, s *Store, id, task, effectKey string) error {
	t.Helper()
	return exec(t, s, `INSERT INTO plan_actions (action_id, task_id, kind, effect_key, trigger_json, target_spec_version, authorization_json, generation, status, created_request_id, created_at, updated_at)
		VALUES (?, ?, 'implement', ?, '{}', 1, '{}', 1, 'pending', 'req', 0, 0)`, id, task, effectKey)
}

func TestOpenAppliesMigrationsOnce(t *testing.T) {
	s, p := openTest(t)
	var version int
	if err := s.Read(context.Background(), func(tx *ReadTx) error {
		return tx.Q().QueryRowContext(context.Background(), `SELECT MAX(version) FROM schema_migrations`).Scan(&version)
	}); err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if version != len(migrations) {
		t.Errorf("schema version = %d, want %d", version, len(migrations))
	}
	insertTask(t, s, "task_a")
	s.Close()

	// Reopening must not reapply migrations or lose data.
	s2, err := Open(context.Background(), p, testkit.NewClock().Now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	var n int
	if err := s2.Read(context.Background(), func(tx *ReadTx) error {
		return tx.Q().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM tasks`).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("tasks after reopen = %d, want 1", n)
	}
}

func TestPragmas(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	err := s.Write(ctx, func(tx *Tx) error {
		var mode string
		var fk int
		if err := tx.Q().QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil {
			return err
		}
		if err := tx.Q().QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil {
			return err
		}
		if mode != "wal" || fk != 1 {
			t.Errorf("journal_mode = %q, foreign_keys = %d; want wal, 1", mode, fk)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestReaderCannotWrite(t *testing.T) {
	s, _ := openTest(t)
	err := s.Read(context.Background(), func(tx *ReadTx) error {
		_, err := tx.tx.ExecContext(context.Background(), `INSERT INTO inbox (source, source_event_id, received_at) VALUES ('a', 'b', 0)`)
		return err
	})
	if err == nil {
		t.Error("write through the read pool succeeded")
	}
}

func TestWriteRollsBackOnError(t *testing.T) {
	s, _ := openTest(t)
	errBoom := errors.New("boom")
	err := s.Write(context.Background(), func(tx *Tx) error {
		if _, err := tx.Exec(context.Background(), `INSERT INTO inbox (source, source_event_id, received_at) VALUES ('a', 'b', 0)`); err != nil {
			return err
		}
		return errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("Write() error = %v, want %v", err, errBoom)
	}
	var n int
	if err := s.Read(context.Background(), func(tx *ReadTx) error {
		return tx.Q().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM inbox`).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("rows after rollback = %d, want 0", n)
	}
}

func TestForeignKeysEnforced(t *testing.T) {
	s, _ := openTest(t)
	err := exec(t, s, `INSERT INTO task_specs (task_id, spec_version, body_json, created_at) VALUES ('missing', 1, '{}', 0)`)
	if err == nil {
		t.Error("spec for a missing task was accepted")
	}
}

func TestDedupConstraints(t *testing.T) {
	s, _ := openTest(t)
	insertTask(t, s, "task_a")

	if err := insertAction(t, s, "act_1", "task_a", "implement|task_a|1|-|1"); err != nil {
		t.Fatal(err)
	}
	t.Run("effect key", func(t *testing.T) {
		err := insertAction(t, s, "act_2", "task_a", "implement|task_a|1|-|1")
		if !IsUniqueViolation(err) {
			t.Errorf("duplicate effect key: err = %v, want unique violation", err)
		}
	})
	t.Run("run generation", func(t *testing.T) {
		const q = `INSERT INTO runs (run_id, task_id, action_id, generation, role, desired_state, observed_state, created_at, updated_at)
			VALUES (?, 'task_a', 'act_1', 1, 'implement', 'run', 'starting', 0, 0)`
		mustExec(t, s, q, "run_1")
		if err := exec(t, s, q, "run_2"); !IsUniqueViolation(err) {
			t.Errorf("second run for the same action generation: err = %v, want unique violation", err)
		}
	})
	t.Run("plan commit", func(t *testing.T) {
		const q = `INSERT INTO plan_commits (source_message_id, commit_seq, request_id, created_at) VALUES ('msg_1', 1, ?, 0)`
		mustExec(t, s, q, "req_1")
		if err := exec(t, s, q, "req_2"); !IsUniqueViolation(err) {
			t.Errorf("replayed plan commit: err = %v, want unique violation", err)
		}
	})
	t.Run("not a unique violation", func(t *testing.T) {
		if IsUniqueViolation(errors.New("UNIQUE constraint failed: fake")) {
			t.Error("plain error text classified as unique violation")
		}
		if IsUniqueViolation(nil) {
			t.Error("nil classified as unique violation")
		}
	})
}

func TestConcurrentWritesSerialize(t *testing.T) {
	s, _ := openTest(t)
	mustExec(t, s, `INSERT INTO inbox (source, source_event_id, received_at) VALUES ('counter', 'c', 0)`)
	const workers = 32
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for range workers {
		wg.Go(func() {
			errs <- s.Write(context.Background(), func(tx *Tx) error {
				var n int64
				if err := tx.Q().QueryRowContext(context.Background(), `SELECT received_at FROM inbox WHERE source = 'counter'`).Scan(&n); err != nil {
					return err
				}
				_, err := tx.Exec(context.Background(), `UPDATE inbox SET received_at = ? WHERE source = 'counter'`, n+1)
				return err
			})
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Write() error = %v", err)
		}
	}
	var got int64
	if err := s.Read(context.Background(), func(tx *ReadTx) error {
		return tx.Q().QueryRowContext(context.Background(), `SELECT received_at FROM inbox WHERE source = 'counter'`).Scan(&got)
	}); err != nil {
		t.Fatal(err)
	}
	if got != workers {
		t.Errorf("counter = %d, want %d (lost update)", got, workers)
	}
}
