//go:build linux

package updater

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

type boundedCancelRetainSource struct{ *fakeProcess }

func (*boundedCancelRetainSource) Stop(context.Context, Identity, time.Duration) error {
	return ErrUnknown
}

func boundedCancelCommandFixture(t *testing.T, action string) (*harness, protocol.BoundedRunnerMaintenanceGrant) {
	t.Helper()
	h, grant := boundedRunnerFixture(t)
	h.a.deny = true
	process := &boundedCancelRetainSource{h.p}
	h.opts.Process, h.s.opts.Process = process, process
	if err := h.s.AuthorizeBoundedRunnerMaintenance(t.Context(), grant, action); err != nil {
		t.Fatal(err)
	}
	// Record the first unsuccessful observation before Serve starts its ticker.
	// Subsequent observations cannot change the fresh cancellation CAS.
	if err := h.s.Step(t.Context()); err == nil {
		t.Fatal("original runner unexpectedly drained or stopped")
	}
	return h, grant
}

func boundedCancelServe(t *testing.T, h *harness) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	finished := make(chan error, 1)
	go func() { finished <- h.s.Serve(ctx) }()
	stop := sync.OnceFunc(func() {
		cancel()
		select {
		case err := <-finished:
			if err != nil {
				t.Error("supervisor control server failed", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("supervisor control server did not stop")
		}
	})
	t.Cleanup(stop)
	for range 100 {
		readCtx, stopRead := context.WithTimeout(t.Context(), 25*time.Millisecond)
		_, err := Call(readCtx, h.cfg, "status", ControlRequest{})
		stopRead()
		if err == nil {
			return stop
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("live supervisor control socket did not become ready")
	return stop
}

func boundedCancelOwnerGrant(t *testing.T, h *harness, original protocol.BoundedRunnerMaintenanceGrant, request string, hostRevision int64) protocol.BoundedRunnerMaintenanceGrant {
	t.Helper()
	grant := original
	grant.Plan.ExpectedControlRevision = hostRevision
	grant.ControlRevision = hostRevision + 1
	auth := &grant.Plan.Authorization
	auth.RequestID = request
	auth.ExpectedGeneration = h.s.Status().Generation
	auth.ExpectedPhase = h.s.Status().Bounded.Phase
	auth.Reason = "Owner cancels this window before the original runner stops"
	auth.ExpiresAtMillis = h.now.Add(time.Hour).UnixMilli()
	sha, err := protocol.RunnerBoundedMaintenancePlanSHA256(grant.Plan)
	if err != nil || sha != original.Plan.Authorization.PlanSHA256 {
		t.Fatal("fresh owner CAS changed the frozen runner source plan", sha, err)
	}
	auth.PlanSHA256 = sha
	return grant
}

func boundedCancelCommand(t *testing.T, h *harness, grant protocol.BoundedRunnerMaintenanceGrant) (any, error) {
	t.Helper()
	dir := t.TempDir()
	planFile, authorizationFile := filepath.Join(dir, "grant.json"), filepath.Join(dir, "authorization.json")
	if err := writeJSON(planFile, grant); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(authorizationFile, grant.Plan.Authorization); err != nil {
		t.Fatal(err)
	}
	auth := grant.Plan.Authorization
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	return BoundedCommand(ctx, h.cfg, "cancel", planFile, authorizationFile, auth.RequestID, auth.ExpectedGeneration, auth.ExpectedPhase)
}

func TestBoundedCancelCommand(t *testing.T) {
	h, original := boundedCancelCommandFixture(t, "apply")
	stop := boundedCancelServe(t, h)
	before := h.s.Status()
	journal, err := os.ReadFile(h.data)
	if err != nil {
		t.Fatal(err)
	}
	incarnation, err := os.ReadFile(h.data + ".incarnation")
	if err != nil {
		t.Fatal(err)
	}
	stale := boundedCancelOwnerGrant(t, h, original, "req_00000000000000000000000003", original.ControlRevision)
	stale.Plan.Authorization.ExpectedGeneration--
	if _, err := boundedCancelCommand(t, h, stale); err == nil {
		t.Fatal("stale cancellation CAS was accepted")
	}
	if after := h.s.Status(); !reflect.DeepEqual(after, before) {
		t.Fatal("rejected stale cancellation changed the original window")
	}
	// The owner granted another host-control revision after the stale local
	// attempt. This is a new independent request, not a modified retry.
	grant := boundedCancelOwnerGrant(t, h, original, "req_00000000000000000000000002", stale.ControlRevision)
	if _, err := boundedCancelCommand(t, h, grant); err != nil {
		t.Fatal("valid independent cancellation could not reach the runner backend", err)
	}
	cancelled := h.s.Status()
	if cancelled.Operation.Phase != "cancelled" || cancelled.Bounded.Outcome != "cancelled" || cancelled.Bounded.SourceExit != nil || cancelled.Process != before.Process || h.p.starts != 0 || h.p.stops != 0 {
		t.Fatal("cancellation did not retain the original running writer", cancelled.Operation, cancelled.Bounded, h.p.starts, h.p.stops)
	}
	if got, ok := cancelled.Bounded.Authorizations[grant.Plan.Authorization.RequestID]; !ok || !reflect.DeepEqual(got, grant.Plan.Authorization) {
		t.Fatal("independent cancellation authority was not durably recorded", got)
	}
	if got, ok := cancelled.BoundedRunnerGrants[grant.Plan.Authorization.RequestID]; !ok || !reflect.DeepEqual(got, grant) {
		t.Fatal("cancellation lost its exact owner grant or host CAS", got)
	}
	if len(cancelled.Bounded.Audit) != len(before.Bounded.Audit)+1 {
		t.Fatal("cancellation did not add exactly one independent audit")
	}
	audit := cancelled.Bounded.Audit[len(cancelled.Bounded.Audit)-1]
	if audit.RequestID != grant.Plan.Authorization.RequestID || audit.Action != "cancel" || audit.Outcome != "cancelled_before_stop" || !reflect.DeepEqual(cancelled.Bounded.Audit[:len(before.Bounded.Audit)], before.Bounded.Audit) {
		t.Fatal("cancellation changed previous unknown-work audit or fabricated success", audit)
	}
	persisted, err := readState(filepath.Join(h.cfg.StateDir, "state.json"))
	if err != nil || !reflect.DeepEqual(persisted, cancelled) {
		t.Fatal("cancelled outcome was not durable", err)
	}
	if _, err := h.p.Inspect(t.Context(), original.Plan.Source.PID); err != nil {
		t.Fatal("original process stopped during cancellation", err)
	}
	for _, path := range []string{h.data, h.data + ".incarnation"} {
		actual, err := os.ReadFile(path)
		want := journal
		if path != h.data {
			want = incarnation
		}
		if err != nil || !reflect.DeepEqual(actual, want) {
			t.Fatal("cancellation changed original journal or incarnation", path, err)
		}
	}
	if _, err := boundedCancelCommand(t, h, grant); err != nil || !reflect.DeepEqual(h.s.Status(), cancelled) {
		t.Fatal("same cancellation request changed audit, generation, deadlines or effects", err)
	}
	stop()
	h.reopen(t)
	boundedCancelServe(t, h)
	reopened := h.s.Status()
	if _, err := boundedCancelCommand(t, h, grant); err != nil || !reflect.DeepEqual(h.s.Status(), reopened) || h.p.starts != 0 || h.p.stops != 0 {
		t.Fatal("cancellation replay after restart changed its original authority", err)
	}
}

func TestBoundedCancelCommandRejectsReuse(t *testing.T) {
	for _, action := range []string{"apply", "force"} {
		t.Run(action, func(t *testing.T) {
			h, grant := boundedCancelCommandFixture(t, action)
			boundedCancelServe(t, h)
			before := h.s.Status()
			if _, err := boundedCancelCommand(t, h, grant); err == nil {
				t.Fatal("cancel falsely succeeded through another action's deduplication", action)
			}
			if after := h.s.Status(); !reflect.DeepEqual(after, before) || after.Bounded.Outcome == "cancelled" || h.p.starts != 0 || h.p.stops != 0 {
				t.Fatal("rejected action identity changed original process or authority", action)
			}
			if _, err := h.p.Inspect(t.Context(), grant.Plan.Source.PID); errors.Is(err, ErrNotRunning) {
				t.Fatal("original source disappeared after rejected cancellation")
			}
		})
	}
}
