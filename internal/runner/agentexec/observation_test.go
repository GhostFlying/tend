package agentexec_test

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/agentexec"
	"github.com/GhostFlying/tend/internal/runner/gitws"
	"github.com/GhostFlying/tend/internal/testkit"
)

type observationFixture struct {
	source  *sourceFixture
	pool    *agentexec.Pool
	block   atomic.Bool
	entered chan struct{}
	release chan error
	cancel  context.CancelFunc
}

func newObservationFixture(t *testing.T) *observationFixture {
	t.Helper()
	f := &observationFixture{source: newSource(t), entered: make(chan struct{}), release: make(chan error, 1)}
	f.source.config.FormatVersion = 2
	f.source.config.Runtimes[0].CLI = "codex"
	f.source.config.Runtimes[0].DeclaredPurposes = []agentconfig.Purpose{agentconfig.WorkerRun, agentconfig.IssueManager}
	f.source.config.Agents[0].Purposes = []agentconfig.Purpose{agentconfig.WorkerRun, agentconfig.IssueManager}
	f.source.reload(t)
	var err error
	f.pool, err = agentexec.New(agentexec.Options{
		Source: f.source.control, BackendID: "git", Workspaces: gitws.New(gitws.Config{Home: t.TempDir()}),
		Probe: func(context.Context, agentexec.BuildInput) error { return nil },
		ProbeOwner: func(ctx context.Context, _ agentexec.BuildInput) error {
			if !f.block.Swap(false) {
				return nil
			}
			close(f.entered)
			select {
			case err := <-f.release:
				return err
			case <-ctx.Done():
				return ctx.Err()
			}
		},
		Build: func(agentexec.BuildInput) (harness.Workers, error) { return &testkit.Workers{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.pool.Close)
	if err := f.pool.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *observationFixture) retainCurrent(t *testing.T) []protocol.AgentExecutor {
	t.Helper()
	current, err := f.pool.Observe(t.Context())
	if err != nil || len(current) != 2 || !current[0].Available || !current[1].Available {
		t.Fatal(current, err)
	}
	f.pool.SetRetained([]protocol.RunExecutionPin{current[0].Pin, current[1].Pin})
	if err := f.pool.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	return current
}

func (f *observationFixture) startRefresh(t *testing.T) <-chan error {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	f.cancel = cancel
	done := make(chan error, 1)
	f.block.Store(true)
	go func() { done <- f.pool.Refresh(ctx); close(done) }()
	// Cleanup cancels the fake external IO and joins the owner goroutine even if
	// an observation assertion fails while the probe is deliberately blocked.
	t.Cleanup(func() { cancel(); <-done })
	select {
	case <-f.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	return done
}

func TestObservationPreservesInventoryDuringRetainedProbe(t *testing.T) {
	for _, failure := range []bool{false, true} {
		name := "success"
		if failure {
			name = "failure"
		}
		t.Run(name, func(t *testing.T) {
			f := newObservationFixture(t)
			original := f.retainCurrent(t)
			done := f.startRefresh(t)
			current, err := f.pool.Observe(t.Context())
			if err != nil || !slices.Equal(current, original) {
				t.Errorf("in-flight probe erased current inventory: %+v, %v", current, err)
			}
			retained, err := f.pool.ObserveRetained(t.Context())
			if err != nil || !slices.Equal(retained, original) {
				t.Errorf("in-flight probe erased original continuations: %+v, %v", retained, err)
			}
			var probeErr error
			if failure {
				probeErr = errors.New("native unavailable")
			}
			f.release <- probeErr
			if err := <-done; (err != nil) != failure {
				t.Fatal("refresh outcome differs", err)
			}
			retained, err = f.pool.ObserveRetained(t.Context())
			if err != nil || len(retained) != 2 || retained[0] != original[0] || retained[1].Pin != original[1].Pin || retained[1].Available == failure {
				t.Fatal("finished probe did not publish its original-pin outcome", retained, err)
			}
		})
	}
}

func TestObservationDoesNotReuseAvailableSnapshotAfterConfigurationChange(t *testing.T) {
	for _, change := range []string{"private", "disabled", "empty"} {
		t.Run(change, func(t *testing.T) {
			f := newObservationFixture(t)
			original := f.retainCurrent(t)
			done := f.startRefresh(t)
			switch change {
			case "private":
				f.source.config.Agents[0].Instructions = "CHANGED_PRIVATE_ROLE"
			case "disabled":
				f.source.config.Agents[0].Enabled = false
			case "empty":
				f.source.config.Agents = []agentconfig.AgentDefinition{}
			}
			f.source.reload(t)
			current, err := f.pool.Observe(t.Context())
			want := map[string]int{"private": 2, "disabled": 1, "empty": 0}[change]
			if err != nil || len(current) != want {
				t.Fatal("current definitions disappeared during unrelated original probe", current, err)
			}
			for _, e := range current {
				if e.Pin.SnapshotID == original[0].Pin.SnapshotID || e.Available {
					t.Fatal("new configuration inherited stale readiness", e)
				}
			}
			f.release <- nil
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			current, err = f.pool.Observe(t.Context())
			if err != nil || len(current) != want {
				t.Fatal(current, err)
			}
			for _, e := range current {
				if e.Available != (change == "private") {
					t.Fatal("completed refresh did not apply current configuration", e)
				}
			}
			retained, err := f.pool.ObserveRetained(t.Context())
			if err != nil || !slices.Equal(retained, original) {
				t.Fatal("current configuration replaced original retained pins", retained, err)
			}
		})
	}
}

func TestObservationProbeCancellationJoinsWithoutErasingInventory(t *testing.T) {
	f := newObservationFixture(t)
	original := f.retainCurrent(t)
	done := f.startRefresh(t)
	f.cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled probe did not exit", err)
	}
	current, err := f.pool.Observe(t.Context())
	if err != nil || !slices.Equal(current, original) {
		t.Fatal("cancelled probe erased the existing current state", current, err)
	}
	retained, err := f.pool.ObserveRetained(t.Context())
	if err != nil || !slices.Equal(retained, original) {
		t.Fatal("cancelled probe erased original continuation facts", retained, err)
	}
}

func TestRetainedWithdrawalDuringProbeCannotRepublishOldPins(t *testing.T) {
	f := newObservationFixture(t)
	f.retainCurrent(t)
	done := f.startRefresh(t)
	f.pool.SetRetained(nil)
	retained, err := f.pool.ObserveRetained(t.Context())
	if err != nil || len(retained) != 0 {
		t.Fatal("withdrawn retained pins remained observable", retained, err)
	}
	f.release <- nil
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	retained, err = f.pool.ObserveRetained(t.Context())
	if err != nil || len(retained) != 0 {
		t.Fatal("completed old probe republished withdrawn pins", retained, err)
	}
}
