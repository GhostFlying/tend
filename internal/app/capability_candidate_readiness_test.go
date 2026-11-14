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
