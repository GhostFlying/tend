package app

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
)

// workerSpec projects a frozen spec by role before it crosses the runner
// boundary. It uses Scope, understood by older runners, rather than relying on
// a new prompt field being interpreted by an old harness.
func workerSpec(spec domain.Spec, role protocol.RunRole) (json.RawMessage, error) {
	// Selection is negotiated separately; retain the legacy spec wire shape.
	spec.AgentSelection = nil
	spec.AcceptanceExecution = nil
	var b strings.Builder
	b.WriteString(spec.Scope)
	for _, c := range spec.SharedContext {
		fmt.Fprintf(&b, "\n\nShared work-item background (%s, revision %d):\nIntent: %s\n", c.WorkItemID, c.Revision, c.Intent)
		for _, constraint := range c.Constraints {
			fmt.Fprintf(&b, "Owner constraint: %s\n", constraint)
		}
		for _, fact := range c.Facts {
			fmt.Fprintf(&b, "[%s] %s (source: %s)\n", fact.Kind, fact.Text, fact.Source)
		}
	}
	if role == protocol.RoleImplement && spec.ImplementationContext != "" {
		fmt.Fprintf(&b, "\nImplementation-only context:\n%s\n", spec.ImplementationContext)
	}
	spec.Scope = b.String()
	spec.ImplementationContext = ""
	spec.SharedContext = nil
	// Host selection is enforced by the server, not an extra runner wire field.
	spec.Requirements.HostID = ""
	// Provenance is a planning read model. Keep the original task-inputs-v1
	// wire shape for existing runners without mutating the frozen spec.
	spec.Inputs = slices.Clone(spec.Inputs)
	for i := range spec.Inputs {
		spec.Inputs[i].SourceTaskID = ""
		spec.Inputs[i].SourceArtifactID = ""
	}
	return json.Marshal(spec)
}

// acceptanceArtifact avoids priming reviewers with optional author narrative.
// A required report is itself a deliverable to review and stays visible.
func acceptanceArtifact(raw string, spec domain.Spec) (json.RawMessage, error) {
	if spec.RequiresDelivery(domain.DeliveryReport) {
		return json.RawMessage(raw), nil
	}
	var artifact map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &artifact); err != nil {
		return nil, err
	}
	delete(artifact, "report")
	return json.Marshal(artifact)
}
