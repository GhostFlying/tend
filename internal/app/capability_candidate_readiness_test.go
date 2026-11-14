package app

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestCapabilityDirectorySeparatesDeclarationsFromReadiness(t *testing.T) {
	for _, observation := range []string{"ready", "adapter_unavailable", "heartbeat_expired", "executor_unobserved"} {
		t.Run(observation, func(t *testing.T) {
			h, runtime, _ := capabilitySetup(t)
			originalExecutors := append([]protocol.AgentExecutor(nil), runtime.AgentExecutors...)
			wantReadiness, wantCurrent := "ready", "eligible"
			switch observation {
			case "adapter_unavailable":
				for i := range runtime.AgentExecutors {
					runtime.AgentExecutors[i].Available = false
				}
				wantReadiness, wantCurrent = "unavailable", "pending"
			case "executor_unobserved":
				runtime.AgentExecutors = nil
				wantReadiness, wantCurrent = "unknown", "pending"
			case "heartbeat_expired":
				wantReadiness, wantCurrent = "unavailable", "pending"
			}
			runtime.Sequence++
			if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if observation == "heartbeat_expired" {
				h.clock.Advance(time.Minute)
			}
			page, err := h.app.CapabilityAgents(t.Context(), protocol.AgentCapabilityQuery{})
			if err != nil || len(page.Items) != 2 {
				t.Fatal(page, err)
			}
			for i, candidate := range page.Items {
				wantDeclaration := "declared"
				if i == 1 {
					wantDeclaration = "empty"
				}
				if candidate.DeclarationState != wantDeclaration || candidate.ExecutorReadiness.State != wantReadiness || candidate.Current.State != wantCurrent || len(candidate.Current.Required) != 0 || len(candidate.Current.Reasons) != 0 {
					t.Fatalf("declarations treated as task requirements: %+v", candidate)
				}
				if candidate.ExecutorReadiness.Reason == "" || candidate.ExecutorReadiness.Explanation == "" || candidate.ExecutorReadiness.ObservedAtMillis == 0 || candidate.ExecutorReadiness.CheckedAtMillis != h.clock.Now().UnixMilli() {
					t.Fatal(candidate.ExecutorReadiness)
				}
			}
			filtered, err := h.app.CapabilityAgents(t.Context(), protocol.AgentCapabilityQuery{Category: "reviewer", State: wantCurrent, Limit: 1})
			if err != nil || len(filtered.Items) != 1 || filtered.Items[0].DeclarationState != "empty" {
				t.Fatal(filtered, err)
			}
			// The same accepted inventory recovers via an ordinary authenticated Hello;
			// no declarations, configuration edits, or tool probes are needed.
			if observation != "ready" {
				runtime.AgentExecutors = originalExecutors
				for i := range runtime.AgentExecutors {
					runtime.AgentExecutors[i].Available = true
				}
				runtime.Sequence++
				if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				recovered, err := h.app.CapabilityAgents(t.Context(), protocol.AgentCapabilityQuery{State: "eligible"})
				if err != nil || len(recovered.Items) != 2 {
					t.Fatal(recovered, err)
				}
			}
		})
	}
}

func TestCapabilityEmptyRequirementsKeepOrdinaryAdmission(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "selected", true: "legacy"}[legacy], func(t *testing.T) {
			h, runtime, _ := capabilitySetup(t)
			spec := testSpec
			if !legacy {
				// beta declares no capabilities. Empty requirements must allow it to work.
				spec.AgentSelection = &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "reviewer", AgentID: "linux-1/beta"}, Acceptance: protocol.AgentRequirement{CategoryID: "implementer"}}
			}
			_, task := h.createTask(owner, createTaskPayload{Title: "ordinary work", Spec: spec})
			config := testConfig()
			config.Hosts[0].Slots = 0
			if op := h.syncConfig(config); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			explanation, err := h.app.TaskCapabilityExplanation(t.Context(), task.TaskID, 0, protocol.RoleImplement)
			if err != nil || explanation.State == "eligible" || len(explanation.Required) != 0 {
				t.Fatal(explanation, err)
			}
			claim := func() protocol.Assignment {
				t.Helper()
				op := h.exec(runner("linux-1"), h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{WorkspaceProtocol: 1, Runtime: runtime}, nil), Claim{})
				var result claimResult
				if op.Status != protocol.OpSucceeded || json.Unmarshal(op.Result, &result) != nil {
					t.Fatal(op)
				}
				return result.Assignment
			}
			if as := claim(); as.RunID != "" {
				t.Fatal("capacity bypassed", as)
			}
			if op := h.syncConfig(testConfig()); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			explanation, err = h.app.TaskCapabilityExplanation(t.Context(), task.TaskID, 0, protocol.RoleImplement)
			if err != nil || explanation.State != "eligible" {
				t.Fatal(explanation, err)
			}
			if as := claim(); as.TaskID != task.TaskID || !legacy && (as.Execution == nil || as.Execution.Identity.LocalID != "beta") {
				t.Fatal("empty declarations blocked ordinary claim or changed pin", as)
			}
		})
	}
}

