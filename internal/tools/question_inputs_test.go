package tools

import (
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
)

func TestQuestionInputsMainToolBoundary(t *testing.T) {
	for _, purpose := range []agentconfig.Purpose{agentconfig.Main, agentconfig.IssueManager, agentconfig.Consultation} {
		seen := map[string]bool{}
		for _, spec := range NamedSpecs(purpose) {
			seen[spec.Name] = true
		}
		for _, name := range []string{"question_inputs_read", "question_inputs_grant", "question_inputs_revoke"} {
			if seen[name] != (purpose == agentconfig.Main) {
				t.Fatal(purpose, name, seen[name])
			}
		}
	}
}
