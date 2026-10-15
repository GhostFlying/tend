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
	"syscall"
	"time"

	"github.com/GhostFlying/tend/internal/cfgfile"
	"github.com/GhostFlying/tend/internal/harness/traex"
	"github.com/GhostFlying/tend/internal/netx"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner"
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
	// ServerURL is tend's address, such as http://tend on the tailnet.
	ServerURL string `json:"server_url"`
	HostID    string `json:"host_id"`
	// Home is TEND_HOME: repositories, workspaces and the journal live
	// there. Default ~/.tend.
	Home    string `json:"home"`
	Tailnet *struct {
		Hostname  string `json:"hostname"`
		Ephemeral bool   `json:"ephemeral"`
	} `json:"tailnet"`
	// Worker uses the machine's traex and its own configuration (D38);
	// Model only overrides the default model.
	Worker struct {
		TraexBin string `json:"traex_bin"`
		Model    string `json:"model"`
	} `json:"worker"`
	Git struct {
		Bin string   `json:"bin"`
		Env []string `json:"env"`
	} `json:"git"`
	Repos map[string]struct {
		Reference string             `json:"reference"`
		Profiles  map[string]profile `json:"profiles"`
	} `json:"repos"`
	Profiles map[string]profile `json:"profiles"`
}

func main() {
	path := flag.String("config", "", "configuration file (HuJSON)")
	version := flag.String("version", "dev", "build version reported to tend")
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, *path, *version, log); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("tend-runner stopped", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, path, version string, log *slog.Logger) error {
	if path == "" {
		return errors.New("-config is required")
	}
	var cfg config
	if err := cfgfile.Load(path, &cfg); err != nil {
		return err
	}
	token := os.Getenv("TEND_HOST_TOKEN")
	if token == "" {
		return errors.New("TEND_HOST_TOKEN is required")
	}
	if cfg.Home == "" {
		cfg.Home = "~/.tend"
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
		repos[name] = gitws.Repo{Reference: ref, Profiles: profiles}
	}
	profiles := map[string]gitws.Profile{}
	for pn, p := range cfg.Profiles {
		profiles[pn] = p.gitws()
	}
	r := runner.New(runner.Config{
		ServerURL: cfg.ServerURL, HostID: cfg.HostID, HostToken: token, Version: version, HTTPClient: client, Journal: j,
		Workers:    traex.NewWorkers(traex.WorkerOptions{Session: traex.Options{Bin: cfg.Worker.TraexBin, Version: version, Log: log}, Model: cfg.Worker.Model}),
		Workspaces: gitws.New(gitws.Config{Home: home, Git: cfg.Git.Bin, Env: cfg.Git.Env, Repos: repos, Profiles: profiles, Log: log}),
		Now:        time.Now, IDs: protocol.NewULIDGen(time.Now, nil), Log: log,
	})
	log.Info("tend-runner running", "host", cfg.HostID, "home", home)
	return r.Run(ctx)
}
