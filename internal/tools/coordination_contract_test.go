package tools

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
)

func TestNamedCoordinationSchemasDescribeWritableContracts(t *testing.T) {
	for _, spec := range NamedSpecs(agentconfig.Main) {
		if spec.Name != "issues_create" && spec.Name != "planner_entry_put" {
			continue
		}
		var root map[string]any
		if err := json.Unmarshal(spec.InputSchema, &root); err != nil {
			t.Fatal(err)
		}
		payload := planningSchemaAt(t, root, "payload")
		if spec.Name == "issues_create" {
			owner := planningSchemaAt(t, root, "payload", "owner")
			ownerProps := owner["properties"].(map[string]any)
			if !slices.Contains(owner["required"].([]any), any("category_id")) || ownerProps["expected_assignment_epoch"] != nil || ownerProps["source_message_id"] != nil {
				t.Fatal("new Issue ownership asks the model to invent transaction metadata", owner)
			}
			if payload["properties"].(map[string]any)["manager"] != nil {
				t.Fatal("new named turns advertise historical manager publication")
			}
			required := payload["required"].([]any)
			for _, field := range []string{"source_message_id", "expected_source_revision"} {
				if !slices.Contains(required, any(field)) {
					t.Fatalf("%s not required: %v", field, required)
				}
				if planningSchemaAt(t, root, "payload", field)["description"] == nil {
					t.Fatalf("%s lacks exact source instructions", field)
				}
			}
		} else {
			entry := planningSchemaAt(t, root, "payload", "entry")
			props := entry["properties"].(map[string]any)
			for _, field := range []string{"entry_id", "revision", "updated_at"} {
				if props[field] != nil {
					t.Fatalf("tool asks agent to invent %s", field)
				}
			}
			for field, values := range map[string][]any{"kind": {"user_action", "issue_priority"}, "status": {"open", "blocked", "done", "cancelled"}} {
				got := planningSchemaAt(t, root, "payload", "entry", field)["enum"].([]any)
				if !slices.Equal(got, values) {
					t.Fatalf("%s=%v", field, got)
				}
			}
		}
	}
}

func TestNamedOwnerSchemasCarryWorkspaceRecoveryInstructions(t *testing.T) {
	found := 0
	for _, spec := range NamedSpecs(agentconfig.IssueManager) {
		if spec.Name != "issues_create_task" && spec.Name != "issues_update_task" {
			continue
		}
		found++
		var root map[string]any
		if err := json.Unmarshal(spec.InputSchema, &root); err != nil {
			t.Fatal(err)
		}
		for field, text := range map[string]string{"mode": "Omitted means new", "source_task_id": "follow_up", "include_existing_changes": "issues_update_task"} {
			description, _ := planningSchemaAt(t, root, "payload", "spec", "workspace", field)["description"].(string)
			if !strings.Contains(description, text) {
				t.Fatalf("%s lost %s instructions in actual named tool schema", spec.Name, field)
			}
		}
	}
	if found != 2 {
		t.Fatal("named execution tools missing", found)
	}
}
