package tools

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestDeploymentPreparationNamedToolKeepsResponsibilityAndExactSelector(t *testing.T) {
	for _, purpose := range []agentconfig.Purpose{agentconfig.Main, agentconfig.IssueManager, agentconfig.Consultation} {
		found := false
		for _, spec := range NamedSpecs(purpose) {
			if spec.Name != "issues_deployment_preparation_read" {
				continue
			}
			found = true
			var schema map[string]any
			if err := json.Unmarshal(spec.InputSchema, &schema); err != nil {
				t.Fatal(err)
			}
			props, ok := schema["properties"].(map[string]any)
			if !ok || len(props) != 1 || props["issue_id"] == nil || schema["additionalProperties"] != false {
				t.Fatalf("named tool schema widened the read selector: %s", spec.InputSchema)
			}
		}
		if found != (purpose != agentconfig.Consultation) {
			t.Fatalf("deployment preparation escaped its responsible role: %s found=%v", purpose, found)
		}
	}
	// Role rejection precedes access to either the process observer or store.
	var s Service
	result, handled := s.canonicalIssueRead(t.Context(), Call{Tool: "issues_deployment_preparation_read", Arguments: json.RawMessage(`{"issue_id":"unrelated"}`)}, store.HostedExecution{Selection: hostedconfig.Selection{Purpose: agentconfig.Consultation}})
	var answer answerView
	if !handled || json.Unmarshal([]byte(result.Text), &answer) != nil || answer.OK || answer.Error == nil || answer.Error.Code != protocol.CodeForbidden {
		t.Fatal("consultation read bypassed role scope", result)
	}
}
