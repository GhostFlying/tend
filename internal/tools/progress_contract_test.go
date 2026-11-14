package tools

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestProgressAndWorkspaceContractsReachToolSchemas(t *testing.T) {
	var progress map[string]any
	if err := json.Unmarshal(hostedMutationSchema(protocol.UpdateIssueProgressPayload{}, "issues.update_progress"), &progress); err != nil {
		t.Fatal(err)
	}
	payload := progress["properties"].(map[string]any)["payload"].(map[string]any)
	fields := payload["allOf"].([]any)[0].(map[string]any)["properties"].(map[string]any)
	if fields["summary"].(map[string]any)["maxLength"] != float64(500) || fields["next_step"].(map[string]any)["maxLength"] != float64(240) {
		t.Fatal("hosted progress limits diverged", fields)
	}
	var spec map[string]any
	if err := json.Unmarshal(exportSchema(json.RawMessage(specSchema)), &spec); err != nil {
		t.Fatal(err)
	}
	props := spec["properties"].(map[string]any)
	workspace := props["workspace"].(map[string]any)["properties"].(map[string]any)
	base := workspace["mounts"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)["base"].(map[string]any)
	if base["not"] == nil || base["properties"].(map[string]any)["branch"].(map[string]any)["type"] != "string" {
		t.Fatal("legacy planning schema lost shared base selector contract", base)
	}
}
