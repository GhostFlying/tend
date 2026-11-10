package agentconfig

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestHistoricalPurposesDoNotRestrictConfiguredAgent(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*Config)
	}{
		{"worker-consultation", func(c *Config) { c.Agents[0].Purposes = []Purpose{Consultation} }},
		{"hosted-execution", func(c *Config) { c.Agents[2].Purposes = []Purpose{WorkerRun} }},
		{"undeclared-purpose", func(c *Config) { c.Agents[1].Purposes = []Purpose{Main, IssueManager} }},
		{"duplicate-label", func(c *Config) { c.Agents[0].Purposes = []Purpose{WorkerRun, WorkerRun} }},
		{"no-labels", func(c *Config) { c.Runtimes[0].DeclaredPurposes, c.Agents[0].Purposes = []Purpose{}, []Purpose{} }},
		{"same-adapter-category-capabilities", func(c *Config) {
			a := c.Agents[0]
			a.LocalID = "implement-b"
			a.Purposes = []Purpose{Main, Consultation}
			c.Agents = append(c.Agents, a)
		}},
		{"retained-label", func(c *Config) {
			c.Runtimes[0].DeclaredPurposes = []Purpose{"retained_responsibility"}
			c.Agents[0].Purposes = []Purpose{"other_retained_responsibility"}
		}},
		{"implicit-appserver-cli", func(c *Config) {
			c.FormatVersion = 2
			c.Runtimes[1].DeclaredPurposes = []Purpose{IssueManager}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := fixture()
			cfg.CapabilityVersion = 1
			for i := range cfg.Agents {
				cfg.Agents[i].Capabilities = []protocol.CapabilityReference{{CapabilityID: "image_gen", DefinitionRevision: 1}}
			}
			tt.change(&cfg)
			raw, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := Decode(raw)
			if err != nil || !reflect.DeepEqual(decoded, cfg) {
				t.Fatal("configuration entry changed retained metadata", err)
			}
			d := requireValid(t, "env-a", decoded)
			projected := d.Public()
			for _, want := range cfg.Agents {
				index := slices.IndexFunc(projected.Agents, func(a protocol.AgentInventoryDefinition) bool { return a.Identity.LocalID == want.LocalID })
				if index < 0 {
					t.Fatal("configured identity disappeared", want.LocalID)
				}
				got := projected.Agents[index]
				purposes := publicPurposes(want.Purposes)
				slices.Sort(purposes)
				categories := slices.Clone(want.Categories)
				slices.Sort(categories)
				if got.Identity.ExecutionEnvironmentID != "env-a" || got.Binding.RuntimeID != want.RuntimeID || got.RequestedModel != want.Model || got.RequestedEffort != want.Effort || !slices.Equal(got.Categories, categories) || !slices.Equal(got.Capabilities, want.Capabilities) || !slices.Equal(got.Purposes, purposes) {
					t.Fatalf("metadata changed real identity/configuration: got %+v, want %+v", got, want)
				}
			}
			local, hash := publicBytes(t, d)
			wire, wireHash, err := protocol.CanonicalAgentInventory(projected)
			if err != nil || !bytes.Equal(local, wire) || hash != wireHash {
				t.Fatal("accepted local metadata cannot register on the inventory entry", err)
			}
		})
	}
}

func TestHistoricalPurposesDoNotOverrideRealConfigurationValidation(t *testing.T) {
	for _, tt := range []struct {
		name, code, path string
		change           func(*Config)
	}{
		{"adapter-kind", "unsupported_runtime", "$.runtimes[0].kind", func(c *Config) { c.Runtimes[0].Kind = "absent-adapter" }},
		{"adapter-cli", "unsupported_cli", "$.runtimes[0].cli", func(c *Config) { c.Runtimes[0].CLI = "absent-cli" }},
		{"model", "model_required", "$.agents[0].model", func(c *Config) { c.Agents[0].Model, c.Agents[0].Effort = "", "" }},
		{"effort", "unsupported_effort", "$.agents[0].effort", func(c *Config) { c.Agents[0].Effort = "invalid-effort" }},
		{"binding", "missing_runtime", "$.agents[0].runtime_id", func(c *Config) { c.Agents[0].RuntimeID = "absent-runtime" }},
		{"owner-transport", "unsupported_owner_runtime", "$.agents[0].owner_runtime_id", func(c *Config) { c.Agents[0].OwnerRuntimeID = "orca" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := fixture()
			cfg.FormatVersion = 2
			cfg.Runtimes[0].DeclaredPurposes = []Purpose{Main}
			cfg.Agents[0].Purposes = []Purpose{IssueManager, Consultation}
			tt.change(&cfg)
			_, err := Validate("env-a", cfg, source())
			requireError(t, err, tt.code, tt.path)
		})
	}
}
