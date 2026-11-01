package agentconfig

import (
	"strings"
	"testing"
)

func TestCentralRuntimeIsExplicitAndCoexistsWithLocal(t *testing.T) {
	cfg := fixture()
	cfg.FormatVersion = 2
	cfg.Runtimes[0].Kind = CentralWorker
	cfg.Runtimes[0].CLI = "traex"
	definitions := requireValid(t, "runner", cfg)
	raw, _ := publicBytes(t, definitions)
	if !strings.Contains(string(raw), "central-traex-worker") || !strings.Contains(string(raw), "appserver-worker") {
		t.Fatal("local and central definitions were not retained")
	}
	for _, change := range []func(*Config){func(c *Config) { c.FormatVersion = 1 }, func(c *Config) { c.Runtimes[0].CLI = "codex" }, func(c *Config) { c.Runtimes[0].DeclaredPurposes = []Purpose{IssueManager} }} {
		bad := fixture()
		bad.FormatVersion = 2
		bad.Runtimes[0].Kind = CentralWorker
		bad.Runtimes[0].CLI = "traex"
		change(&bad)
		if _, err := Validate("runner", bad, source()); err == nil {
			t.Fatal("unsupported central contract accepted")
		}
	}
}
