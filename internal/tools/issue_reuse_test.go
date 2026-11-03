package tools

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
)

func TestIssueReuseNamedToolsRequireExactOwnerOriginAndCurrentCAS(t *testing.T) {
	for _, purpose := range []agentconfig.Purpose{agentconfig.Main, agentconfig.IssueManager, agentconfig.Consultation} {
		found := 0
		for _, spec := range NamedSpecs(purpose) {
			if spec.Name != "issues_reuse" && spec.Name != "issues_reuse_status" {
				continue
			}
			found++
			if purpose != agentconfig.Main {
				t.Fatal("non-Main received responsibility reuse", purpose, spec.Name)
			}
			if spec.Name == "issues_reuse_status" {
				continue
			}
			var root map[string]any
			if err := json.Unmarshal(spec.InputSchema, &root); err != nil {
				t.Fatal(err)
			}
			required := planningSchemaAt(t, root, "payload")["required"].([]any)
			for _, field := range []string{"original_request_id", "expected_assignment_epoch", "expected_association_revision", "source_message_id", "expected_source_revision", "reason"} {
				if !slices.Contains(required, any(field)) {
					t.Fatal("missing reuse identity", field, required)
				}
			}
		}
		if purpose == agentconfig.Main && found != 2 {
			t.Fatal("missing reuse write or origin read", found)
		}
	}
}
