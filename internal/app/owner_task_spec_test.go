package app

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestOwnerTaskInputProjectionMapsEverySuppliedField(t *testing.T) {
	input := protocol.OwnerTaskSpec{AgentSelection: &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer", Model: "implementation-model", Effort: "high"}, Acceptance: protocol.AgentRequirement{CategoryID: "reviewer", Model: "formal-review", Effort: "xhigh"}}, AcceptanceExecution: &protocol.ExecutionProfile{Model: "formal-review", Effort: "xhigh"}, Observation: &protocol.ObservationScope{WorkItemIDs: []string{"wi_scope"}, Deployment: true}, Goal: "Explicit owner goal", Scope: "Bounded work", Constraints: []string{"Keep compatibility"}, Acceptance: []string{"Report measured behavior"}, Delivery: "commit", Deliverables: []string{"commit", "report"}, Inputs: []protocol.OwnerTaskInput{{FileID: "fil_original"}}, ImplementationContext: "Implementation-only rationale", MaxImplementAttempts: 2, Workspace: protocol.OwnerTaskWorkspace{Mode: "reuse", SourceTaskID: "task_original", IncludeExistingChanges: true, Template: "configured-template", Mounts: []protocol.OwnerTaskMount{{Repo: "example", Path: "source", Base: protocol.OwnerTaskBase{Branch: "develop", Commit: ""}, Profile: "prepare"}}, Profile: "link"}, Requirements: protocol.OwnerTaskRequirements{HostID: "host-a", OS: "linux", Labels: []string{"builder"}}}
	mapped := domainSpecFromOwner(input)
	left, _ := json.Marshal(input)
	right, _ := json.Marshal(mapped)
	var l, r map[string]any
	_ = json.Unmarshal(left, &l)
	_ = json.Unmarshal(right, &r)
	if !reflect.DeepEqual(l, r) {
		t.Fatalf("owner projection drifted from domain mapping\nowner=%s\ndomain=%s", left, right)
	}
	if len(mapped.SharedContext) != 0 || len(mapped.Inputs) != 1 || mapped.Inputs[0].Name != "" || mapped.Inputs[0].SHA256 != "" {
		t.Fatal("owner supplied server-frozen fields")
	}
}