func TestCapabilityDisabledRuntimeReadinessAndRecovery(t *testing.T) {
	h, runtime, projection, _ := inventorySetup(t)
	runtime.JournalVersion = 19
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityAgentExecution)
	runtime.Sequence++
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	projection.Runtimes[0].Enabled = false
	projection.Runtimes[0].Availability.Reason = "binding_disabled"
	for i := range projection.Agents {
		projection.Agents[i].Availability.Reason = "binding_disabled"
	}
	register := func(local, server int64, snapshot string) string {
		t.Helper()
		_, hash, err := protocol.CanonicalAgentInventory(projection)
		if err != nil {
			t.Fatal(err)
		}
		if op := h.exec(runner("linux-1"), inventoryRequest(h, runtime, projection, hash, local, server, snapshot), RegisterAgentInventory{}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		return hash
	}
	register(1, 0, "disabled-binding")
	page, err := h.app.CapabilityAgents(t.Context(), protocol.AgentCapabilityQuery{})
	if err != nil || len(page.Items) != 2 {
		t.Fatal(page, err)
	}
	for _, candidate := range page.Items {
		if candidate.ExecutorReadiness.State != "unavailable" || candidate.ExecutorReadiness.Reason != "binding_disabled" || candidate.Current.State != "pending" {
			t.Fatal("known disabled binding became unknown", candidate)
		}
	}
	// Restore the accepted configuration through its real revision boundary,
	// then observe the new exact pins; no capability declarations are added.
	projection.Runtimes[0].Enabled = true
	projection.Runtimes[0].Revision++
	projection.Runtimes[0].Availability.Reason = "runtime_not_verified"
	for i := range projection.Agents {
		projection.Agents[i].Revision++
		projection.Agents[i].Binding.Revision = projection.Runtimes[0].Revision
		projection.Agents[i].Availability.Reason = "runtime_not_verified"
	}
	hash := register(2, 1, "enabled-binding")
	for _, a := range projection.Agents {
		runtime.AgentExecutors = append(runtime.AgentExecutors, protocol.AgentExecutor{Available: true, Pin: protocol.RunExecutionPin{Identity: a.Identity, AgentRevision: a.Revision, Runtime: a.Binding, InventoryRevision: 2, SnapshotID: "enabled-binding", PublicSHA256: hash, Profile: protocol.ExecutionProfile{Model: a.RequestedModel, Effort: a.RequestedEffort}, Worker: protocol.WorkerAppServer, BackendID: "git"}})
	}
	runtime.Sequence++
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	page, err = h.app.CapabilityAgents(t.Context(), protocol.AgentCapabilityQuery{State: "eligible"})
	if err != nil || len(page.Items) != 2 {
		t.Fatal("enabled binding failed to recover", page, err)
	}
	for _, candidate := range page.Items {
		if candidate.ExecutorReadiness.State != "ready" || candidate.DeclarationState != "empty" {
			t.Fatal(candidate)
		}
	}
}

func TestCapabilityLegacyWithoutInventoryExplainsCapacityAndRequirements(t *testing.T) {
	for _, wrongOS := range []bool{false, true} {
		t.Run(map[bool]string{false: "recoverable_capacity", true: "permanent_requirement"}[wrongOS], func(t *testing.T) {
			h := newHarness(t)
			runtime := testRuntime("legacy-capacity")
			if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			spec := testSpec
			if wrongOS {
				spec.Requirements.OS = "windows"
			}
			_, task := h.createTask(owner, createTaskPayload{Title: "ordinary legacy work", Spec: spec})
			config := testConfig()
			config.Hosts[0].Slots = 0
			if op := h.syncConfig(config); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			want := "pending"
			if wrongOS {
				want = "unavailable"
			}
			explanation, err := h.app.TaskCapabilityExplanation(t.Context(), task.TaskID, 0, protocol.RoleImplement)
			if err != nil || explanation.State != want || len(explanation.Candidates) != 0 || len(explanation.Required) != 0 {
				t.Fatal(explanation, err)
			}
			claim := func() protocol.Assignment {
				t.Helper()
				op := h.exec(runner("linux-1"), h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{Worker: h.worker("linux-1"), WorkspaceProtocol: 1, Runtime: runtime}, nil), Claim{})
				var result claimResult
				if op.Status != protocol.OpSucceeded || json.Unmarshal(op.Result, &result) != nil {
					t.Fatal(op)
				}
				return result.Assignment
			}
			if as := claim(); as.RunID != "" {
				t.Fatal("blocked legacy task claimed", as)
			}
			if op := h.syncConfig(testConfig()); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			explanation, err = h.app.TaskCapabilityExplanation(t.Context(), task.TaskID, 0, protocol.RoleImplement)
			if wrongOS {
				if err != nil || explanation.State != "unavailable" {
					t.Fatal("restoring capacity relaxed requirements", explanation, err)
				}
				if as := claim(); as.RunID != "" {
					t.Fatal("requirements bypassed", as)
				}
			} else {
				if err != nil || explanation.State != "eligible" {
					t.Fatal(explanation, err)
				}
				if as := claim(); as.TaskID != task.TaskID {
					t.Fatal("ordinary legacy claim did not recover", as)
				}
			}
		})
	}
}
