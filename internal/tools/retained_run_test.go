package tools

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
)

func TestRetainedRunNamedToolsExposeExactTypedRecoveryByResponsibility(t *testing.T) {
	for _, purpose := range []agentconfig.Purpose{agentconfig.Main, agentconfig.IssueManager, agentconfig.Consultation} {
		read, write := false, false
		for _, spec := range NamedSpecs(purpose) {
			if spec.Name == "runs_maintenance_reconciliation" {
				read = true
			}
			if spec.Name != "runs_continue_retained" {
				continue
			}
			write = true
			var root map[string]any
			if err := json.Unmarshal(spec.InputSchema, &root); err != nil {
				t.Fatal(err)
			}
			body := planningSchemaAt(t, root, "payload")
			required := body["required"].([]any)
			for _, field := range []string{"task_id", "action_id", "generation", "spec_version", "host_id", "lease_id", "execution", "protections", "protection_set_sha256", "reason"} {
				if !slices.Contains(required, any(field)) {
					t.Fatalf("%s recovery schema omitted %s", purpose, field)
				}
			}
			if purpose == agentconfig.Main {
				for _, field := range []string{"source_message_id", "expected_source_revision"} {
					if !slices.Contains(required, any(field)) {
						t.Fatal("Main recovery lacks admitted source contract", field)
					}
				}
			}
		}
		want := purpose != agentconfig.Consultation
		if read != want || write != want {
			t.Fatal("recovery tool escaped its responsibility surface", purpose, read, write)
		}
	}
}
