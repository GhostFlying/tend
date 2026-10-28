// Command tend-runner executes tend's runs on one host: it pulls work,
// lays out git workspaces and drives the machine's own traex as workers.
//
// Configuration is a HuJSON file (-config). Secrets come from the
// environment, typically an env file: TEND_HOST_TOKEN authenticates the
// host, TS_AUTHKEY joins the tailnet the first time.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/GhostFlying/tend/internal/cfgfile"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/bridge"
	"github.com/GhostFlying/tend/internal/harness/orca"
	"github.com/GhostFlying/tend/internal/harness/traex"
	"github.com/GhostFlying/tend/internal/netx"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner"
	"github.com/GhostFlying/tend/internal/runner/agentcontrol"
	"github.com/GhostFlying/tend/internal/runner/gitws"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

// profile is a prepare step as written in the configuration.
type profile struct {
	Commands       [][]string       `json:"commands"`
	RerunIfChanged []string         `json:"rerun_if_changed"`
	Timeout        cfgfile.Duration `json:"timeout"`
}

func (p profile) gitws() gitws.Profile {
	return gitws.Profile{Commands: p.Commands, RerunIfChanged: p.RerunIfChanged, Timeout: time.Duration(p.Timeout)}
}

// config is the runner's configuration file.
type config struct {
	Agents           *agentcontrol.Config   `json:"agents,omitempty"`
	DeliveryObserver deliveryObserverConfig `json:"delivery_observer"`
	// ServerURL is tend's address, such as http://tend on the tailnet.
	ServerURL            string `json:"server_url"`
	HostID               string `json:"host_id"`
	UpdaterSocket        string `json:"updater_socket"`
	UpdaterControlSocket string `json:"updater_control_socket"`
	UpdaterBundleDir     string `json:"updater_bundle_dir"`
	// Home is TEND_HOME: repositories, workspaces and the journal live
	// there. Default ~/.tend.
	Home    string `json:"home"`
	Tailnet *struct {
		Hostname  string `json:"hostname"`
		Ephemeral bool   `json:"ephemeral"`
	} `json:"tailnet"`
	// Worker uses the machine's agent and its own configuration (D38);
	// Model and Effort only override the defaults. Kind must be the
	// worker the server's configuration names for this host (D64);
	// default appserver, which runs traex's app-server. orca runs Agent,
	// codex or traex, as a TUI in orca (D66); OrcaBin defaults to orca on
	// PATH.
	Worker struct {
		Acceptance       *protocol.ExecutionProfile `json:"acceptance,omitempty"`
		Kind             protocol.WorkerKind        `json:"kind"`
		TraexBin         string                     `json:"traex_bin"`
		Agent            protocol.AgentKind         `json:"agent"`
		OrcaBin          string                     `json:"orca_bin"`
		Model            string                     `json:"model"`
		Effort           string                     `json:"effort"`
		RecoveryAttempts *int                       `json:"recovery_attempts,omitempty"`
		StartupAttempts  *int                       `json:"startup_attempts,omitempty"`
	} `json:"worker"`
	Git struct {
		Bin string   `json:"bin"`
		Env []string `json:"env"`
	} `json:"git"`
	Repos map[string]struct {
		Reference string             `json:"reference"`
		Profiles  map[string]profile `json:"profiles"`
		// Orca selects the repository registered in orca, such as
		// name:<name>; the orca worker needs it (D65).
		Orca string `json:"orca"`
	} `json:"repos"`
	Profiles map[string]profile `json:"profiles"`
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "agents" {
		os.Exit(runAgentsCommand(os.Args[2:], os.Stdout, os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "repo" {
		os.Exit(runRepositoryCommand(os.Args[2:]))
	}
	if len(os.Args) == 4 && os.Args[1] == "check-config" {
		if err := checkConfiguration(os.Args[2], os.Args[3]); err != nil {
			fmt.Fprintln(os.Stderr, "runner configuration maintenance validation failed")
			os.Exit(1)
		}
		fmt.Println("configuration parsed and provider executables resolved")
		return
	}
	if len(os.Args) > 1 && os.Args[1] == workerMCP {
		os.Exit(runWorkerMCP(os.Args[2:]))
	}
	path := flag.String("config", "", "configuration file (HuJSON)")
	version := flag.String("version", "dev", "build version reported to tend")
	flag.Parse()
	// Managed releases retain their original argv, so the updater's selected
	// immutable release is authoritative over an older --version argument.
	if releaseID := os.Getenv("TEND_UPDATER_RELEASE_ID"); releaseID != "" {
		*version = releaseID
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, *path, *version, log); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("tend-runner stopped", "err", err)
		os.Exit(1)
	}
}

// workerMCP is the subcommand a TUI worker's agent starts as its MCP
// server (D66).
const workerMCP = "worker-mcp"

// runWorkerMCP serves the run's tools to the agent on stdio:
// `tend-runner worker-mcp --state <file> --run <run>`.
func runWorkerMCP(args []string) int {
	fs := flag.NewFlagSet(workerMCP, flag.ContinueOnError)
	state := fs.String("state", "", "the run's state file, written by tend-runner")
	run := fs.String("run", "", "the run the agent was started for")
	if err := fs.Parse(args); err != nil || *state == "" || *run == "" || fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "usage: tend-runner worker-mcp --state <file> --run <run>")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := bridge.ServeMCP(ctx, *state, *run, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "tend-runner worker-mcp: %v\n", err)
		return 1
	}
	return 0
}

