package agentconfig

import (
	"encoding/json"
	"strings"
	"testing"
)

func ownerBindingConfig() Config {
	return Config{FormatVersion: 2, Runtimes: []RuntimeBinding{
		{ID: "terminal", Revision: 1, Kind: OrcaWorker, CLI: "codex", ExecutableRef: "PRIVATE_TERMINAL_COMMAND", DeclaredPurposes: []Purpose{WorkerRun}, Enabled: true},
		{ID: "native", Revision: 2, Kind: AppServerWorker, CLI: "codex", ExecutableRef: "PRIVATE_NATIVE_COMMAND", DeclaredPurposes: []Purpose{WorkerRun}, Enabled: true},
	}, Agents: []AgentDefinition{{LocalID: "implementer", Revision: 3, DisplayName: "Implementer", RuntimeID: "terminal", OwnerRuntimeID: "native", Categories: []string{"implementation"}, Purposes: []Purpose{WorkerRun}, Model: "model", Enabled: true}}}
}

func TestOwnerBindingSharesOrdinaryAgentIdentity(t *testing.T) {
	cfg := ownerBindingConfig()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	d, err := Validate("runner", decoded, SourceReference{Kind: Inline})
	if err != nil {
		t.Fatal(err)
	}
	p := d.Public()
	if len(p.Agents) != 1 || p.Agents[0].OwnerBinding == nil || p.Agents[0].OwnerBinding.RuntimeID != "native" || p.Agents[0].Binding.RuntimeID != "terminal" || p.Agents[0].Identity.LocalID != "implementer" || len(p.Agents[0].Purposes) != 1 || p.Agents[0].Purposes[0] != "worker_run" {
		t.Fatal("coordination created another Agent or changed its execution role", p)
	}
	public, _, err := d.CanonicalPublic()
	if err != nil || strings.Contains(string(public), "PRIVATE_") {
		t.Fatal("invalid or private owner projection", err)
	}
}

func TestOwnerBindingRequiresExplicitSupportedTransport(t *testing.T) {
	for _, mutate := range []func(*Config){
		func(c *Config) { c.FormatVersion = 1 },
		func(c *Config) { c.Agents[0].OwnerRuntimeID = "missing" },
		func(c *Config) { c.Agents[0].OwnerRuntimeID = "terminal" },
		func(c *Config) { c.Runtimes[1].CLI = "" },
	} {
		cfg := ownerBindingConfig()
		mutate(&cfg)
		if _, err := Validate("runner", cfg, SourceReference{Kind: Inline}); err == nil {
			t.Fatal("accepted an unsupported owner transport")
		}
	}
}
