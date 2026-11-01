package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/harness/orca"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner"
	"github.com/GhostFlying/tend/internal/runner/agentcontrol"
	"github.com/GhostFlying/tend/internal/runner/agentexec"
	"github.com/GhostFlying/tend/internal/runner/gitws"
)

func assembleAgentExecutions(ctx context.Context, cfg config, source *agentcontrol.Control, workspaces *gitws.Workspaces, ids protocol.IDGen, now func() time.Time, log *slog.Logger, clients ...*http.Client) (runner.ExecutionResolver, error) {
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
	return agentexec.New(agentexec.Options{CentralOrca: client, ProbeCentral: func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(cfg.ServerURL, "/")+"/runner/v1/native/status", nil)
		if err != nil {
			return err
		}
		req.Header.Set("X-Tend-Host", cfg.HostID)
		req.Header.Set("Authorization", "Bearer "+os.Getenv("TEND_HOST_TOKEN"))
		client := http.DefaultClient
		if len(clients) > 0 && clients[0] != nil {
			client = clients[0]
		}
		response, err := client.Do(req)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		var status struct {
			Enabled bool `json:"enabled"`
		}
		if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&status) != nil || !status.Enabled {
			return errors.New("central native workers are not enabled")
		}
		return nil
	}, Source: source, BackendID: backend, Workspaces: view, BridgeCommand: self, IDs: ids, Now: now, Log: log})
}
