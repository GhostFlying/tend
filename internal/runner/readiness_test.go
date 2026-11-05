package runner

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

type maintenanceReadinessExecutions struct {
	calls   atomic.Int64
	entered chan struct{}
	release <-chan struct{}
}

func (e *maintenanceReadinessExecutions) Refresh(ctx context.Context) error {
	e.calls.Add(1)
	if e.entered != nil {
		select {
		case e.entered <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if e.release != nil {
		select {
		case <-e.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (*maintenanceReadinessExecutions) Observe(context.Context) ([]protocol.AgentExecutor, error) {
	return nil, nil
}

func (*maintenanceReadinessExecutions) Resolve(context.Context, protocol.RunExecutionPin) (ResolvedExecution, error) {
	panic("unexpected execution")
}

func TestMaintenanceRejectsBackgroundReadinessAndNormalReopensIt(t *testing.T) {
	r := controlRunner(t)
	e := &maintenanceReadinessExecutions{}
	r.cfg.Executions = e
	maintenance := protocol.RunnerControl{Revision: 1, Mode: protocol.RunnerMaintenance}
	if err := r.applyControl(t.Context(), &maintenance, r.cfg.Now(), true); err != nil {
		t.Fatal(err)
	}
	if err := r.refreshExecutionReadiness(t.Context()); err != nil || e.calls.Load() != 0 {
		t.Fatal("maintenance admitted background probe", err, e.calls.Load())
	}
	normal := protocol.RunnerControl{Revision: 2, Mode: protocol.RunnerNormal}
	if err := r.applyControl(t.Context(), &normal, r.cfg.Now(), false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.reconcile:
	default:
		t.Fatal("normal control did not wake the existing reconciler")
	}
	if err := r.refreshExecutionReadiness(t.Context()); err != nil || e.calls.Load() != 1 {
		t.Fatal("normal control did not admit background probe", err, e.calls.Load())
	}
}

func TestMaintenanceReadinessWaitsForAdmittedProbeCleanup(t *testing.T) {
	r := controlRunner(t)
	release := make(chan struct{})
	e := &maintenanceReadinessExecutions{entered: make(chan struct{}), release: release}
	r.cfg.Executions = e
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		done <- r.refreshExecutionReadiness(ctx)
	}()
	t.Cleanup(func() { cancel(); <-finished })
	select {
	case <-e.entered:
	case <-ctx.Done():
		t.Fatal("background probe did not enter")
	}
	maintenance := protocol.RunnerControl{Revision: 1, Mode: protocol.RunnerMaintenance}
	if err := r.applyControl(t.Context(), &maintenance, r.cfg.Now(), true); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.resourceReconciled = true
	r.mu.Unlock()
	view, err := r.runtimeObservation(t.Context())
	if err != nil || view.AppliedControlRevision != 1 || view.Mode != protocol.RunnerMaintenance || !slices.Contains(view.Blockers, "readiness_refresh_active") {
		t.Fatal("in-flight probe escaped the current maintenance observation", view, err)
	}
	if err := r.refreshExecutionReadiness(t.Context()); err != nil || e.calls.Load() != 1 {
		t.Fatal("maintenance admitted another probe", err, e.calls.Load())
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	view, err = r.runtimeObservation(t.Context())
	if err != nil || view.Mode != protocol.RunnerReady || view.AppliedControlRevision != 1 || slices.Contains(view.Blockers, "readiness_refresh_active") {
		t.Fatal("probe cleanup did not release the current maintenance blocker", view, err)
	}
}

func TestBootstrapMaintenanceAndCancelledContextDoNotAdmitReadiness(t *testing.T) {
	r := controlRunner(t)
	e := &maintenanceReadinessExecutions{}
	r.cfg.Executions = e
	r.bootstrapMaintenance = true
	if err := r.refreshExecutionReadiness(t.Context()); err != nil || e.calls.Load() != 0 {
		t.Fatal("bootstrap maintenance admitted readiness", err, e.calls.Load())
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := r.refreshExecutionReadiness(ctx); !errors.Is(err, context.Canceled) || e.calls.Load() != 0 {
		t.Fatal("cancelled readiness created work", err, e.calls.Load())
	}
}