func run(ctx context.Context, path, version string, log *slog.Logger) error {
	if path == "" {
		return errors.New("-config is required")
	}
	var cfg config
	configurationSHA, err := cfgfile.LoadDigest(path, &cfg)
	if err != nil {
		return err
	}
	if profile := cfg.Worker.Acceptance; profile != nil {
		if err := profile.Validate(); err != nil {
			return fmt.Errorf("worker.acceptance: %w", err)
		}
	}
	if cfg.Worker.StartupAttempts != nil && *cfg.Worker.StartupAttempts < 0 {
		return errors.New("worker.startup_attempts must be non-negative")
	}
	if cfg.Worker.RecoveryAttempts != nil && *cfg.Worker.RecoveryAttempts < 0 {
		return errors.New("worker.recovery_attempts must be non-negative")
	}
	token := os.Getenv("TEND_HOST_TOKEN")
	if token == "" {
		return errors.New("TEND_HOST_TOKEN is required")
	}
	if cfg.Home == "" {
		cfg.Home = "~/.tend"
	}
	if cfg.Worker.Kind == "" {
		cfg.Worker.Kind = protocol.WorkerAppServer
	}
	if !cfg.Worker.Kind.Valid() {
		return fmt.Errorf("worker kind %q is not supported by this runner", cfg.Worker.Kind)
	}
	home, err := cfgfile.Path(cfg.Home)
	if err != nil {
		return err
	}
	state := filepath.Join(home, "runner")
	if err := os.MkdirAll(state, 0o700); err != nil {
		return err
	}

	client := http.DefaultClient
	if cfg.Tailnet != nil {
		tn, err := netx.JoinTailnet(ctx, netx.TailnetConfig{
			Hostname: cfg.Tailnet.Hostname, Dir: filepath.Join(state, "tsnet"), AuthKey: os.Getenv("TS_AUTHKEY"),
			Ephemeral: cfg.Tailnet.Ephemeral, Logf: func(f string, args ...any) { log.Info(fmt.Sprintf(f, args...)) },
		})
		if err != nil {
			return err
		}
		defer tn.Close()
		client = tn.HTTPClient()
		log.Info("joined the tailnet", "name", tn.DNSName())
	}

	j, err := journal.Open(ctx, filepath.Join(state, "journal.db"), time.Now)
	if err != nil {
		return err
	}
	defer j.Close()
	repos := map[string]gitws.Repo{}
	for name, r := range cfg.Repos {
		ref, err := cfgfile.Path(r.Reference)
		if err != nil {
			return err
		}
		profiles := map[string]gitws.Profile{}
		for pn, p := range r.Profiles {
			profiles[pn] = p.gitws()
		}
		repos[name] = gitws.Repo{Reference: ref, Profiles: profiles, Orca: r.Orca}
	}
	profiles := map[string]gitws.Profile{}
	for pn, p := range cfg.Profiles {
		profiles[pn] = p.gitws()
	}
	ids := protocol.NewULIDGen(time.Now, nil)
	wsCfg := gitws.Config{Home: home, ServerURL: cfg.ServerURL, Git: cfg.Git.Bin, Env: cfg.Git.Env, Repos: repos, Profiles: profiles, Log: log}
	var (
		workers    harness.Workers
		workspaces *gitws.Workspaces
	)
	switch cfg.Worker.Kind {
	case protocol.WorkerOrca:
		if cfg.Worker.Agent != protocol.AgentCodex && cfg.Worker.Agent != protocol.AgentTraex {
			return fmt.Errorf("worker.agent must be codex or traex for the orca worker, not %q", cfg.Worker.Agent)
		}
		for name, r := range repos {
			if r.Orca == "" {
				return fmt.Errorf("repos.%s.orca is required for the orca worker", name)
			}
		}
		self, err := os.Executable()
		if err != nil {
			return err
		}
		oc := &orca.Client{Bin: cfg.Worker.OrcaBin}
		preflightCtx, cancelPreflight := context.WithTimeout(ctx, 30*time.Second)
		preflightErr := oc.CheckWorkspaceProtocol(preflightCtx)
		cancelPreflight()
		if preflightErr != nil {
			return fmt.Errorf("upgrade worker.orca_bin to an Orca CLI supporting folder-workspace and project-group commands before enabling workspace protocol 1: %w", preflightErr)
		}
		workers = orca.NewWorkers(orca.WorkerOptions{
			Client: oc, Agent: cfg.Worker.Agent, Model: cfg.Worker.Model, Effort: cfg.Worker.Effort, Acceptance: cfg.Worker.Acceptance, RecoveryAttempts: cfg.Worker.RecoveryAttempts, StartupAttempts: cfg.Worker.StartupAttempts,
			BridgeCommand: self, BridgeArgs: []string{workerMCP}, IDs: ids, Now: time.Now, Log: log,
		})
		workspaces = gitws.NewOrca(wsCfg, oc)
	default:
		workers = traex.NewWorkers(traex.WorkerOptions{Session: traex.Options{Bin: cfg.Worker.TraexBin, Version: version, Log: log}, Model: cfg.Worker.Model, Effort: cfg.Worker.Effort, Acceptance: cfg.Worker.Acceptance})
		workspaces = gitws.New(wsCfg)
	}
	agents, err := assembleAgentControl(ctx, home, cfg.HostID, cfg.Agents, time.Now, ids)
	if err != nil {
		return err
	}
	if agents != nil {
		defer agents.Close()
	}
	var agentInventory func(context.Context) (protocol.LocalAgentConfiguration, error)
	var agentChanges <-chan struct{}
	if agents != nil {
		agentInventory, agentChanges = agents.Status, agents.Changes()
	}
	var repositoryBindings func() []protocol.RunnerRepository
	if runtime.GOOS != "windows" {
		repositoryBindings = workspaces.RepositoryBindings
	}
	build, err := runnerBuild(version)
	if err != nil {
		return err
	}
	instanceID := os.Getenv("TEND_UPDATER_PROCESS_INSTANCE_ID")
	upgradeID := os.Getenv("TEND_UPGRADE_ID")
	initialMaintenance := os.Getenv("TEND_UPDATER_MAINTENANCE") == "1"
	updaterInstance := os.Getenv("TEND_UPDATER_INSTANCE_ID")
	if updaterInstance == "" {
		updaterInstance = cfg.HostID
	}
	if initialMaintenance && (instanceID == "" || upgradeID == "" || os.Getenv("TEND_RELEASE_SHA256") != build.BinarySHA256) {
		return errors.New("updater candidate identity or executable digest mismatch")
	}
	for _, field := range []*string{&cfg.UpdaterSocket, &cfg.UpdaterControlSocket, &cfg.UpdaterBundleDir} {
		if *field != "" {
			value, err := cfgfile.Path(*field)
			if err != nil {
				return err
			}
			*field = value
		}
	}
	if (cfg.UpdaterSocket != "" || cfg.UpdaterControlSocket != "" || cfg.UpdaterBundleDir != "") && (cfg.UpdaterSocket == "" || cfg.UpdaterControlSocket == "" || cfg.UpdaterBundleDir == "") {
		return errors.New("updater bridge, control socket and bundle directory must be configured together")
	}
	observer, providers, err := cfg.DeliveryObserver.build()
	if err != nil {
		return err
	}
	r := runner.New(runner.Config{
		AgentInventory: agentInventory, AgentChanges: agentChanges, RepositoryBindings: repositoryBindings, ConfigurationSHA256: configurationSHA, DeliveryObserver: observer, DeliveryProviders: providers,
		ServerURL: cfg.ServerURL, HostID: cfg.HostID, HostToken: token, Version: version, HTTPClient: client, Journal: j,
		InstanceID: instanceID, UpgradeID: upgradeID, InitialMaintenance: initialMaintenance, UpdaterInstanceID: updaterInstance, UpdaterSocket: cfg.UpdaterSocket, UpdaterControlSocket: cfg.UpdaterControlSocket, UpdaterBundleDir: cfg.UpdaterBundleDir, Build: build, Worker: cfg.Worker.Kind, Workers: workers, Workspaces: workspaces,
		Now: time.Now, IDs: ids, Log: log,
	})
	if runtime.GOOS != "windows" {
		var agentHandler http.Handler
		if agents != nil {
			agentHandler = r.AgentInventoryHandler(agents.Handler())
		}
		closeRepositoryControl, err := startRepositoryControl(ctx, home, workspaces, agentHandler)
		if err != nil {
			return err
		}
		defer closeRepositoryControl()
	}
	log.Info("tend-runner running", "host", cfg.HostID, "home", home)
	return r.Run(ctx)
}
