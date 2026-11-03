package tools

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
)

func TestCreatorAcceptancePolicyNamedToolExplainsChoiceWithoutCreatorInput(t *testing.T) {
	for _, purpose := range []agentconfig.Purpose{agentconfig.Main, agentconfig.IssueManager} {
		foundCreate, foundPolicy := false, false
		for _, spec := range NamedSpecs(purpose) {
			if spec.Name != "issues_create" && spec.Name != "issues_set_acceptance_policy" {
				continue
			}
			var root map[string]any
			if err := json.Unmarshal(spec.InputSchema, &root); err != nil {
				t.Fatal(err)
			}
			payload := planningSchemaAt(t, root, "payload")
			props := payload["properties"].(map[string]any)
			if props["creator"] != nil || props["decided_by"] != nil || props["acceptance_policy"] != nil {
				t.Fatal("tool asks model to supply trusted provenance", props)
			}
			if spec.Name == "issues_create" {
				foundCreate = true
				policy := planningSchemaAt(t, root, "payload", "independent_acceptance")
				if !slices.Contains(policy["required"].([]any), any("required")) || !strings.Contains(spec.Description, "read-only") {
					t.Fatal(spec.Description, policy)
				}
			} else {
				foundPolicy = true
				if !strings.Contains(spec.Description, "future tasks") || !strings.Contains(spec.Description, "workers") {
					t.Fatal(spec.Description)
				}
			}
		}
		if !foundCreate || !foundPolicy {
			t.Fatal("creator's named policy surface is missing", purpose)
		}
	}
	for _, spec := range NamedSpecs(agentconfig.Consultation) {
		if spec.Name == "issues_set_acceptance_policy" {
			t.Fatal("consultation gained policy authority")
		}
	}
}
