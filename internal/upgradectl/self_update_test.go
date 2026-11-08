package upgradectl

import (
	"context"
	"encoding/json"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/updater"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

type selfDeadlineBackend struct {
	*fakeBackend
	ticks int
}

func (f *selfDeadlineBackend) TickUpgrade(ctx context.Context, o app.ServerObservation) (protocol.UpgradeView, error) {
	f.ticks++
	return f.fakeBackend.TickUpgrade(ctx, o)
}
func TestSelfUpdatePendingHandoffStillReconcilesBatchDeadline(t *testing.T) {
	c, f := testController(t)
	f.view.Phase = "server_updater"
	f.view.OverallDeadlineMillis = time.Now().Add(-time.Second).UnixMilli()
	f.batches = map[string]store.UpgradeRow{"batch": {View: f.view, ReleaseSet: &release.Set{Format: 2}}}
	b := &selfDeadlineBackend{fakeBackend: f}
	c.app = b
	l, e := net.Listen("unix", c.cfg.UpdaterSocket)
	if e != nil {
		t.Fatal(e)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			json.NewEncoder(w).Encode(updater.ControlResponse{State: updater.State{InstanceID: "server"}})
		} else {
			json.NewEncoder(w).Encode(updater.SelfUpdateStatus{Supported: true})
		}
	}), ReadHeaderTimeout: time.Second}
	go server.Serve(l)
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	err := c.tick(ctx)
	t.Logf("tick result: %v", err)
	if b.ticks == 0 {
		t.Fatal("original batch deadline never reconciled while waiting for missing handoff receipt")
	}
}

func TestSelfUpdateDoesNotDispatchAfterPhaseDeadline(t *testing.T) {
	c, f := testController(t)
	f.view.Phase = "server_updater"
	f.view.OverallDeadlineMillis = time.Now().Add(time.Hour).UnixMilli()
	f.batches = map[string]store.UpgradeRow{"batch": {View: f.view, Deadline: time.Now().Add(-time.Second).UnixMilli(), ReleaseSet: &release.Set{Format: 2}}}
	b := &selfDeadlineBackend{fakeBackend: f}
	c.app = b
	l, err := net.Listen("unix", c.cfg.UpdaterSocket)
	if err != nil {
		t.Fatal(err)
	}
	var dispatches atomic.Int64
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			dispatches.Add(1)
		}
		if r.URL.Path == "/status" {
			_ = json.NewEncoder(w).Encode(updater.ControlResponse{State: updater.State{InstanceID: "server"}})
		} else {
			_ = json.NewEncoder(w).Encode(updater.SelfUpdateStatus{Supported: true})
		}
	}), ReadHeaderTimeout: time.Second}
	go func() { _ = server.Serve(l) }()
	defer server.Close()
	if err := c.tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if b.ticks != 1 || dispatches.Load() != 0 || len(f.commands) != 0 {
		t.Fatal("expired phase dispatched before reconciliation", b.ticks, dispatches.Load(), f.commands)
	}
}
