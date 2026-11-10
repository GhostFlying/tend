package agentexec_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/agentexec"
	"github.com/GhostFlying/tend/internal/runner/gitws"
	"github.com/GhostFlying/tend/internal/scheduler"
	"github.com/GhostFlying/tend/internal/testkit"
	"github.com/GhostFlying/tend/internal/views"
)

func TestHistoricalOwnerPurposeAllowsMatchingWorkerAndKeepsAdapterReadiness(t *testing.T) {
	for _, ready := range []bool{true, false} {
		t.Run(map[bool]string{true: "worker-ready", false: "worker-unavailable"}[ready], func(t *testing.T) {
			f := newSource(t)
			f.config.FormatVersion = 2
			f.config.CapabilityVersion = 1
			f.config.Runtimes[0].CLI = "codex"
			f.config.Runtimes[0].DeclaredPurposes = []agentconfig.Purpose{agentconfig.IssueManager}
			f.config.Agents[0].Purposes = []agentconfig.Purpose{agentconfig.IssueManager}
			required := []protocol.CapabilityReference{{CapabilityID: "image_gen", DefinitionRevision: 1}}
			f.config.Agents[0].Capabilities = required
			f.reload(t)
			p, err := agentexec.New(agentexec.Options{Source: f.control, BackendID: "git", Workspaces: gitws.New(gitws.Config{Home: t.TempDir()}), Probe: func(context.Context, agentexec.BuildInput) error {
				if !ready {
					return errors.New("worker adapter unavailable")
				}
				return nil
			}, ProbeOwner: func(context.Context, agentexec.BuildInput) error { return nil }, Build: func(agentexec.BuildInput) (harness.Workers, error) { return &testkit.Workers{}, nil }})
			if err != nil {
				t.Fatal(err)
			}
			if err = p.Refresh(t.Context()); (err == nil) != ready {
				t.Fatal("worker readiness was hidden", err)
			}
			observed, err := p.Observe(t.Context())
			if err != nil || len(observed) != 2 || observed[0].Available != ready || !observed[1].Available || observed[0].Pin.Purpose != "" || observed[1].Pin.Purpose != "issue_manager" || observed[0].Pin.Identity != observed[1].Pin.Identity {
				t.Fatal("role metadata overrode actual adapter availability", observed, err)
			}
			status, err := f.control.Status(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			executors := persistedExecutors(t, *status.Projection, status.SnapshotID, status.PublicSHA256, int64(status.Revision), observed)
			host := scheduler.Host{ID: "runner-a", Executors: executors}
			candidate := scheduler.Candidate{Kind: protocol.ActionImplement, WorkspaceBackendID: "git", AgentSelection: &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer", RequiredCapabilities: required}}}
			pin, reason := scheduler.SelectExecution(host, candidate)
			if !ready {
				if pin != nil || len(executors) != 1 || executors[0].Pin.Purpose != "issue_manager" {
					t.Fatal("owner transport was fabricated into an available worker", pin, reason, executors)
				}
				return
			}
			if pin == nil || reason != "" || pin.Purpose != "" || pin.Identity.LocalID != "worker" || pin.BackendID != "git" {
				t.Fatal("matching configured Agent could not execute because of its old purpose", pin, reason)
			}
			if _, err := p.Resolve(t.Context(), *pin); err != nil {
				t.Fatal("selected worker could not resolve its real adapter", err)
			}
			candidate.AgentSelection.Implementation.RequiredCapabilities = []protocol.CapabilityReference{{CapabilityID: "image_gen", DefinitionRevision: 2}}
			if pin, _ := scheduler.SelectExecution(host, candidate); pin != nil {
				t.Fatal("purpose cleanup bypassed capability revision")
			}
			candidate.AgentSelection.Implementation.RequiredCapabilities = required
			candidate.AgentSelection.Implementation.CategoryID = "other-category"
			if pin, _ := scheduler.SelectExecution(host, candidate); pin != nil {
				t.Fatal("purpose cleanup bypassed category")
			}
			candidate.AgentSelection.Implementation.CategoryID = "implementer"
			candidate.WorkspaceBackendID = "orca"
			if pin, _ := scheduler.SelectExecution(host, candidate); pin != nil {
				t.Fatal("purpose cleanup bypassed actual workspace backend")
			}
		})
	}
}

func persistedExecutors(t *testing.T, inventory protocol.AgentInventoryProjection, snapshot, hash string, revision int64, observed []protocol.AgentExecutor) []scheduler.Executor {
	t.Helper()
	// Exercise the real read projection and scheduler with a current immutable
	// inventory receipt, without requiring unrelated business ledger fixtures.
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.ExecContext(t.Context(), `
CREATE TABLE host_runtime(host_id TEXT,runtime_json TEXT,last_heartbeat_at INTEGER,online INTEGER);
CREATE TABLE agent_inventory_versions(host_id TEXT,server_revision INTEGER,receipt_json TEXT,projection_json TEXT);
CREATE TABLE extension_state(revision INTEGER,state_json TEXT);
CREATE TABLE agent_category_versions(category_id TEXT,revision INTEGER,title TEXT,description TEXT,state TEXT);
INSERT INTO agent_category_versions VALUES('implementer',1,'Implementation','','active');`)
	if err != nil {
		t.Fatal(err)
	}
	runtime, _ := json.Marshal(protocol.RunnerRuntime{Protocol: protocol.CurrentRunnerProtocol, JournalVersion: 23, Capabilities: []string{protocol.CapabilityAgentExecution, protocol.CapabilityExecutorDeclarations}, AgentExecutors: observed})
	projection, _ := json.Marshal(inventory)
	receipt, _ := json.Marshal(protocol.AgentInventoryReceipt{SnapshotID: snapshot, PublicSHA256: hash, LocalRevision: revision})
	if _, err = db.ExecContext(t.Context(), `INSERT INTO host_runtime VALUES(?,?,?,1)`, "runner-a", string(runtime), 1000); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(t.Context(), `INSERT INTO agent_inventory_versions VALUES(?,1,?,?)`, "runner-a", string(receipt), string(projection)); err != nil {
		t.Fatal(err)
	}
	executors, err := views.HostExecutors(t.Context(), db, "runner-a", 1000)
	if err != nil {
		t.Fatal(err)
	}
	return executors
}

func TestHostedTransportDoesNotFabricateWorkerAdapter(t *testing.T) {
	f := newSource(t)
	f.config.Runtimes[0].Kind = agentconfig.HostedManager
	f.config.Runtimes[0].DeclaredPurposes = []agentconfig.Purpose{agentconfig.Main}
	f.config.Agents[0].Purposes = []agentconfig.Purpose{agentconfig.Main}
	if _, err := agentconfig.Validate("runner-a", f.config, agentconfig.SourceReference{Kind: agentconfig.Inline}); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(f.config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.source, raw, 0600); err != nil {
		t.Fatal(err)
	}
	request := testkit.NewIDGen(testkit.NewClock(), 2026).New(protocol.PrefixRequest)
	if _, err := f.control.Reload(t.Context(), protocol.ReloadLocalAgentsRequest{RequestID: request}); err == nil {
		t.Fatal("hosted-only runtime was registered as a runner adapter")
	}
	builds := 0
	p, err := agentexec.New(agentexec.Options{Source: f.control, BackendID: "git", Workspaces: gitws.New(gitws.Config{Home: t.TempDir()}), Probe: func(context.Context, agentexec.BuildInput) error { return nil }, Build: func(agentexec.BuildInput) (harness.Workers, error) {
		builds++
		return &testkit.Workers{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	observed, err := p.Observe(t.Context())
	if err != nil || len(observed) != 0 || builds != 0 {
		t.Fatal("hosted transport acquired an unconfigured worker adapter", observed, builds, err)
	}
}
