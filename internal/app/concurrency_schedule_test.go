package app

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/domain"
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
			h.app.SchedulingChanges().Notify()
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
		h.app.SchedulingChanges().Notify()
		<-wake
		return true
	}
	h.app.runConcurrency(t.Context(), slog.New(slog.NewTextHandler(io.Discard, nil)), wait, func() { ticks++ })
	if ticks != 2 {
		t.Fatal("sample count", ticks)
	}
}

func TestConcurrencyIdleSchedulingNotificationsCoalesceToThirtySeconds(t *testing.T) {
	h := newHarness(t)
	concurrencyConfigure(t, h, "create", "shared", 0, protocol.ConfigureConcurrencyPayload{Name: "Shared", Min: 1, Initial: 2, Max: 4})
	ticks, waits := 0, 0
	wait := func(_ context.Context, wake <-chan struct{}, delay time.Duration) bool {
		waits++
		if waits > 30 {
			return false
		}
		if ticks != 1 || waits == 1 && delay <= time.Minute || waits > 1 && delay != time.Duration(31-waits)*time.Second {
			t.Fatal("idle notification accelerated sampling", ticks, waits, delay)
		}
		h.clock.Advance(time.Second)
		h.app.SchedulingChanges().Notify()
		<-wake
		return true
	}
	h.app.runConcurrency(t.Context(), slog.New(slog.NewTextHandler(io.Discard, nil)), wait, func() { ticks++ })
	if ticks != 2 {
		t.Fatal("sample count", ticks)
	}
}

func TestConcurrencyIdleReadExpiresWindowWithoutSamplingOrWriting(t *testing.T) {
	h := newHarness(t)
	concurrencyConfigure(t, h, "create", "shared", 0, protocol.ConfigureConcurrencyPayload{Name: "Shared", Min: 1, Initial: 2, Max: 4})
	now := h.clock.Now().UnixMilli()
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		state, err := concurrencyState(t.Context(), tx.Q(), "shared")
		if err != nil {
			return err
		}
		state.Group.ObservationSince = now - domain.ConcurrencyWindow.Milliseconds()
		state.Group.SaturatedMillis = domain.ConcurrencySaturation.Milliseconds()
		state.Group.SuccessfulTurns = 1
		state.Intervals = []domain.ConcurrencyInterval{{Start: now - domain.ConcurrencySaturation.Milliseconds(), End: now}}
		state.Successes = []int64{now}
		return saveConcurrency(t.Context(), tx, state)
	}); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(20 * time.Minute)
	group, err := h.app.ConcurrencyGroup(t.Context(), "shared")
	if err != nil || group.SaturatedMillis != (10*time.Minute).Milliseconds() || group.SuccessfulTurns != 1 {
		t.Fatal("window was not clipped", group, err)
	}
	h.clock.Advance(10*time.Minute + time.Millisecond)
	group, err = h.app.ConcurrencyGroup(t.Context(), "shared")
	if err != nil || group.SaturatedMillis != 0 || group.SuccessfulTurns != 0 {
		t.Fatal("expired idle evidence remained current", group, err)
	}
	page, err := h.app.ConcurrencyGroups(t.Context(), protocol.ConcurrencyQuery{Limit: 50})
	if err != nil || len(page.Items) != 1 || page.Items[0].SaturatedMillis != 0 || page.Items[0].SuccessfulTurns != 0 {
		t.Fatal("list retained expired evidence", page, err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		state, err := concurrencyState(t.Context(), tx.Q(), "shared")
		if err == nil && (state.Group.SaturatedMillis != domain.ConcurrencySaturation.Milliseconds() || len(state.Successes) != 1) {
			t.Fatal("read rewrote stored observations", state)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrencyIdleIgnoresMinuteOfUnrelatedReplyUpdates(t *testing.T) {
	h := newHarness(t)
	concurrencyConfigure(t, h, "create", "shared", 0, protocol.ConfigureConcurrencyPayload{Name: "Shared", Min: 1, Initial: 2, Max: 4})
	ticks, waits := 0, 0
	wait := func(ctx context.Context, wake <-chan struct{}, delay time.Duration) bool {
		waits++
		if waits > 1 {
			return false
		}
		for range 100 {
			h.clock.Advance(600 * time.Millisecond)
			visible := h.app.Changes().Wait()
			op := h.exec(protocol.Principal{Kind: protocol.PrincipalSystem, Thread: "foreground"}, h.env("conversation.record_agent_message", protocol.Target{Type: "message"}, map[string]any{"body": "streamed text"}, nil), RecordAgentMessage{})
			if op.Error != nil {
				t.Fatal(op.Error)
			}
			select {
			case <-visible:
			default:
				t.Fatal("reply update did not notify readers")
			}
			select {
			case <-wake:
				t.Fatal("reply text woke idle concurrency sampling")
			default:
			}
		}
		// A real configuration change must still wake the idle group.
		concurrencyConfigure(t, h, "update", "shared", 1, protocol.ConfigureConcurrencyPayload{Name: "Changed", Min: 1, Initial: 2, Max: 5})
		return waitConcurrency(ctx, wake, delay)
	}
	h.app.runConcurrency(t.Context(), slog.New(slog.NewTextHandler(io.Discard, nil)), wait, func() { ticks++ })
	if ticks != 2 || waits != 2 {
		t.Fatal("expected startup and configuration change only", ticks, waits)
	}
}
