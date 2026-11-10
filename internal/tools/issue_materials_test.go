package tools

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
)

func TestIssueMaterialsNamedToolIsOrdinaryReadForCoordinators(t *testing.T) {
	for _, purpose := range []agentconfig.Purpose{agentconfig.Main, agentconfig.IssueManager, agentconfig.Consultation} {
		found := false
		for _, spec := range NamedSpecs(purpose) {
			if spec.Name != "issues_materials_read" {
				continue
			}
			found = true
			var schema map[string]any
			if err := json.Unmarshal(spec.InputSchema, &schema); err != nil {
				t.Fatal(err)
			}
			props := schema["properties"].(map[string]any)
			for _, field := range []string{"issue_id", "kind", "id", "duty_revision", "after", "limit"} {
				if props[field] == nil {
					t.Fatal("ordinary read lost a material or pagination selector", purpose, field)
				}
			}
			for _, field := range []string{"expected_control_revision", "grant_revision", "source_message_id", "spec_version", "assignment_epoch"} {
				if props[field] != nil {
					t.Fatal("ordinary reading gained a permission ceremony", purpose, field)
				}
			}
		}
		if found != (purpose != agentconfig.Consultation) {
			t.Fatal("coordinator material read unavailable", purpose, found)
		}
	}
}
