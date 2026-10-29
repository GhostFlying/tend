package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/GhostFlying/tend/internal/harness/orca"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner"
	"github.com/GhostFlying/tend/internal/runner/agentcontrol"
	"github.com/GhostFlying/tend/internal/runner/agentexec"
	"github.com/GhostFlying/tend/internal/runner/gitws"
)

func assembleAgentExecutions(ctx context.Context, cfg config, source *agentcontrol.Control, workspaces *gitws.Workspaces, ids protocol.IDGen, now func() time.Time, log *slog.Logger) (runner.ExecutionResolver, error) {
	if cfg.Agents == nil || cfg.Agents.Execution == nil {
		return nil, nil
	}
	if source == nil {
		return nil, errors.New("named execution requires the configured local source")
	}
	backend := cfg.Agents.Execution.WorkspaceBackend
	if backend == "" {
		backend = "git"
		if cfg.Worker.Kind == protocol.WorkerOrca {
			backend = "orca"
		}
	}
	var client *orca.Client
	if backend == "orca" {
		bin := cfg.Agents.Execution.OrcaBin
		if bin == "" {
			bin = cfg.Worker.OrcaBin
		}
		client = &orca.Client{Bin: bin}
		if err := client.CheckWorkspaceProtocol(ctx); err != nil {
			return nil, err
		}
	}
	if err := workspaces.AddBackend(backend, client); err != nil {
		return nil, err
	}
	view, err := workspaces.Backend(backend)
	if err != nil {
		return nil, err
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return agentexec.New(agentexec.Options{Source: source, BackendID: backend, Workspaces: view, BridgeCommand: self, IDs: ids, Now: now, Log: log})
}
