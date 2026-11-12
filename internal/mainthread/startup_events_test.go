package mainthread

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
)

func TestRunnerStartupAppliesFinalEventsBeforeHistoryReadCompletes(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	events := make(chan harness.Event, 1)
	events <- harness.Event{Kind: harness.EventRequestUsage}
	reading := make(chan struct{})
	applied := make(chan struct{})
	err := drainStartupEvents(ctx, events, func(ctx context.Context, event harness.Event) error {
		if event.Kind != harness.EventRequestUsage {
			t.Error(event)
		}
		select {
		case <-reading:
		case <-ctx.Done():
			return ctx.Err()
		}
		close(applied)
		return nil
	}, func(ctx context.Context) error {
		close(reading)
		select {
		case <-applied:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	if err != nil {
		t.Fatal("startup blocked final event application", err)
	}
}

func TestRunnerStartupJoinsHistoryReadAfterEventFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	events := make(chan harness.Event, 1)
	events <- harness.Event{}
	stopped := make(chan struct{})
	failure := errors.New("event requires reconciliation")
	err := drainStartupEvents(ctx, events, func(context.Context, harness.Event) error { return failure }, func(ctx context.Context) error {
		defer close(stopped)
		<-ctx.Done()
		return ctx.Err()
	})
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	default:
		t.Fatal("startup history reader was left running")
	}
}

func TestRunnerStartupReadFailureKeepsAlreadyQueuedTail(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	events := make(chan harness.Event, 16)
	for i := 0; i < 16; i++ {
		events <- harness.Event{Kind: harness.EventItemCompleted, OriginSequence: int64(i + 1)}
	}
	close(events)
	failure := errors.New("native read is unavailable after connection end")
	reading := make(chan struct{})
	applied := 0
	err := drainStartupEvents(ctx, events, func(ctx context.Context, event harness.Event) error {
		select {
		case <-reading:
		case <-ctx.Done():
			return ctx.Err()
		}
		applied++
		if event.OriginSequence != int64(applied) {
			t.Fatal("startup changed original queued order", event)
		}
		return nil
	}, func(context.Context) error { close(reading); return failure })
	if applied != 16 {
		t.Fatal("observation failure skipped the original queued tail", applied, err)
	}
	if err == nil {
		t.Fatal("ended startup was reported ready")
	}
}
