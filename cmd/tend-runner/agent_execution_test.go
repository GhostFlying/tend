package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/agentcontrol"
	"github.com/GhostFlying/tend/internal/runner/gitws"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestNamedWorkspaceBackendIsIndependentFromLegacyWorker(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix bootstrap")
	}
	clock := testkit.NewClock()
	ids := testkit.NewIDGen(clock, 749)
	home := t.TempDir()
	bin := filepath.Join(home, "fake-orca")
	// Assembly probes only the existing read-only workspace CLI contract.
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s\\n' '{\"ok\":true,\"result\":{}}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, backend := range []string{"git", "orca"} {
		t.Run(backend, func(t *testing.T) {
			cfg := config{HostID: "runner-a", Agents: &agentcontrol.Config{SourceFile: filepath.Join(t.TempDir(), "definitions.json"), Execution: &agentcontrol.ExecutionConfig{WorkspaceBackend: backend, OrcaBin: bin}}}
			cfg.Worker.Kind = protocol.WorkerAppServer
			if backend == "git" {
				cfg.Worker.Kind = protocol.WorkerOrca
			}
			original := cfg.Worker.Kind
			registryHome := t.TempDir()
			if err := os.MkdirAll(filepath.Join(registryHome, "runner"), 0700); err != nil {
				t.Fatal(err)
			}
			source, err := assembleAgentControl(t.Context(), registryHome, cfg.HostID, cfg.Agents, clock.Now, ids)
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			coordinator := gitws.New(gitws.Config{Home: t.TempDir()})
			executions, err := assembleAgentExecutions(t.Context(), cfg, source, coordinator, ids, clock.Now, nil)
			if err != nil || executions == nil {
				t.Fatal(executions, err)
			}
			if cfg.Worker.Kind != original {
				t.Fatal("named backend changed the legacy worker")
			}
			if _, err := coordinator.Backend(backend); err != nil {
				t.Fatal(err)
			}
			observations, err := executions.Observe(t.Context())
			if err != nil || len(observations) != 0 {
				t.Fatal("bootstrap implicitly accepted source", observations, err)
			}
		})
	}
}
