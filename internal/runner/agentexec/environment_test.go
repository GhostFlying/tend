//go:build linux

package agentexec_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/codexenv"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/agentexec"
	"github.com/GhostFlying/tend/internal/runner/gitws"
	"github.com/GhostFlying/tend/internal/testkit"
)

func mappedEnvironment(t *testing.T) (*codexenv.Service, codexenv.Profile, *codexenv.Bindings) {
	t.Helper()
	root := t.TempDir()
	s := &codexenv.Service{Root: filepath.Join(root, "store"), Validate: func(context.Context, codexenv.Profile, string) error { return nil }}
	p := codexenv.Profile{ID: "worker", Home: filepath.Join(root, "home"), UserHome: t.TempDir(), Enabled: true}
	if _, err := s.Execute(t.Context(), codexenv.Request{Action: "create", ID: p.ID, RequestID: "create", Profile: &p}); err != nil {
		t.Fatal(err)
	}
	return s, p, &codexenv.Bindings{Directory: s.Root, Runtimes: map[string]string{"native": p.ID}}
}

func mappedPool(t *testing.T, f *sourceFixture, bindings *codexenv.Bindings) *agentexec.Pool {
	t.Helper()
	probe := func(context.Context, agentexec.BuildInput) error { return nil }
	p, err := agentexec.New(agentexec.Options{Source: f.control, BackendID: "git", Workspaces: gitws.New(gitws.Config{Home: t.TempDir()}), Environments: bindings, Probe: probe, ProbeOwner: probe,
		Build: func(agentexec.BuildInput) (harness.Workers, error) { return &testkit.Workers{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRetainedMappedSnapshotReestablishesContinuationReadiness(t *testing.T) {
	_, _, bindings := mappedEnvironment(t)
	f := newSource(t)
	f.config.FormatVersion = 2
	f.config.Runtimes[0].CLI = "codex"
	f.reload(t)
	p := mappedPool(t, f, bindings)
	if err := p.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	observations, err := p.Observe(t.Context())
	if err != nil || len(observations) != 2 || !observations[0].Available {
		t.Fatal(observations, err)
	}
	original := observations[0].Pin
	f.config.Agents = f.config.Agents[:0]
	f.reload(t)
	cold := mappedPool(t, f, bindings)
	cold.SetRetained([]protocol.RunExecutionPin{original})
	if err = cold.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	retained, err := cold.ObserveRetained(t.Context())
	if err != nil || len(retained) != 1 || !retained[0].Available || retained[0].Pin != original {
		t.Fatal("healthy retained inventory remained unready after restart", retained, err)
	}
}

func TestEnvironmentRecoveryCannotEraseTerminalExecutorReason(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "native_refusal", true: "definition_disabled"}[disabled], func(t *testing.T) {
			s, profile, bindings := mappedEnvironment(t)
			f := newSource(t)
			f.config.FormatVersion = 2
			f.config.Runtimes[0].CLI = "codex"
			f.config.Agents[0].Enabled = !disabled
			f.reload(t)
			p := mappedPool(t, f, bindings)
			if err := p.Refresh(t.Context()); err != nil {
				t.Fatal(err)
			}
			rows, _ := p.Observe(t.Context())
			expected := "definition_disabled"
			if !disabled {
				p.Reject(rows[0].Pin, "adapter_rejected_configuration")
				expected = "adapter_rejected_configuration"
			}
			for _, enabled := range []bool{false, true} {
				profile.Enabled = enabled
				status, _ := s.Read(profile.ID)
				if _, err := s.Execute(t.Context(), codexenv.Request{Action: "update", ID: profile.ID, RequestID: map[bool]string{false: "disable", true: "enable"}[enabled], ExpectedRevision: status.Revision, Profile: &profile}); err != nil {
					t.Fatal(err)
				}
				_ = p.Refresh(t.Context())
				rows, _ = p.Observe(t.Context())
				if rows[0].Available || rows[0].Reason != expected {
					t.Fatal("environment readiness erased a terminal restriction", rows)
				}
			}
		})
	}
}

func TestRetainedReadinessDoesNotProbeReplacementMapping(t *testing.T) {
	_, _, bindings := mappedEnvironment(t)
	f := newSource(t)
	f.config.FormatVersion = 2
	f.config.Runtimes[0].CLI = "codex"
	f.reload(t)
	originalPool := mappedPool(t, f, bindings)
	if err := originalPool.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	original, _ := originalPool.Observe(t.Context())
	pins := []protocol.RunExecutionPin{original[0].Pin, original[1].Pin}
	f.config.Agents = f.config.Agents[:0]
	f.reload(t)
	replacement := &codexenv.Bindings{Directory: t.TempDir(), Runtimes: map[string]string{"native": "unavailable-replacement"}}
	cold := mappedPool(t, f, replacement)
	cold.SetRetained(pins)
	if err := cold.Refresh(t.Context()); err != nil {
		t.Fatal("replacement environment blocked original recovery", err)
	}
	rows, err := cold.ObserveRetained(t.Context())
	if err != nil || len(rows) != 2 || !rows[0].Available || !rows[1].Available {
		t.Fatal(rows, err)
	}
	cold.Reject(pins[0], "adapter_rejected_configuration")
	rows, err = cold.ObserveRetained(t.Context())
	if err != nil || rows[0].Available || rows[0].Reason != "adapter_rejected_configuration" {
		t.Fatal("terminal refusal bypassed", rows, err)
	}
}
