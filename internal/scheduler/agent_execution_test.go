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

func TestCapabilityAcceptancePrerequisiteBoundaryAndFilter(t *testing.T) {
	implementer := protocol.RunExecutionPin{Identity: protocol.AgentIdentity{ExecutionEnvironmentID: "host", LocalID: "implement"}, BackendID: "git"}
	reviewer := implementer
	reviewer.Identity.LocalID = "review"
	ref := protocol.CapabilityReference{CapabilityID: "image_gen", DefinitionRevision: 1}
	host := Host{ID: "host", Executors: []Executor{{Pin: implementer, Categories: []string{"implementer"}}}}
	c := Candidate{Kind: protocol.ActionImplement, AgentSelection: &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer"}, Acceptance: protocol.AgentRequirement{CategoryID: "reviewer", AgentID: "host/review", RequiredCapabilities: []protocol.CapabilityReference{ref}}}}
	for _, scenario := range []string{"first", "waived", "assigned", "workspace_only", "no_workspace", "acceptance_action"} {
		t.Run(scenario, func(t *testing.T) {
			trial := c
			want := true
			switch scenario {
			case "waived":
				trial.AcceptanceWaived, want = true, false
			case "assigned":
				trial.NamedExecutionAssigned, trial.WorkspaceHost, want = true, "host", false
			case "workspace_only":
				trial.WorkspaceHost = "host"
			case "no_workspace":
				trial.NamedExecutionAssigned = true
			case "acceptance_action":
				trial.Kind, want = protocol.ActionAccept, false
			}
			if RequiresAcceptancePrerequisite(trial) != want {
				t.Fatal("prerequisite boundary differs", trial)
			}
			pin, _ := SelectExecution(host, trial)
			if (pin == nil) != (want || trial.Kind == protocol.ActionAccept) {
				t.Fatal("selection ignored prerequisite boundary", pin)
			}
		})
	}
	host.Executors = append(host.Executors, Executor{Pin: reviewer, Categories: []string{"reviewer"}, CapabilitySupport: true, DeclaredCapabilities: []protocol.CapabilityReference{ref}})
	c.ReviewExecutionFilter = &reviewer
	if pin, reason := SelectExecution(host, c); pin == nil || *pin != implementer || reason != "" {
		t.Fatal("review prerequisite filter prevented valid pair", pin, reason)
	}
	c.ReviewExecutionFilter = &implementer
	if pin, _ := SelectExecution(host, c); pin != nil {
		t.Fatal("review filter overwrote acceptance identity/category", pin)
	}
	c.ReviewExecutionFilter = &reviewer
	host.Executors[1].Pin.BackendID = "orca"
	if pin, _ := SelectExecution(host, c); pin != nil {
		t.Fatal("review filter bypassed common backend", pin)
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

func TestConcurrencySkipsBusyRuntimeWithoutReservingReviewerCapacity(t *testing.T) {
	pin := func(agent, runtime string) protocol.RunExecutionPin {
		return protocol.RunExecutionPin{Identity: protocol.AgentIdentity{ExecutionEnvironmentID: "host", LocalID: agent}, Runtime: protocol.AgentRuntimeReference{RuntimeID: runtime}, BackendID: "git"}
	}
	h := Host{ID: "host", ConcurrencyBlocked: map[string]bool{"busy": true, "review": true}, Executors: []Executor{{Pin: pin("alpha", "busy"), Categories: []string{"implementer"}}, {Pin: pin("beta", "free"), Categories: []string{"implementer"}}, {Pin: pin("review", "review"), Categories: []string{"reviewer"}}}}
	c := Candidate{Kind: protocol.ActionImplement, AgentSelection: &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer"}, Acceptance: protocol.AgentRequirement{CategoryID: "reviewer"}}}
	p, reason := SelectExecution(h, c)
	if reason != "" || p == nil || p.Identity.LocalID != "beta" {
		t.Fatal(p, reason)
	}
	c.Kind = protocol.ActionAccept
	if p, reason := SelectExecution(h, c); p != nil || reason == "" {
		t.Fatal("busy reviewer selected", p, reason)
	}
}

func TestCapabilityDeclarationSelectionKeepsBothRoleAndFrozenCandidateConstraints(t *testing.T) {
	ref := protocol.CapabilityReference{CapabilityID: "image_gen", DefinitionRevision: 1}
	a := protocol.RunExecutionPin{Identity: protocol.AgentIdentity{ExecutionEnvironmentID: "host", LocalID: "a"}, Runtime: protocol.AgentRuntimeReference{RuntimeID: "original", Revision: 1}, BackendID: "git"}
	b := a
	b.Identity.LocalID = "b"
	reviewer := a
	reviewer.Identity.LocalID = "review"
	host := Host{ID: "host", Executors: []Executor{
		{Pin: a, Categories: []string{"implementer"}, CapabilitySupport: true, DeclaredCapabilities: []protocol.CapabilityReference{ref}},
		{Pin: b, Categories: []string{"implementer"}, CapabilitySupport: true, DeclaredCapabilities: []protocol.CapabilityReference{ref}},
		{Pin: reviewer, Categories: []string{"reviewer"}},
	}}
	selection := protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer", AgentID: "host/a", RuntimeID: "original", RequiredCapabilities: []protocol.CapabilityReference{ref}}, Acceptance: protocol.AgentRequirement{CategoryID: "reviewer"}}
	c := Candidate{Kind: protocol.ActionImplement, AgentSelection: &selection, WorkspaceBackendID: "git"}
	if pin, reason := SelectExecution(host, c); reason != "" || pin == nil || *pin != a {
		t.Fatal(pin, reason)
	}
	// Per-candidate explanation must retain, rather than replace, each hard input.
	for _, mutate := range []func(*protocol.RunExecutionPin){
		func(p *protocol.RunExecutionPin) { p.Identity.LocalID = "b" },
		func(p *protocol.RunExecutionPin) { p.Runtime.RuntimeID = "other" },
		func(p *protocol.RunExecutionPin) { p.BackendID = "orca" },
	} {
		other := a
		mutate(&other)
		host.Executors = append(host.Executors, Executor{Pin: other, Categories: []string{"implementer"}, CapabilitySupport: true, DeclaredCapabilities: []protocol.CapabilityReference{ref}})
		c.ExecutionFilter = &other
		if pin, reason := SelectExecution(host, c); pin != nil || reason == "" {
			t.Fatal("explanation replaced a frozen constraint", pin, reason)
		}
	}
	c.ExecutionFilter = nil
	host.Executors[0].DeclaredCapabilities = nil
	if pin, _ := SelectExecution(host, c); pin != nil {
		t.Fatal("undeclared pinned Agent selected", pin)
	}
	host.Executors[0].DeclaredCapabilities = []protocol.CapabilityReference{{CapabilityID: "image_gen", DefinitionRevision: 2}}
	if pin, _ := SelectExecution(host, c); pin != nil {
		t.Fatal("wrong definition revision selected", pin)
	}
	host.Executors[0].DeclaredCapabilities = []protocol.CapabilityReference{ref}
	host.Executors[0].CapabilitySupport = false
	if pin, _ := SelectExecution(host, c); pin != nil {
		t.Fatal("old evidence-only peer selected", pin)
	}
	selection.Implementation.RequiredCapabilities = nil
	if pin, reason := SelectExecution(host, c); pin == nil || reason != "" {
		t.Fatal("empty required introduced a label gate", pin, reason)
	}
}
