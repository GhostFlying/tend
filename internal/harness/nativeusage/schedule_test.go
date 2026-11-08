package nativeusage

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestIdleCollectorSleepsUntilLifecycleWake(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		wake := make(chan struct{}, 1)
		var calls atomic.Int64
		done := make(chan struct{})
		go func() {
			defer close(done)
			for Wait(ctx, wake, 0) {
				calls.Add(1)
			}
		}()
		time.Sleep(time.Minute)
		if calls.Load() != 0 {
			t.Fatal("idle collector woke without work")
		}
		wake <- struct{}{}
		synctest.Wait()
		if calls.Load() != 1 {
			t.Fatal("lifecycle wake was lost")
		}
		cancel()
		<-done
	})
}
func TestSourceRetriesDoNotDelayHealthyWork(t *testing.T) {
	r := Retries{}
	now := int64(1000)
	for _, want := range []time.Duration{2, 4, 8, 16, 30, 30} {
		r.Failed("bad", now)
		if r.Due("bad", now) || r.Delay(now, false) != want*time.Second || r.Delay(now, true) != 2*time.Second {
			t.Fatal(r)
		}
		if !r.Due("healthy", now) {
			t.Fatal("healthy source blocked")
		}
		now += int64(want) * 1000
	}
	r.Succeeded("bad")
	if r.Delay(now, false) != 0 {
		t.Fatal("settled retries did not sleep")
	}
}
