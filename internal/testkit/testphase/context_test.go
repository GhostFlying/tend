package testphase

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestSlowPreparationDoesNotSpendScenarioBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// This exceeds the former whole-test minute without sleeping in real time.
		setup, endSetup := Context(t, t.Context(), "fixture", 10*time.Minute)
		time.Sleep(70 * time.Second)
		if setup.Err() != nil {
			t.Fatal("slow but healthy preparation failed", setup.Err())
		}
		endSetup()
		scenario, endScenario := Context(t, t.Context(), "owner session", time.Minute)
		defer endScenario()
		time.Sleep(59 * time.Second)
		if scenario.Err() != nil {
			t.Fatal("preparation consumed the scenario's budget", scenario.Err())
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if !errors.Is(scenario.Err(), context.DeadlineExceeded) || !strings.Contains(context.Cause(scenario).Error(), "owner session") {
			t.Fatal("scenario lost its finite, named watchdog", scenario.Err(), context.Cause(scenario))
		}
	})
}

func TestPhaseRetainsParentCancellationAndEarlierDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		parent, cancel := context.WithCancelCause(t.Context())
		sentinel := errors.New("service failed")
		ctx, finish := Context(t, parent, "recovery", time.Minute)
		defer finish()
		cancel(sentinel)
		if !errors.Is(context.Cause(ctx), sentinel) {
			t.Fatal("phase hid the real failure", context.Cause(ctx))
		}
		parent, stop := context.WithTimeout(t.Context(), 10*time.Second)
		defer stop()
		ctx, finish = Context(t, parent, "child", time.Minute)
		defer finish()
		time.Sleep(10 * time.Second)
		synctest.Wait()
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			t.Fatal("phase extended its parent's limit", ctx.Err())
		}
	})
}

func TestEndingOnePhaseDoesNotCancelServiceOrNextPhase(t *testing.T) {
	service, stop := context.WithCancel(t.Context())
	defer stop()
	setup, endSetup := Context(t, service, "ready", time.Minute)
	endSetup()
	endSetup()
	scenario, endScenario := Context(t, service, "scenario", time.Minute)
	defer endScenario()
	if !errors.Is(setup.Err(), context.Canceled) || service.Err() != nil || scenario.Err() != nil {
		t.Fatal("finished phase leaked cancellation into service lifetime", setup.Err(), service.Err(), scenario.Err())
	}
	stop()
	if !errors.Is(scenario.Err(), context.Canceled) {
		t.Fatal("service cancellation did not reach scenario", scenario.Err())
	}
}
