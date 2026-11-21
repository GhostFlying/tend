package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/GhostFlying/tend/internal/cfgfile"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/runner"
)

// Both invocations run in the runner's credential context without opening its
// workspace, registering a runner, or joining the embedded node.
func rollingCommand(ctx context.Context, action string, args []string, in io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet(action, flag.ContinueOnError)
	configPath := fs.String("config", "", "runner configuration file")
	fs.String("version", "", "retained service version")
	id := fs.String("upgrade-id", "", "existing upgrade")
	dir := fs.String("release", "", "complete release directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *configPath == "" {
		return errors.New("rolling runner requires --config")
	}
	var cfg config
	if err := cfgfile.Load(*configPath, &cfg); err != nil {
		return err
	}
	token := os.Getenv("TEND_HOST_TOKEN")
	if token == "" || cfg.HostID == "" || cfg.ServerURL == "" {
		return errors.New("runner host identity is required")
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if cfg.SideChannel != nil {
		var err error
		client, err = cfg.SideChannel.Client(cfg.ServerURL)
		if err != nil {
			return err
		}
	} else if cfg.Tailnet != nil {
		return errors.New("embedded tailnet requires a rolling side_channel")
	}
	defer client.CloseIdleConnections()
	socket, err := cfgfile.Path(cfg.UpdaterControlSocket)
	if err != nil || socket == "" {
		return errors.New("runner updater control socket is required")
	}
	r := runner.New(runner.Config{HostID: cfg.HostID, ServerURL: cfg.ServerURL, HostToken: token, UpdaterControlSocket: socket, HTTPClient: client, Now: time.Now, IDs: protocol.NewULIDGen(time.Now, nil), Log: slog.Default()})
	if action == "rolling-authorize" {
		var input protocol.RollingAuthorization
		if err := json.NewDecoder(io.LimitReader(in, 64<<10)).Decode(&input); err != nil {
			return err
		}
		grant, err := r.AuthorizeRolling(ctx, input)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(grant)
	}
	if !release.ValidID(*id) || *dir == "" {
		return errors.New("rolling bootstrap requires --upgrade-id and --release")
	}
	state, err := r.BootstrapRolling(ctx, *id, *dir)
	if err != nil {
		return err
	}
	if receipt, ok := state.Terminals[*id]; ok {
		return json.NewEncoder(out).Encode(receipt)
	}
	if state.Operation == nil || state.Operation.ID != *id {
		return errors.New("runner: submitted operation has no durable observation")
	}
	return json.NewEncoder(out).Encode(map[string]any{"host_id": state.InstanceID, "upgrade_id": *id, "phase": state.Operation.Phase, "candidate_instance": state.Operation.LaunchID})
}
