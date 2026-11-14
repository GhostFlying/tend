package upgradectl

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/updater"
)

func TestSelfUpdatePreparationOwnsBudgetAfterSlowObservation(t *testing.T) {
	c, f := testController(t)
	f.view.Phase = "server_updater"
	f.view.OverallDeadlineMillis = time.Now().Add(time.Minute).UnixMilli()
	f.batches = map[string]store.UpgradeRow{"batch": {View: f.view, Deadline: time.Now().Add(time.Minute).UnixMilli(), StartRequestID: "original", ReleaseSet: &release.Set{Format: 2}}}
	l, err := net.Listen("unix", c.cfg.UpdaterSocket)
	if err != nil {
		t.Fatal(err)
	}
	var posts atomic.Int64
	server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			_ = json.NewEncoder(w).Encode(updater.ControlResponse{State: updater.State{InstanceID: "server"}})
			return
		}
		if r.Method == http.MethodPost {
			posts.Add(1)
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(3 * time.Second):
		}
		_ = json.NewEncoder(w).Encode(updater.SelfUpdateStatus{Supported: true})
	})}
	go func() { _ = server.Serve(l) }()
	defer server.Close()
	if err := c.tick(t.Context()); err != nil {
		t.Fatal("finite preparation inherited time consumed by observation", err)
	}
	if posts.Load() != 1 || len(f.commands) != 1 {
		t.Fatal("original preparation was skipped or repeated", posts.Load(), f.commands)
	}
}

func TestUpgradeStepBudgetKeepsParentAndOriginalDeadlines(t *testing.T) {
	c, _ := testController(t)
	now := time.Now()
	c.cfg.Now = func() time.Time { return now }
	for _, tc := range []struct {
		name           string
		phase, overall int64
		parent         time.Duration
		want           time.Duration
	}{
		{"step", 0, now.Add(time.Minute).UnixMilli(), time.Minute, 5 * time.Second},
		{"phase", now.Add(time.Second).UnixMilli(), now.Add(time.Minute).UnixMilli(), time.Minute, time.Second},
		{"overall", now.Add(time.Minute).UnixMilli(), now.Add(time.Second).UnixMilli(), time.Minute, time.Second},
		{"parent", 0, now.Add(time.Minute).UnixMilli(), 50 * time.Millisecond, 50 * time.Millisecond},
		{"expired", now.Add(-time.Second).UnixMilli(), now.Add(time.Minute).UnixMilli(), time.Minute, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent, stop := context.WithTimeout(t.Context(), tc.parent)
			defer stop()
			row := store.UpgradeRow{Deadline: tc.phase}
			row.View.OverallDeadlineMillis = tc.overall
			ctx, cancel := c.upgradeStepContext(parent, row, 5*time.Second)
			defer cancel()
			if tc.want == 0 {
				if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
					t.Fatal("expired original budget still permits work", ctx.Err())
				}
				return
			}
			deadline, ok := ctx.Deadline()
			remaining := time.Until(deadline)
			if !ok || remaining > tc.want || remaining < tc.want-100*time.Millisecond {
				t.Fatal("original operation budget changed", remaining, tc.want)
			}
			stop()
			if !errors.Is(ctx.Err(), context.Canceled) && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
				t.Fatal("caller cancellation was lost", ctx.Err())
			}
		})
	}
}
