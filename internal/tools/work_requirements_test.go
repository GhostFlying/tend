package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/protocol"
)

type workRequirementsOwner struct{}

func (workRequirementsOwner) Specs() []protocol.OwnerToolSpec {
	var out []protocol.OwnerToolSpec
	for _, m := range hostedMutations {
		out = append(out, protocol.OwnerToolSpec{Name: "owner_" + m.name, Capability: ownerMutationOperation(m)})
	}
	return out
}
func (workRequirementsOwner) Call(context.Context, protocol.OwnerToolCall) (any, error) {
	panic("discovery must not execute a mutation")
}

func assertWorkSelection(t *testing.T, requirement map[string]any) {
	t.Helper()
	props := requirement["properties"].(map[string]any)
	for _, key := range []string{"agent_id", "runtime_id", "model", "effort"} {
		if props[key] != nil {
			t.Fatalf("ordinary discovery exposes %s", key)
		}
	}
	if props["category_id"] == nil || props["required_capabilities"] == nil {
		t.Fatal("work requirements lost", requirement)
	}
}

func TestOrdinaryWorkDiscoveryCoversBothRolesAndOwnerEntrypoints(t *testing.T) {
	s := &Service{owner: workRequirementsOwner{}}
	for _, purpose := range []agentconfig.Purpose{agentconfig.Main, agentconfig.IssueManager} {
		found := 0
		for _, spec := range s.NamedSpecs(purpose) {
			var root map[string]any
			if err := json.Unmarshal(spec.InputSchema, &root); err != nil {
				t.Fatal(err)
			}
			switch spec.Name {
			case "issues_create_task", "issues_update_task":
				found++
				for _, role := range []string{"implementation", "acceptance"} {
					assertWorkSelection(t, planningSchemaAt(t, root, "payload", "spec", "agent_selection", role))
				}
				props := planningSchemaAt(t, root, "payload", "spec")["properties"].(map[string]any)
				if props["acceptance_execution"] != nil {
					t.Fatal("acceptance profile bypass is advertised")
				}
				if planningSchemaAt(t, root, "payload", "spec", "requirements", "host_id")["description"] == nil {
					t.Fatal("real resource requirements lost guidance")
				}
			case "issues_create":
				props := planningSchemaAt(t, root, "payload")["properties"].(map[string]any)
				if props["assignee"] != nil || props["manager"] != nil {
					t.Fatal("legacy concrete assignment bypass is advertised")
				}
				if planningSchemaAt(t, root, "payload", "owner")["properties"].(map[string]any)["agent_id"] != nil {
					t.Fatal("new owner advertises concrete Agent")
				}
			case "issues_request_owner":
				if planningSchemaAt(t, root, "payload")["properties"].(map[string]any)["agent_id"] != nil {
					t.Fatal("request owner advertises concrete Agent")
				}
			case "issues_set_status":
				if planningSchemaAt(t, root, "payload", "owner")["properties"].(map[string]any)["agent_id"] != nil {
					t.Fatal("status owner advertises concrete Agent")
				}
			}
		}
		if purpose == agentconfig.IssueManager && found != 2 {
			t.Fatal("task tools missing", found)
		}
	}
	// Keep full historical and owner wire schemas. Discovery changes must not
	// silently discard an old callback's accepted profile or pin.
	var original map[string]any
	_ = json.Unmarshal([]byte(argumentSchema(protocol.OwnerTaskSpec{})), &original)
	defs := original["definitions"].(map[string]any)
	if defs["AgentRequirement"].(map[string]any)["properties"].(map[string]any)["agent_id"] == nil || original["properties"].(map[string]any)["acceptance_execution"] == nil {
		t.Fatal("wire schema was narrowed")
	}
}

func TestOwnerTypeDiscoveryUsesWorkProjectionButKeepsHistoryWhole(t *testing.T) {
	s := &Service{owner: workRequirementsOwner{}}
	for _, name := range []string{"OwnerTaskSpec", "TaskAgentSelection", "CreateIssueTaskPayload"} {
		result, err := s.ownerCatalog(ownerCatalogArgs{SchemaType: name})
		if err != nil {
			t.Fatal(err)
		}
		var root map[string]any
		_ = json.Unmarshal(result.(map[string]any)["schema"].(json.RawMessage), &root)
		defs := root["definitions"].(map[string]any)
		assertWorkSelection(t, defs["AgentRequirement"].(map[string]any))
	}
	result, err := s.ownerCatalog(ownerCatalogArgs{SchemaType: "TaskSelectionSource"})
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	_ = json.Unmarshal(result.(map[string]any)["schema"].(json.RawMessage), &root)
	defs := root["definitions"].(map[string]any)
	if defs["AgentRequirement"].(map[string]any)["properties"].(map[string]any)["model"] == nil {
		t.Fatal("history was projected as new model input")
	}
}
