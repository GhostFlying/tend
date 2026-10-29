package scheduler

import (
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestNamedSelectionRequiresCommonBackendAndKeepsHostCapacity(t *testing.T) {
	pin := func(agent, backend string) protocol.RunExecutionPin {
		return protocol.RunExecutionPin{Identity: protocol.AgentIdentity{ExecutionEnvironmentID: "host", LocalID: agent}, Runtime: protocol.AgentRuntimeReference{RuntimeID: agent}, BackendID: backend}
	}
	h := Host{ID: "host", Active: true, Slots: 1, Executors: []Executor{{Pin: pin("implement", "git"), Categories: []string{"implementer"}}}}
	c := Candidate{AgentSelection: &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer"}, Acceptance: protocol.AgentRequirement{CategoryID: "reviewer"}}, Kind: protocol.ActionImplement}
	if _, reason := SelectExecution(h, c); reason == "" {
		t.Fatal("implementation-only host selected")
	}
	h.Executors = append(h.Executors, Executor{Pin: pin("review", "orca"), Categories: []string{"reviewer"}})
	if _, reason := SelectExecution(h, c); reason == "" {
		t.Fatal("different workspace backends treated as equivalent")
	}
	h.Executors[1].Pin.BackendID = "git"
	first, reason := SelectExecution(h, c)
	if reason != "" || first.Identity.LocalID != "implement" {
		t.Fatal(first, reason)
	}
	// Named runtimes still share the existing host slot and unknown-workspace gates.
	valid := implement()
	valid.Repos = nil
	valid.AgentSelection = c.AgentSelection
	valid.WorkspaceOccupied = true
	if reason := SkipReason(h, valid); reason == nil || reason.Code != protocol.QueueWorkspaceBusy {
		t.Fatal("named selection bypassed possibly-running workspace", reason)
	}
	h.Busy = 1
	if reason := SkipReason(h, c); reason == nil || reason.Code != protocol.QueueHostCapacity {
		t.Fatal("runtime selection bypassed host slot gate", reason)
	}
	c.AgentSelection.Implementation.RuntimeID = "missing"
	if _, reason := SelectExecution(h, c); reason == "" {
		t.Fatal("hard runtime ignored")
	}
	c.AgentSelection.Implementation.RuntimeID = ""
	c.Kind = protocol.ActionAccept
	c.WorkspaceHost = "host"
	c.WorkspaceBackendID = "git"
	selected, reason := SelectExecution(h, c)
	if reason != "" || selected.Identity.LocalID != "review" {
		t.Fatal(selected, reason)
	}
}

func TestNamedModelEffortRequirementsFilterBothRoles(t *testing.T) {
	implementation := protocol.RunExecutionPin{Identity: protocol.AgentIdentity{ExecutionEnvironmentID: "host", LocalID: "implement"}, BackendID: "git", Profile: protocol.ExecutionProfile{Model: "implement-model", Effort: "high"}}
	review := implementation
	review.Identity.LocalID = "review"
	review.Profile = protocol.ExecutionProfile{Model: "review-model", Effort: "xhigh"}
	host := Host{ID: "host", Executors: []Executor{{Pin: implementation, Categories: []string{"implementer"}}, {Pin: review, Categories: []string{"reviewer"}}}}
	selection := protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer"}, Acceptance: protocol.AgentRequirement{CategoryID: "reviewer"}}
	candidate := Candidate{Kind: protocol.ActionImplement, AgentSelection: &selection, WorkspaceHost: "host", WorkspaceBackendID: "git"}
	for _, role := range []string{"implementation", "acceptance"} {
		for _, field := range []string{"model", "effort"} {
			t.Run(role+"_"+field, func(t *testing.T) {
				request := selection
				requirement := &request.Implementation
				if role == "acceptance" {
					requirement = &request.Acceptance
				}
				if field == "model" {
					requirement.Model = "different-model"
				} else {
					requirement.Effort = "low"
				}
				current := candidate
				current.AgentSelection = &request
				if pin, reason := SelectExecution(host, current); pin != nil || reason == "" {
					t.Fatal("first pair ignored requested profile", pin, reason)
				}
				current.Kind = protocol.ActionAccept
				if role == "acceptance" {
					if pin, reason := SelectExecution(host, current); pin != nil || reason == "" {
						t.Fatal("acceptance ignored requested profile", pin, reason)
					}
				}
			})
		}
	}
	// Empty effort means no filter, even when the local definition is explicit.
	selection.Implementation.Model = implementation.Profile.Model
	selection.Acceptance.Effort = review.Profile.Effort
	if pin, reason := SelectExecution(host, candidate); reason != "" || *pin != implementation {
		t.Fatal(pin, reason)
	}
	candidate.Kind = protocol.ActionAccept
	if pin, reason := SelectExecution(host, candidate); reason != "" || *pin != review {
		t.Fatal(pin, reason)
	}
	// The older acceptance override still requires the entire exact profile:
	// an omitted override effort is not a wildcard.
	candidate.AcceptanceProfile = &protocol.ExecutionProfile{Model: review.Profile.Model}
	if pin, reason := SelectExecution(host, candidate); pin != nil || reason == "" {
		t.Fatal("legacy exact profile became partial", pin, reason)
	}
}
