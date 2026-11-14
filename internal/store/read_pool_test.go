package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestReadPoolReusesConcurrentSnapshots(t *testing.T) {
	s, _ := openTest(t)
	limit := readPoolConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	mustExec(t, s, `INSERT INTO inbox (source, source_event_id, received_at) VALUES ('pool', 'snapshot', 0)`)

	// Two bursts must reuse the retained connections while each transaction
	// still observes its own snapshot and refuses writes.
	for round := range 2 {
		ready := make(chan error, limit)
		errs := make(chan error, limit)
		release := make(chan struct{})
		var wg sync.WaitGroup
		for range limit {
			wg.Go(func() {
				errs <- s.Read(ctx, func(tx *ReadTx) error {
					var before int
					err := tx.Q().QueryRowContext(ctx, `SELECT received_at FROM inbox WHERE source = 'pool'`).Scan(&before)
					ready <- err
					if err != nil {
						return err
					}
					select {
					case <-release:
					case <-ctx.Done():
						return ctx.Err()
					}
					var after, queryOnly int
					if err := tx.Q().QueryRowContext(ctx, `SELECT received_at FROM inbox WHERE source = 'pool'`).Scan(&after); err != nil {
						return err
					}
					if before != round || after != before {
						return fmt.Errorf("snapshot changed: before=%d after=%d round=%d", before, after, round)
					}
					if err := tx.Q().QueryRowContext(ctx, `PRAGMA query_only`).Scan(&queryOnly); err != nil {
						return err
					}
					if queryOnly != 1 {
						return fmt.Errorf("reader query_only = %d", queryOnly)
					}
					if _, err := tx.tx.ExecContext(ctx, `INSERT INTO inbox (source, source_event_id, received_at) VALUES ('bad', 'write', 0)`); err == nil {
						return fmt.Errorf("reader accepted a write")
					}
					return nil
				})
			})
		}
		for range limit {
			select {
			case err := <-ready:
				if err != nil {
					t.Error(err)
				}
			case <-ctx.Done():
				t.Error(ctx.Err())
			}
		}
		if err := exec(t, s, `UPDATE inbox SET received_at = received_at + 1 WHERE source = 'pool'`); err != nil {
			t.Error(err)
		}
		close(release)
		wg.Wait()
		for range limit {
			if err := <-errs; err != nil {
				t.Error(err)
			}
		}
		stats := s.read.Stats()
		if stats.Idle != limit || stats.MaxIdleClosed != 0 || stats.MaxOpenConnections != limit {
			t.Errorf("read pool after burst: %+v", stats)
		}
	}
	if s.write.Stats().MaxOpenConnections != 1 {
		t.Error("write pool no longer serializes writes")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if s.read.Stats().OpenConnections != 0 || s.write.Stats().OpenConnections != 0 {
		t.Error("Close left database connections open")
	}
}

func TestReadPoolBoundsAndCancelsWaiters(t *testing.T) {
	s, _ := openTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	limit := readPoolConnections()
	release := make(chan struct{})
	ready := make(chan struct{}, limit)
	done := make(chan error, limit)
	for range limit {
		go func() {
			done <- s.Read(ctx, func(tx *ReadTx) error {
				var n int
				if err := tx.Q().QueryRowContext(ctx, `SELECT 1`).Scan(&n); err != nil {
					return err
				}
				ready <- struct{}{}
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
		}()
	}
	var finish sync.Once
	finishReads := func() {
		finish.Do(func() {
			close(release)
			for range limit {
				if err := <-done; err != nil {
					t.Error(err)
				}
			}
		})
	}
	defer finishReads()
	for range limit {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	stats := s.PoolStats()
	if stats.Reader.InUse != limit || stats.Reader.OpenConnections != limit || stats.Writer.MaxOpenConnections != 1 {
		t.Fatalf("pool not bounded: %+v", stats)
	}
	waitCtx, stop := context.WithTimeout(ctx, 50*time.Millisecond)
	defer stop()
	called := false
	err := s.Read(waitCtx, func(*ReadTx) error { called = true; return nil })
	if !errors.Is(err, context.DeadlineExceeded) || called {
		t.Fatalf("pool waiter: called=%v err=%v", called, err)
	}
	stats = s.PoolStats()
	if stats.Reader.OpenConnections > limit || stats.Reader.WaitCount == 0 {
		t.Fatalf("wait was not recorded: %+v", stats)
	}
	finishReads()
	if err := s.Read(ctx, func(tx *ReadTx) error {
		var n int
		return tx.Q().QueryRowContext(ctx, `SELECT 1`).Scan(&n)
	}); err != nil {
		t.Fatalf("read after cancelled waiter/release: %v", err)
	}
}
