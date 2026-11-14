package upgradectl

import (
	"context"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
)

func TestActiveUpgradeReconcilesCommittedChangeBeforePeriodicTick(t *testing.T) {
	c, _ := testController(t)
	changed := app.NewBroadcast()
	subscribed := make(chan struct{}, 10)
	c.cfg.Changes = func() <-chan struct{} { subscribed <- struct{}{}; return changed.Wait() }
	observed := make(chan struct{}, 10)
	c.observe = func(context.Context) (app.ServerObservation, error) {
		observed <- struct{}{}
		return app.ServerObservation{}, nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- c.RunWith(ctx, nil) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-subscribed:
	case <-time.After(time.Second):
		t.Fatal("initial tick did not finish")
	}
	<-observed
	changed.Notify()
	select {
	case <-observed:
	case <-time.After(time.Second):
		t.Fatal("external change waited for hourly tick")
	}
	select {
	case <-subscribed:
	case <-time.After(time.Second):
		t.Fatal("not resubscribed")
	}
	select {
	case <-observed:
		t.Fatal("notification self-triggered another tick")
	case <-time.After(50 * time.Millisecond):
	}
}
