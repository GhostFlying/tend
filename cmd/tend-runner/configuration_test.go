package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/runner/agentcontrol"
)

func TestConfigurationValidatorPreservesServiceIdentity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable fixture")
	}
	dir := t.TempDir()
	cli := filepath.Join(dir, "cli")
	if err := os.WriteFile(cli, []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	before := filepath.Join(dir, "before")
	after := filepath.Join(dir, "after")
	base := config{HostID: "host-one", ServerURL: "https://example.invalid"}
	write := func(p string, c config) {
		t.Helper()
		raw, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(before, base)
	changed := base
	changed.DeliveryObserver = deliveryObserverConfig{Providers: []string{"github"}, GitHubBin: cli}
	write(after, changed)
	if err := checkConfiguration(before, after); err != nil {
		t.Fatal(err)
	}
	changed.HostID = "another-host"
	write(after, changed)
	if err := checkConfiguration(before, after); err == nil {
		t.Fatal("service identity change accepted")
	}
	changed.HostID = base.HostID
	changed.DeliveryObserver.GitHubBin = "gh"
	write(after, changed)
	if err := checkConfiguration(before, after); err == nil {
		t.Fatal("implicit executable accepted")
	}
}

func TestConfigurationMaintenanceAddsAgentInstallations(t *testing.T) {
	dir := t.TempDir()
	artifact := filepath.Join(dir, "artifact")
	contents := []byte("installed provider artifact")
	if err := os.WriteFile(artifact, contents, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(contents)
	pin := agentcontrol.Installation{RuntimeID: "orca", ArtifactID: "orca-install", ExpectedSHA256: "sha256:" + hex.EncodeToString(digest[:]), AdapterID: "orca", AdapterRevision: "1", LocalReference: artifact}
	added := pin
	added.RuntimeID = "orca-codex"
	source := filepath.Join(dir, "agents.json")
	definitions := agentconfig.Config{FormatVersion: 1, Runtimes: []agentconfig.RuntimeBinding{
		{ID: "orca", Revision: 1, Kind: agentconfig.OrcaWorker, CLI: "traex", ExecutableRef: artifact, DeclaredPurposes: []agentconfig.Purpose{agentconfig.WorkerRun}, Enabled: true},
		{ID: "orca-codex", Revision: 1, Kind: agentconfig.OrcaWorker, CLI: "codex", ExecutableRef: artifact, DeclaredPurposes: []agentconfig.Purpose{agentconfig.WorkerRun}, Enabled: true},
	}, Agents: []agentconfig.AgentDefinition{}}
	write := func(path string, value any) {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(source, definitions)
	before := filepath.Join(dir, "before.json")
	after := filepath.Join(dir, "after.json")
	base := config{HostID: "runner-one", ServerURL: "https://example.invalid", Agents: &agentcontrol.Config{SourceFile: source, Installations: []agentcontrol.Installation{pin}}}
	write(before, base)
	candidate := func() config {
		c := base
		a := *base.Agents
		a.Installations = []agentcontrol.Installation{pin, added}
		c.Agents = &a
		return c
	}
	write(after, candidate())
	if err := checkConfiguration(before, after); err != nil {
		t.Fatal(err)
	}
	// Validation never creates an accepted registry or alters the source.
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 4 {
		t.Fatal("validation wrote state", entries, err)
	}
	for _, tc := range []struct {
		name   string
		change func(*config)
	}{
		{"identity", func(c *config) { c.HostID = "runner-two" }},
		{"source", func(c *config) { c.Agents.SourceFile = filepath.Join(dir, "other.json") }},
		{"backend", func(c *config) { c.Agents.Execution = &agentcontrol.ExecutionConfig{WorkspaceBackend: "git"} }},
		{"existing_installation", func(c *config) { c.Agents.Installations[0].AdapterRevision = "2" }},
		{"removed_installation", func(c *config) { c.Agents.Installations = c.Agents.Installations[1:] }},
		{"digest", func(c *config) { c.Agents.Installations[1].ExpectedSHA256 = "sha256:" + strings.Repeat("0", 64) }},
		{"relative_installation", func(c *config) { c.Agents.Installations[1].LocalReference = "artifact" }},
		{"duplicate_runtime", func(c *config) { c.Agents.Installations[1].RuntimeID = "orca" }},
		{"missing_runtime", func(c *config) { c.Agents.Installations[1].RuntimeID = "missing" }},
		{"enable_agent_control", func(c *config) { c.Agents.SourceFile = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := candidate()
			tc.change(&c)
			write(after, c)
			if err := checkConfiguration(before, after); err == nil {
				t.Fatal("unsafe configuration change accepted")
			}
		})
	}
	write(after, candidate())
	definitions.Runtimes[1].Kind = agentconfig.HostedManager
	definitions.Runtimes[1].CLI = ""
	definitions.Runtimes[1].DeclaredPurposes = []agentconfig.Purpose{agentconfig.Main}
	write(source, definitions)
	if err := checkConfiguration(before, after); err == nil {
		t.Fatal("hosted runtime accepted by runner maintenance")
	}
	if err := os.WriteFile(source, []byte("invalid JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkConfiguration(before, after); err == nil {
		t.Fatal("invalid agent definitions accepted")
	}
}
