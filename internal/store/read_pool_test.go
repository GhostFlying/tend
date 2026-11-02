package store

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestReadPoolReusesConcurrentSnapshots(t *testing.T) {
	s, _ := openTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	mustExec(t, s, `INSERT INTO inbox (source, source_event_id, received_at) VALUES ('pool', 'snapshot', 0)`)

	// Two bursts must reuse the retained connections while each transaction
	// still observes its own snapshot and refuses writes.
	for round := range 2 {
		ready := make(chan error, readIdleConnections)
		errs := make(chan error, readIdleConnections)
		release := make(chan struct{})
		var wg sync.WaitGroup
		for range readIdleConnections {
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
		for range readIdleConnections {
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
		for range readIdleConnections {
			if err := <-errs; err != nil {
				t.Error(err)
			}
		}
		stats := s.read.Stats()
		if stats.Idle != readIdleConnections || stats.MaxIdleClosed != 0 || stats.MaxOpenConnections != 0 {
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
