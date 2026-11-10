package agentexec_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner"
	"github.com/GhostFlying/tend/internal/runner/agentexec"
	"github.com/GhostFlying/tend/internal/runner/gitws"
)

func observedManager(t *testing.T, p *agentexec.Pool) protocol.AgentExecutor {
	t.Helper()
	observations, err := p.Observe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var managers []protocol.AgentExecutor
	for _, observation := range observations {
		if observation.Pin.Purpose == "issue_manager" {
			managers = append(managers, observation)
		}
	}
	if len(managers) != 1 {
		t.Fatalf("expected one manager adapter: observations=%+v", observations)
	}
	return managers[0]
}

func TestManagerReceiptResolverUsesOriginalSnapshotWithoutBusinessProbe(t *testing.T) {
	f := newSource(t)
	f.config.FormatVersion = 2
	f.config.Runtimes[0].CLI = "codex"
	f.config.Runtimes[0].DeclaredPurposes = []agentconfig.Purpose{agentconfig.IssueManager}
	f.config.Agents[0].Purposes = []agentconfig.Purpose{agentconfig.IssueManager}
	f.reload(t)
	var probes atomic.Int64
	p, err := agentexec.New(agentexec.Options{Source: f.control, BackendID: "git", Workspaces: gitws.New(gitws.Config{Home: t.TempDir()}), ProbeOwner: func(context.Context, agentexec.BuildInput) error {
		probes.Add(1)
		return errors.New("business readiness is unavailable")
	}})
	if err != nil {
		t.Fatal(err)
	}
	original := observedManager(t, p).Pin
	if _, err := p.ResolveManager(t.Context(), original); err == nil || probes.Load() != 1 {
		t.Fatal("ordinary manager stopped checking business readiness", err, probes.Load())
	}
	reader, ok := any(p).(runner.ManagerReceiptExecutionResolver)
	if !ok {
		t.Fatal("original receipt resolution still requires the business probe")
	}
	f.config.Agents[0].Revision++
	f.config.Agents[0].Model = "changed-current-model"
	f.config.Agents[0].Instructions = "CHANGED_CURRENT_INSTRUCTIONS"
	f.reload(t)
	resolved, err := reader.ResolveManagerReceipt(t.Context(), original)
	if err != nil || resolved.Open == nil || resolved.Profile != original.Profile || resolved.Instructions != "PRIVATE_ROLE" || probes.Load() != 1 {
		t.Fatal("receipt resolver used current defaults or probed new business", resolved, probes.Load(), err)
	}
	for _, kind := range []string{"snapshot", "hash", "environment", "agent_revision", "runtime_revision", "model", "effort"} {
		t.Run(kind, func(t *testing.T) {
			pin := original
			switch kind {
			case "snapshot":
				pin.SnapshotID = "unaccepted-snapshot"
			case "hash":
				pin.PublicSHA256 = "different-original-hash"
			case "environment":
				pin.Identity.ExecutionEnvironmentID = "another-environment"
			case "agent_revision":
				pin.AgentRevision++
			case "runtime_revision":
				pin.Runtime.Revision++
			case "model":
				pin.Profile.Model = "different-model"
			case "effort":
				pin.Profile.Effort = "high"
			}
			if _, err := reader.ResolveManagerReceipt(t.Context(), pin); err == nil {
				t.Fatal("receipt resolver accepted a different original pin", kind, pin)
			}
			if probes.Load() != 1 {
				t.Fatal("invalid receipt identity reached a business probe", probes.Load())
			}
		})
	}
}
