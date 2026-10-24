package runner

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/testkit"
)

func controlRunner(t *testing.T) *Runner {
	t.Helper()
	clock := testkit.NewClock()
	j, err := journal.Open(t.Context(), filepath.Join(t.TempDir(), "journal.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })
	return New(Config{Journal: j, InstanceID: "instance", Now: clock.Now})
}
func TestMaintenanceFixedGrantAndRestartConfirmation(t *testing.T) {
	r := controlRunner(t)
	ctx := context.Background()
	sent := r.cfg.Now()
	command := protocol.RunnerControl{Revision: 1, Mode: protocol.RunnerMaintenance, UpgradeID: "upgrade", Authorization: &protocol.MaintenanceAuthorization{ID: "grant", DeadlineMillis: sent.Add(300 * time.Second).UnixMilli(), RemainingMillis: 300000, RunIDs: []string{"run"}}}
	if err := r.applyControl(ctx, &command, sent, true); err != nil {
		t.Fatal(err)
	}
	first := r.authorizedDeadline("run", time.Time{})
	if !first.Equal(sent.Add(225 * time.Second)) {
		t.Fatalf("deadline %v", first)
	}
	if err := r.applyControl(ctx, &command, sent.Add(time.Minute), false); err != nil {
		t.Fatal(err)
	}
	if !r.authorizedDeadline("run", time.Time{}).Equal(first) {
		t.Fatal("repeated authorization extended deadline")
	}
	renewed := protocol.RunnerControl{Revision: 2, Mode: protocol.RunnerNormal}
	if err := r.applyControl(ctx, &renewed, sent, false); err != nil {
		t.Fatal(err)
	}
	if err := r.applyControl(ctx, &renewed, sent, false); err != nil {
		t.Fatal(err)
	}
	if !r.authorizedDeadline("run", time.Time{}).Equal(first) {
		t.Fatal("ordinary control revoked grant")
	}
	restarted := New(r.cfg)
	if err := restarted.loadControl(ctx); err != nil {
		t.Fatal(err)
	}
	if !restarted.authorizedDeadline("run", time.Time{}).IsZero() {
		t.Fatal("persisted grant active before hello")
	}
	if err := restarted.applyControl(ctx, &renewed, sent, false); err != nil {
		t.Fatal(err)
	}
	if !restarted.authorizedDeadline("run", time.Time{}).IsZero() {
		t.Fatal("heartbeat activated old authorization")
	}
}
func TestMaintenanceReadinessRetainsUnconfirmedWork(t *testing.T) {
	r := controlRunner(t)
	ctx := t.Context()
	command := protocol.RunnerControl{Revision: 1, Mode: protocol.RunnerMaintenance}
	if err := r.applyControl(ctx, &command, r.cfg.Now(), true); err != nil {
		t.Fatal(err)
	}
	r.resourceReconciled = true
	view, err := r.runtimeObservation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != protocol.RunnerReady {
		t.Fatalf("not ready: %+v", view)
	}
	env := protocol.Envelope{RequestID: "claim", Operation: protocol.OpRunnerClaim}
	if err := r.cfg.Journal.PutRequest(ctx, journal.Request{Envelope: env}); err != nil {
		t.Fatal(err)
	}
	view, err = r.runtimeObservation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode == protocol.RunnerReady || view.PendingClaims != 1 {
		t.Fatalf("false ready: %+v", view)
	}
	if err := r.cfg.Journal.DeleteRequest(ctx, "claim"); err != nil {
		t.Fatal(err)
	}
	if err := r.cfg.Journal.AcceptAssignment(ctx, protocol.Assignment{RunID: "held", Protocol: 0}, ""); err != nil {
		t.Fatal(err)
	}
	view, err = r.runtimeObservation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode == protocol.RunnerReady {
		t.Fatal("open run ready")
	}
}
func TestControlCannotReuseRevisionOrGrantIdentity(t *testing.T) {
	r := controlRunner(t)
	ctx := t.Context()
	a := protocol.RunnerControl{Revision: 1, Mode: protocol.RunnerMaintenance}
	if err := r.applyControl(ctx, &a, r.cfg.Now(), true); err != nil {
		t.Fatal(err)
	}
	a.Mode = protocol.RunnerNormal
	if err := r.applyControl(ctx, &a, r.cfg.Now(), false); err == nil {
		t.Fatal("revision reused")
	}
}

func TestIdleHeartbeatReportsRuntimeAndAppliesMaintenance(t *testing.T) {
	r := controlRunner(t)
	r.cfg.IDs = testkit.NewIDGen(testkit.NewClock(), 1)
	r.cfg.Log = slog.Default()
	r.c.log = r.cfg.Log
	var seen protocol.HeartbeatPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var env protocol.Envelope
		if err := json.NewDecoder(q.Body).Decode(&env); err != nil {
			t.Error(err)
			return
		}
		if env.Operation != protocol.OpRunnerHeartbeat {
			t.Errorf("operation %s", env.Operation)
		}
		if err := json.Unmarshal(env.Payload, &seen); err != nil {
			t.Error(err)
		}
		result, _ := json.Marshal(protocol.HeartbeatResult{Control: &protocol.RunnerControl{Revision: 1, Mode: protocol.RunnerMaintenance}})
		json.NewEncoder(w).Encode(rpcResponse{Operation: &protocol.Operation{Result: result}})
	}))
	defer server.Close()
	r.c.base = server.URL
	r.c.http = server.Client()
	r.heartbeat(t.Context())
	if seen.Runtime == nil || seen.Runtime.Sequence != 1 || seen.Runtime.InstanceID != "instance" || len(seen.Runs) != 0 {
		t.Fatalf("idle runtime missing: %+v", seen)
	}
	if !r.maintenance() {
		t.Fatal("idle control not applied")
	}
	persisted, err := r.cfg.Journal.Control(t.Context())
	if err != nil || persisted.Revision != 1 {
		t.Fatalf("not durable: %+v %v", persisted, err)
	}
}

