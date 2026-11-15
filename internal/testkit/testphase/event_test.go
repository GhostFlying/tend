package testphase

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestSeparateEventsDoNotSpendRegistrationBudgetOnPreparation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ready, registered := make(chan string, 1), make(chan string, 1)
		go func() {
			time.Sleep(70 * time.Second)
			ready <- "original-thread"
			time.Sleep(20 * time.Second)
			registered <- "original-run"
		}()
		if value, err := Await(t, t.Context(), "migration and runner ready", 5*time.Minute, ready, nil); err != nil || value != "original-thread" {
			t.Fatal(value, err)
		}
		if value, err := Await(t, t.Context(), "wait committed", time.Minute, registered, nil); err != nil || value != "original-run" {
			t.Fatal(value, err)
		}
	})
}

func TestEventWaitPreservesFailureCancellationAndFiniteWatchdog(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		event := make(chan string)
		failures := make(chan string, 1)
		failures <- "original failure"
		if _, err := Await(t, t.Context(), "failed", time.Minute, event, failures); err == nil || !strings.Contains(err.Error(), "original failure") {
			t.Fatal(err)
		}
		parent, cancel := context.WithCancelCause(t.Context())
		sentinel := errors.New("parent stopped")
		cancel(sentinel)
		if _, err := Await(t, parent, "cancelled", time.Minute, event, nil); !errors.Is(err, sentinel) {
			t.Fatal(err)
		}
		if _, err := Await(t, t.Context(), "missing receipt", time.Minute, event, nil); !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "missing receipt") {
			t.Fatal(err)
		}
	})
}
