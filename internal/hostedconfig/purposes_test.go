package hostedconfig

import (
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
)

func TestHostedEntryPointsUseConfiguredAdapterInsteadOfHistoricalPurposeList(t *testing.T) {
	configuration := Configuration{Revision: 1, EnvironmentID: "hosted", MainAgentID: "same-agent", Definitions: agentconfig.Config{FormatVersion: 1,
		Runtimes: []agentconfig.RuntimeBinding{{ID: "native", Revision: 1, Kind: agentconfig.HostedManager, ExecutableRef: "configured-native", DeclaredPurposes: []agentconfig.Purpose{agentconfig.IssueManager}, Enabled: true}},
		Agents:   []agentconfig.AgentDefinition{{LocalID: "same-agent", Revision: 1, DisplayName: "Agent", RuntimeID: "native", Categories: []string{"planning"}, Purposes: []agentconfig.Purpose{agentconfig.IssueManager}, Model: "configured-model", Effort: "high", Enabled: true}},
	}}
	prepared, err := Prepare(configuration)
	if err != nil {
		t.Fatal("a historical purpose list blocked the explicit main binding", err)
	}
	for _, purpose := range []agentconfig.Purpose{agentconfig.Main, agentconfig.IssueManager, agentconfig.Consultation} {
		selected, err := prepared.Select("same-agent", purpose)
		if err != nil || selected.Purpose != purpose || selected.Identity.LocalID != "same-agent" || selected.Definition.Model != "configured-model" || selected.Definition.Effort != "high" || len(selected.Definition.Purposes) != 1 || selected.Definition.Purposes[0] != agentconfig.IssueManager {
			t.Fatal("supported entry point rewrote configuration or substituted identity", purpose, selected, err)
		}
	}
	for _, purpose := range []agentconfig.Purpose{agentconfig.WorkerRun, "unknown-entry"} {
		if _, err := prepared.Select("same-agent", purpose); err == nil {
			t.Fatal("hosted adapter fabricated an unsupported execution path", purpose)
		}
	}
	restored, err := Restore(prepared.PrivateJSON())
	if err != nil || restored.Hash() != prepared.Hash() {
		t.Fatal("historical configuration snapshot changed", err)
	}
	configuration.Definitions.Agents[0].Enabled = false
	if _, err := Prepare(configuration); err == nil {
		t.Fatal("disabled main binding was enabled")
	}
}
