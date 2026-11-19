package api

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"testing/synctest"
)

func TestRefreshAcksCoalesceAndRetryUntilShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := make(chan int64)
		results := make(chan error)
		acks := newRefreshAcks(func(ctx context.Context, through int64) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case calls <- through:
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case result := <-results:
				return result
			}
		}, slog.New(slog.DiscardHandler))
		defer acks.stop()
		acks.offer(5)
		if got := <-calls; got != 5 {
			t.Fatalf("initial write through %d, want 5", got)
		}

		acks.offer(10)
		acks.offer(8)
		acks.offer(12)
		acks.offer(12)
		results <- errors.New("temporary SQLite failure")
		if got := <-calls; got != 12 {
			t.Fatalf("retry through %d, want 12", got)
		}
		results <- errors.New("temporary SQLite failure")
		if got := <-calls; got != 12 {
			t.Fatalf("retry without a new offer through %d, want 12", got)
		}
		results <- nil
		acks.offer(10)
		acks.offer(12)
		synctest.Wait()
		select {
		case got := <-calls:
			t.Fatalf("duplicate write through %d", got)
		default:
		}

		acks.offer(20)
		if got := <-calls; got != 20 {
			t.Fatalf("new write through %d, want 20", got)
		}
		acks.stop()
		acks.offer(30)
		synctest.Wait()
		select {
		case got := <-calls:
			t.Fatalf("write after shutdown through %d", got)
		default:
		}
	})
}
