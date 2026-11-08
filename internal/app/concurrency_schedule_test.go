package app

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestConcurrencyIdleMinuteWaitsForFactsAndRetainsMissingProof(t *testing.T) {
	h := newHarness(t)
	concurrencyConfigure(t, h, "create", "shared", 0, protocol.ConfigureConcurrencyPayload{Name: "Shared", Min: 1, Initial: 2, Max: 4})
	ticks, waits := 0, 0
	wait := func(ctx context.Context, wake <-chan struct{}, delay time.Duration) bool {
		waits++
		if delay != 0 && delay <= time.Minute {
			t.Fatalf("empty group scheduled recurring sampling: %v", delay)
		}
		if waits == 1 {
			h.clock.Advance(time.Minute)
			select {
			case <-wake:
				t.Fatal("idle sample generated another change")
			default:
			}
			h.app.Changes().Notify()
			return waitConcurrency(ctx, wake, delay)
		}
		return false
	}
	h.app.runConcurrency(t.Context(), slog.New(slog.NewTextHandler(io.Discard, nil)), wait, func() { ticks++ })
	if ticks != 2 || waits != 2 {
		t.Fatal("unexpected idle sampling", ticks, waits)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		s, err := concurrencyState(t.Context(), tx.Q(), "shared")
		if err == nil && (s.Group.SaturatedMillis != 0 || s.Group.Current != 2) {
			t.Fatal("idle gap became saturation proof", s)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrencySchedulesCooldownAndEvidenceRetention(t *testing.T) {
	h := newHarness(t)
	concurrencyConfigure(t, h, "create", "shared", 0, protocol.ConfigureConcurrencyPayload{Name: "Shared", Min: 1, Initial: 2, Max: 4})
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		s, err := concurrencyState(t.Context(), tx.Q(), "shared")
		if err != nil {
			return err
		}
		s.Group.CooldownUntil = h.clock.Now().Add(30 * time.Second).UnixMilli()
		return saveConcurrency(t.Context(), tx, s)
	}); err != nil {
		t.Fatal(err)
	}
	if delay, err := h.app.concurrencyDelay(t.Context()); err != nil || delay != 30*time.Second {
		t.Fatal("cooldown deadline changed", delay, err)
	}
	h.clock.Advance(31 * time.Second)
	delay, err := h.app.concurrencyDelay(t.Context())
	want := concurrencyRetention - 31*time.Second + time.Millisecond
	if err != nil || delay != want {
		t.Fatal("expired cooldown remained a recurring timer", delay, err)
	}
	h.clock.Advance(delay)
	if err := h.app.tickConcurrency(t.Context()); err != nil {
		t.Fatal(err)
	}
	if delay, err := h.app.concurrencyDelay(t.Context()); err != nil || delay != 0 {
		t.Fatal("retention did not park after cleanup", delay, err)
	}
}

func TestConcurrencyActiveNotificationsCoalesceToThirtySeconds(t *testing.T) {
	h := newHarness(t)
	concurrencyConfigure(t, h, "create", "shared", 0, protocol.ConfigureConcurrencyPayload{Name: "Shared", Min: 1, Initial: 2, Max: 4})
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		state, err := concurrencyState(t.Context(), tx.Q(), "shared")
		if err != nil {
			return err
		}
		state.Group.CooldownUntil = h.clock.Now().Add(30 * time.Second).UnixMilli()
		return saveConcurrency(t.Context(), tx, state)
	}); err != nil {
		t.Fatal(err)
	}
	ticks, waits := 0, 0
	wait := func(_ context.Context, wake <-chan struct{}, delay time.Duration) bool {
		waits++
		if waits > 30 {
			return false
		}
		if ticks != 1 || delay != time.Duration(31-waits)*time.Second {
			t.Fatal("notification accelerated samples", ticks, waits, delay)
		}
		h.clock.Advance(time.Second)
		h.app.Changes().Notify()
		<-wake
		return true
	}
	h.app.runConcurrency(t.Context(), slog.New(slog.NewTextHandler(io.Discard, nil)), wait, func() { ticks++ })
	if ticks != 2 {
		t.Fatal("sample count", ticks)
	}
}