func TestMaintenanceDoesNotMakeNewClaims(t *testing.T) {
	r := controlRunner(t)
	r.cfg.IDs = testkit.NewIDGen(testkit.NewClock(), 1)
	r.cfg.Log = slog.Default()
	r.cfg.RetryDelay = time.Millisecond
	command := protocol.RunnerControl{Revision: 1, Mode: protocol.RunnerMaintenance}
	if err := r.applyControl(t.Context(), &command, r.cfg.Now(), true); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); r.claims(ctx) }()
	time.Sleep(10 * time.Millisecond)
	cancel()
	<-done
	requests, err := r.cfg.Journal.Requests(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 0 {
		t.Fatalf("new claims during maintenance: %+v", requests)
	}
}

func TestMaintenanceAuthorizationConfiguredCeiling(t *testing.T) {
	for _, remaining := range []int64{900000, 900001} {
		r := controlRunner(t)
		command := protocol.RunnerControl{Revision: 1, Mode: protocol.RunnerMaintenance, Authorization: &protocol.MaintenanceAuthorization{ID: "grant", DeadlineMillis: r.cfg.Now().Add(15 * time.Minute).UnixMilli(), RemainingMillis: remaining}}
		err := r.applyControl(t.Context(), &command, r.cfg.Now(), true)
		if (err == nil) != (remaining <= 900000) {
			t.Fatalf("remaining %d: %v", remaining, err)
		}
	}
}

func TestMaintenanceAcknowledgesClockIndependentBoundAndKeepsFirstConfirmation(t *testing.T) {
	for _, offset := range []time.Duration{-24 * time.Hour, 24 * time.Hour} {
		t.Run(offset.String(), func(t *testing.T) {
			r := controlRunner(t)
			serverTime := time.Unix(200000, 0)
			sent := serverTime.Add(offset)
			now := sent.Add(1500*time.Millisecond + 500*time.Microsecond)
			r.cfg.Now = func() time.Time { return now }
			g := &protocol.MaintenanceAuthorization{ID: "fixed", DeadlineMillis: serverTime.Add(300001 * time.Millisecond).UnixMilli(), RemainingMillis: 300001, RunIDs: []string{"active"}}
			c := protocol.RunnerControl{Revision: 1, Mode: protocol.RunnerMaintenance, Authorization: g}
			if err := r.applyControl(t.Context(), &c, sent, true); err != nil {
				t.Fatal(err)
			}
			expectedBound := g.DeadlineMillis - 75001 - 1501
			view, err := r.runtimeObservation(t.Context())
			if err != nil || view.AuthorizationDeadlineMillis != expectedBound {
				t.Fatalf("offset bound %+v want %d err %v", view, expectedBound, err)
			}
			first := r.authorizedDeadline("active", time.Time{})
			if !first.Equal(sent.Add(225000 * time.Millisecond)) {
				t.Fatal("local conservative rounding", first)
			}
			// A duplicate delivered much later must neither revoke already issued
			// protection nor extend it, even with a smaller server remaining time.
			next := *g
			next.RemainingMillis = 100000
			c.Authorization = &next
			laterSent := now.Add(time.Second)
			now = laterSent.Add(90 * time.Second)
			if err := r.applyControl(t.Context(), &c, laterSent, false); err != nil {
				t.Fatal(err)
			}
			view, err = r.runtimeObservation(t.Context())
			if err != nil || view.AuthorizationDeadlineMillis != expectedBound || !r.authorizedDeadline("active", time.Time{}).Equal(first) {
				t.Fatal("duplicate changed confirmed protection", view, err)
			}
		})
	}
}

func TestMaintenanceLongResponseHasNoFalseRestartCoverage(t *testing.T) {
	r := controlRunner(t)
	sent := time.Unix(500000, 0)
	now := sent.Add(230 * time.Second)
	r.cfg.Now = func() time.Time { return now }
	g := &protocol.MaintenanceAuthorization{ID: "late", DeadlineMillis: 1000000, RemainingMillis: 300000, RunIDs: []string{"run"}}
	if err := r.applyControl(t.Context(), &protocol.RunnerControl{Revision: 1, Mode: protocol.RunnerMaintenance, Authorization: g}, sent, true); err != nil {
		t.Fatal(err)
	}
	view, err := r.runtimeObservation(t.Context())
	if err != nil || view.AuthorizationDeadlineMillis != 695000 {
		t.Fatalf("long RTT bound %+v %v", view, err)
	}
	if r.authorizedDeadline("run", time.Time{}).After(now) {
		t.Fatal("slow response created future local protection")
	}
}

func TestRunnerAdvertisesOriginalTaskInputSupport(t *testing.T) {
	r := controlRunner(t)
	view, err := r.runtimeObservation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(view.Capabilities, protocol.CapabilityTaskInputs) {
		t.Fatalf("task inputs capability missing: %+v", view.Capabilities)
	}
}
