package tools

import (
	"encoding/json"

	"github.com/GhostFlying/tend/internal/protocol"
)

// projectWorkRequirements is a discovery projection, not wire validation or an
// authorization check. Existing callbacks retain their filters and attribution.
// Keep this separate from exportSchema: history/output schemas must stay whole.
func projectWorkRequirements(name string, body map[string]any) {
	props, ok := body["properties"].(map[string]any)
	if !ok {
		return
	}
	switch name {
	case "AgentRequirement":
		for _, key := range []string{"agent_id", "runtime_id", "model", "effort"} {
			delete(props, key)
		}
		body["description"] = "Express work using category_id and required_capabilities. Choose implementation or implementation-economy by difficulty/cost. Tend selects the configured executor; continuation pins are maintained by the system."
	case "OwnerTaskSpec":
		delete(props, "acceptance_execution")
	case "IssueOwnerRequest", "RequestIssueManagerPayload":
		delete(props, "agent_id")
		body["description"] = "Select a work category for the new responsibility. Tend chooses its Agent; source_message_id cites material and does not prove human assignment."
	case "CreateIssuePayload":
		delete(props, "assignee")
		delete(props, "manager")
	case "PromoteConsultationPayload":
		delete(props, "assignee")
		// Promotion has no owner input. Retain its compatible manager key as
		// the work-category path, without new-responsibility epoch/pin fields.
		var manager map[string]any
		if err := json.Unmarshal([]byte(argumentSchema(protocol.IssueOwnerRequest{})), &manager); err != nil {
			panic(err)
		}
		projectWorkRequirements("IssueOwnerRequest", manager)
		delete(manager, "definitions")
		manager["description"] = "For a new Issue, provide category_id and reason for the work. Tend selects its executor. With existing_issue_id, retain the existing responsibility and omit this selector."
		props["manager"] = manager
	case "OwnerTaskRequirements":
		if host, ok := props["host_id"].(map[string]any); ok {
			host["description"] = "Only for a real host-bound resource requirement. Leave omitted for ordinary work; do not copy the current Agent's host or bind a preferred provider. OS, labels and workspace constraints remain independently enforced."
		}
	}
}

func projectWorkRequirementDefinitions(root map[string]any) {
	defs, _ := root["definitions"].(map[string]any)
	for name, value := range defs {
		if body, ok := value.(map[string]any); ok {
			projectWorkRequirements(name, body)
		}
	}
}
