package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/GhostFlying/tend/internal/cfgfile"
	"github.com/GhostFlying/tend/internal/cli"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/agentcontrol"
	"github.com/GhostFlying/tend/internal/runner/agentregistry"
)

func runAgentsCommand(args []string, out, errout io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(errout, "usage: tend-runner agents status|validate|reload|reconcile|register|registration --config <file>")
		return 2
	}
	flags := flag.NewFlagSet("agents "+args[0], flag.ContinueOnError)
	flags.SetOutput(errout)
	path := flags.String("config", "", "runner configuration file")
	requestID := flags.String("request-id", "", "original request ID when retrying or reconciling")
	revisionText := flags.String("revision", "", "original local revision when retrying")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 || *path == "" {
		fmt.Fprintln(errout, "--config is required; use named flags")
		return 2
	}
	if runtime.GOOS == "windows" {
		fmt.Fprintln(errout, "local agent configuration requires a Unix runner")
		return 1
	}
	var revision *uint64
	if *revisionText != "" {
		value, err := strconv.ParseUint(*revisionText, 10, 64)
		if err != nil {
			fmt.Fprintln(errout, "invalid --revision")
			return 2
		}
		revision = &value
	}
	var cfg config
	if err := cfgfile.Load(*path, &cfg); err != nil {
		fmt.Fprintln(errout, "runner bootstrap configuration cannot be read")
		return 1
	}
	if cfg.Agents == nil {
		fmt.Fprintln(errout, "local agent configuration is not enabled in runner config")
		return 1
	}
	if cfg.Home == "" {
		cfg.Home = "~/.tend"
	}
	home, err := cfgfile.Path(cfg.Home)
	if err != nil {
		fmt.Fprintln(errout, "runner home cannot be resolved")
		return 1
	}
	socket, err := repositorySocketPath(home)
	if err != nil {
		fmt.Fprintln(errout, "local runner control is unavailable")
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return cli.RunLocalAgents(ctx, cli.Env{Stdout: out, Stderr: errout, IDs: protocol.NewULIDGen(time.Now, nil)}, socket, args[0], *requestID, revision)
}

// assembleAgentControl resolves bootstrap paths and wires the registry's clock/IDs.
func assembleAgentControl(ctx context.Context, home, host string, cfg *agentcontrol.Config, now func() time.Time, ids protocol.IDGen) (*agentcontrol.Control, error) {
	if cfg == nil {
		return nil, nil
	}
	if runtime.GOOS == "windows" {
		return nil, fmt.Errorf("local agent configuration requires a Unix runner")
	}
	source, err := cfgfile.Path(cfg.SourceFile)
	if err != nil {
		return nil, err
	}
	input := *cfg
	input.SourceFile = source
	return agentcontrol.Open(ctx, agentcontrol.Options{Directory: filepath.Join(home, "runner", "agents"), EnvironmentID: host, Config: input, Registry: agentregistry.Options{Now: now, NewSnapshotID: func() string { return "snapshot-" + ids.New(protocol.PrefixRequest) }}})
}
