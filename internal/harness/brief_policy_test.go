package harness

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestBriefUsesFrozenCreatorPolicyIndependentlyOfRoleAndDeliverable(t *testing.T) {
	for _, delivery := range []string{"report", "files", "commit"} {
		for _, policy := range []*protocol.IssueAcceptancePolicy{nil, {Required: true}, {Required: false, Reason: "Bounded investigation"}} {
			spec := WorkerSpec{Role: protocol.RoleImplement, AcceptancePolicy: policy, Spec: json.RawMessage(`{"goal":"Inspect the original evidence","deliverables":["` + delivery + `"]}`)}
			instructions, input := Brief(spec)
			waived := policy != nil && !policy.Required
			if strings.Contains(instructions, "An independent reviewer will check your work.") == waived {
				t.Fatal("launch promise differs from frozen creator policy", delivery, policy, instructions)
			}
			if waived && !strings.Contains(instructions, "does not require separate business acceptance") {
				t.Fatal(instructions)
			}
			if !strings.Contains(instructions, "complete checks and CI") || !strings.Contains(instructions, "Report-only work does not require Git changes") || !strings.Contains(input, "Execute this task") {
				t.Fatal("delivery or repository contract changed", instructions, input)
			}
			spec.Role = protocol.RoleAccept
			reviewer, _ := Brief(spec)
			if !strings.Contains(reviewer, VerdictTool) || strings.Contains(reviewer, "does not require separate business acceptance") {
				t.Fatal("actual acceptance assignment lost independent review contract", reviewer)
			}
		}
	}
}
