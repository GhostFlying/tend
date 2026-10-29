package protocol

import "testing"

func TestAgentRequirementProfileSyntax(t *testing.T) {
	for _, test := range []struct {
		name, model, effort string
		valid               bool
	}{
		{"omitted", "", "", true}, {"model", "model", "", true}, {"effort_only", "", "high", true},
		{"both", "model", "xhigh", true}, {"model_space", " model", "", false},
		{"unsupported_effort_only", "", "unknown", false}, {"unsupported_effort", "model", "unknown", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			selection := TaskAgentSelection{Implementation: AgentRequirement{CategoryID: "implementer", Model: test.model, Effort: test.effort}, Acceptance: AgentRequirement{CategoryID: "reviewer"}}
			if err := selection.Validate(); (err == nil) != test.valid {
				t.Fatal(err)
			}
		})
	}
}
