package agentexec_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/agentexec"
	"github.com/GhostFlying/tend/internal/runner/gitws"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestBusyNativeProbePreservesCurrentAndRetainedObservations(t *testing.T) {
	f := newSource(t)
	f.config.FormatVersion = 2
	f.config.Runtimes[0].CLI = "traex"
	f.config.Agents[0].OwnerRuntimeID = "native"
	f.reload(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var blocked atomic.Bool
	p, err := agentexec.New(agentexec.Options{
		Source: f.control, BackendID: "git", Workspaces: gitws.New(gitws.Config{Home: t.TempDir()}),
		Probe: func(context.Context, agentexec.BuildInput) error { return nil },
		ProbeOwner: func(ctx context.Context, _ agentexec.BuildInput) error {
			if blocked.Load() {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		},
		Build: func(agentexec.BuildInput) (harness.Workers, error) { return &testkit.Workers{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	if err := p.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	before, err := p.Observe(t.Context())
	if err != nil || len(before) != 2 || !before[0].Available || !before[1].Available {
		t.Fatal(before, err)
	}
	p.SetRetained([]protocol.RunExecutionPin{before[0].Pin, before[1].Pin})
	if err := p.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	blocked.Store(true)
	done := make(chan error, 1)
	go func() { _, err := p.ResolveManager(t.Context(), before[1].Pin); done <- err }()
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(func() { unblock(); <-done })
	<-entered
	current, err := p.Observe(t.Context())
	if err != nil || len(current) != 2 || current[0] != before[0] || current[1] != before[1] {
		t.Fatalf("busy native probe erased accepted current observations: %+v, %v", current, err)
	}
	retained, err := p.ObserveRetained(t.Context())
	if err != nil || len(retained) != 2 || retained[0].Pin != before[0].Pin || retained[1].Pin != before[1].Pin || !retained[0].Available || !retained[1].Available {
		t.Fatalf("busy native probe erased original retained observations: %+v, %v", retained, err)
	}
	// A newly accepted definition cannot inherit readiness from the old pin.
	f.config.Agents[0].Revision++
	f.config.Agents[0].Instructions = "new definition while original probe is running"
	f.reload(t)
	newCurrent, err := p.Observe(t.Context())
	if err != nil || len(newCurrent) != 2 || newCurrent[0].Available || newCurrent[1].Available || newCurrent[0].Pin.SnapshotID == before[0].Pin.SnapshotID {
		t.Fatalf("new definition borrowed old readiness or disappeared: %+v, %v", newCurrent, err)
	}
}
