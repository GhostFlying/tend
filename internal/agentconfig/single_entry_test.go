package agentconfig

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestSingleEntryMultipleRolesPreserveIdentityAndProfile(t *testing.T) {
	c := Config{FormatVersion: 3, Runtimes: []RuntimeBinding{{ID: "primary", Revision: 2, Kind: OrcaWorker, CLI: "traex", ExecutableRef: "private-entry", DeclaredPurposes: []Purpose{WorkerRun, IssueManager}, Enabled: true}}, Agents: []AgentDefinition{{LocalID: "ordinary", Revision: 3, DisplayName: "Ordinary", RuntimeID: "primary", Categories: []string{"investigation", "implementation", "formal-review"}, Purposes: []Purpose{WorkerRun, IssueManager}, Model: "original-model", Effort: "high", Enabled: true}}}
	// Sharing an execution tuple is legal; Agent identity is not the tuple.
	second := c.Agents[0]
	second.LocalID, second.DisplayName = "another", "Another"
	c.Agents = append(c.Agents, second)
	d, err := Validate("existing-env", c, SourceReference{Kind: Inline})
	if err != nil {
		t.Fatal(err)
	}
	p := d.Public()
	if _, _, err := protocol.CanonicalAgentInventory(p); err != nil {
		t.Fatal(err)
	}
	for _, a := range p.Agents {
		if a.OwnerBinding != nil || a.Binding.RuntimeID != "primary" || a.RequestedModel != "original-model" || a.RequestedEffort != "high" || len(a.Categories) != 3 || a.Identity.ExecutionEnvironmentID != "existing-env" {
			t.Fatal("role changed the execution identity", a)
		}
	}
	c.Agents[0].OwnerRuntimeID = "primary"
	if _, err := Validate("existing-env", c, SourceReference{Kind: Inline}); err == nil {
		t.Fatal("new format retained a second owner entry")
	}
	binding := p.Agents[0].Binding
	p.Agents[0].OwnerBinding = &binding
	if _, _, err := protocol.CanonicalAgentInventory(p); err == nil {
		t.Fatal("wire projection accepted a second owner entry")
	}
	c.Agents[0].OwnerRuntimeID = ""
	c.Runtimes[0].CLI = ""
	if _, err := Validate("existing-env", c, SourceReference{Kind: Inline}); err == nil {
		t.Fatal("new format inferred CLI from an entry name")
	}
}

func TestSingleEntryMigrationRequiresNewDefinitionRevisionAndNoDowngrade(t *testing.T) {
	old := ownerBindingConfig()
	next := cloneConfig(old)
	next.FormatVersion = 3
	for i := range next.Runtimes {
		if next.Runtimes[i].CLI == "" {
			next.Runtimes[i].CLI = "codex"
			next.Runtimes[i].Revision++
		}
	}
	next.Agents[0].OwnerRuntimeID = ""
	if err := ValidateUpdate(next, old); err == nil {
		t.Fatal("migration rewrote a historical Agent revision")
	}
	next.Agents[0].Revision++
	if err := ValidateUpdate(next, old); err != nil {
		t.Fatal(err)
	}
	if err := ValidateUpdate(old, next); err == nil {
		t.Fatal("active format downgraded to dual entry")
	}
	changed := cloneConfig(next)
	changed.Agents[0].Effort = "medium"
	if err := ValidateUpdate(changed, next); err == nil {
		t.Fatal("effort hot changed within one definition revision")
	}
}

func TestSingleEntryDecodeRejectsEvenEmptyRetiredOwnerField(t *testing.T) {
	cfg := ownerBindingConfig()
	cfg.FormatVersion = 3
	cfg.Agents[0].OwnerRuntimeID = ""
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(raw); err != nil {
		t.Fatal(err)
	}
	raw = bytes.Replace(raw, []byte(`"local_id":`), []byte(`"owner_runtime_id":"","local_id":`), 1)
	if _, err := Decode(raw); err == nil {
		t.Fatal("format 3 accepted a retired second-entry field")
	}
}
