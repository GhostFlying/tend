// Command tend-server runs tend's control service: the ledger, the main
// agent's front thread, the outbox dispatcher and the runner protocol.
//
// Configuration is a HuJSON file (-config). Secrets come from the
// environment, typically an env file: TEND_SERVER_SECRET derives run
// tokens, TS_AUTHKEY joins the tailnet the first time.
package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/GhostFlying/tend/internal/api"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/cfgfile"
	"github.com/GhostFlying/tend/internal/contextbuilder"
	"github.com/GhostFlying/tend/internal/dispatch"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/traex"
	"github.com/GhostFlying/tend/internal/mainthread"
	"github.com/GhostFlying/tend/internal/netx"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runnerapi"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/tools"
)

// config is the server's configuration file.
type config struct {
	// DataDir holds the ledger and the tailnet state.
	DataDir string `json:"data_dir"`
	// Tailnet joins the tailnet as this node; without it the server
	// listens on Listen, for development.
	Tailnet *struct {
		Hostname  string `json:"hostname"`
		Ephemeral bool   `json:"ephemeral"`
	} `json:"tailnet"`
	Listen string `json:"listen"`
	// Owners are the tailnet logins allowed to use the owner API and web
	// client (D42).
	Owners []string `json:"owners"`
	// LocalOwner is the owner every caller is taken to be when the server
	// listens off the tailnet, for development.
	LocalOwner string           `json:"local_owner"`
	LeaseTTL   cfgfile.Duration `json:"lease_ttl"`
	// MainAgent runs the front thread with a dedicated traex home.
	MainAgent struct {
		Instance  string `json:"instance"`
		TraexBin  string `json:"traex_bin"`
		TraexHome string `json:"traex_home"`
		Model     string `json:"model"`
		// Cwd must be outside any repository, so no project AGENTS.md is
		// loaded into the main agent (spike A1).
		Cwd string `json:"cwd"`
	} `json:"main_agent"`
	Hosts     []app.HostConfig     `json:"hosts"`
	Repos     []app.RepoConfig     `json:"repos"`
	Templates []app.TemplateConfig `json:"templates"`
}

func main() {
	path := flag.String("config", "", "configuration file (HuJSON)")
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, *path, log); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("tend-server stopped", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, path string, log *slog.Logger) error {
	if path == "" {
		return errors.New("-config is required")
	}
	var cfg config
	if err := cfgfile.Load(path, &cfg); err != nil {
		return err
	}
	secret, err := hex.DecodeString(os.Getenv("TEND_SERVER_SECRET"))
	if err != nil || len(secret) < 32 {
		return errors.New("TEND_SERVER_SECRET must hold at least 32 bytes, hex-encoded")
	}
	dataDir, err := cfgfile.Path(cfg.DataDir)
	if err != nil {
		return err
	}
	cwd, err := cfgfile.Path(cfg.MainAgent.Cwd)
	if err != nil {
		return err
	}
	traexHome, err := cfgfile.Path(cfg.MainAgent.TraexHome)
	if err != nil {
		return err
	}
	for _, dir := range []string{dataDir, cwd} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}

	s, err := store.Open(ctx, filepath.Join(dataDir, "tend.db"), time.Now)
	if err != nil {
		return err
	}
	defer s.Close()
	ids := protocol.NewULIDGen(time.Now, nil)
	wake := dispatch.NewSignal()
	opts := []app.Option{app.WithNotifier(wake)}
	if cfg.LeaseTTL > 0 {
		opts = append(opts, app.WithLeaseTTL(time.Duration(cfg.LeaseTTL)))
	}
	a := app.New(s, ids, time.Now, opts...)
	if err := syncConfig(ctx, a, ids, cfg); err != nil {
		return err
	}

	var (
		listener net.Listener
		whois    netx.WhoIs
	)
	owners := cfg.Owners
	if cfg.Tailnet != nil {
		tn, err := netx.JoinTailnet(ctx, netx.TailnetConfig{
			Hostname: cfg.Tailnet.Hostname, Dir: filepath.Join(dataDir, "tsnet"), AuthKey: os.Getenv("TS_AUTHKEY"),
			Ephemeral: cfg.Tailnet.Ephemeral, Logf: func(f string, args ...any) { log.Info(fmt.Sprintf(f, args...)) },
		})
		if err != nil {
			return err
		}
		defer tn.Close()
		if listener, err = tn.Listen(":80"); err != nil {
			return err
		}
		whois = tn
		log.Info("joined the tailnet", "name", tn.DNSName())
	} else {
		if listener, err = net.Listen("tcp", cfg.Listen); err != nil {
			return err
		}
		// Off the tailnet there is no peer identity; development only.
		whois = netx.Static{Identity: &netx.Identity{Node: "local", User: cfg.LocalOwner, Tags: []string{"tag:local"}}}
		if cfg.LocalOwner != "" {
			owners = []string{cfg.LocalOwner}
		}
		log.Warn("listening without the tailnet; every peer is taken as tag:local and the local owner", "addr", listener.Addr().String())
	}

	front := mainthread.New(a, contextbuilder.New(a, s, ids, contextbuilder.Config{}), tools.New(a, s, log), mainthread.Config{
		Instance: cfg.MainAgent.Instance,
		Thread: harness.ThreadConfig{
			Cwd: cwd, Model: cfg.MainAgent.Model, DeveloperInstructions: mainthread.Instructions,
			Sandbox: "danger-full-access", ApprovalPolicy: "never", Config: mainthread.Isolation,
		},
		Open: func(ctx context.Context) (harness.Session, error) {
			return traex.Start(ctx, traex.Options{Bin: cfg.MainAgent.TraexBin, Env: []string{"TRAE_HOME=" + traexHome}, Dir: cwd, Version: "tend", Log: log})
		},
		Log: log,
	})
	d := dispatch.New(a, map[string]dispatch.Handler{app.MainThreadDestination: front}, wake, time.Now, dispatch.Config{}, log)
	runners := runnerapi.New(a, whois, secret, runnerapi.Config{}, log)
	mux := http.NewServeMux()
	mux.Handle(runnerapi.Path, runners.Handler())
	mux.Handle("/api/", api.New(a, s, whois, secret, api.Config{Owners: owners}, log).Handler())
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	// The workers stop with the process or when serving fails.
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	var wg sync.WaitGroup
	wg.Go(func() { _ = front.Run(runCtx) })
	wg.Go(func() { _ = d.Run(runCtx) })
	wg.Go(func() { runners.SweepLeases(runCtx, a.LeaseTTL()/4) })
	wg.Go(func() {
		<-runCtx.Done()
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(runCtx), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	})
	log.Info("tend-server running")
	err = server.Serve(listener)
	stop()
	wg.Wait()
	if errors.Is(err, http.ErrServerClosed) {
		return ctx.Err()
	}
	return err
}

// syncConfig brings hosts, repositories and templates into the ledger.
func syncConfig(ctx context.Context, a *app.App, ids protocol.IDGen, cfg config) error {
	payload, err := json.Marshal(app.ConfigPayload{Hosts: cfg.Hosts, Repos: cfg.Repos, Templates: cfg.Templates})
	if err != nil {
		return err
	}
	env := &protocol.Envelope{RequestID: ids.New(protocol.PrefixRequest), Operation: "config.sync", Target: protocol.Target{Type: "config"}, Payload: payload}
	op, err := a.Execute(ctx, protocol.Principal{Kind: protocol.PrincipalSystem}, env, app.SyncConfig{})
	if err != nil {
		return fmt.Errorf("config sync: %w", err)
	}
	if op.Status == protocol.OpFailed {
		return fmt.Errorf("config sync: %s", op.Error.Message)
	}
	return nil
}
