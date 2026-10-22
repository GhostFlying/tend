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
	"slices"
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
	// Owners are the tailnet logins allowed to use the owner API, through
	// the tend CLI and the agents it connects (D49).
	Owners []string `json:"owners"`
	// OwnerNodes maps machines, by name or "tag:", to the owner in Owners
	// whose devices they are, for tagged machines that have no login
	// (D56). Prefer names: a tag may cover machines of others.
	OwnerNodes map[string]string `json:"owner_nodes"`
	// LocalOwner is the owner every caller is taken to be when the server
	// listens off the tailnet, for development.
	LocalOwner string           `json:"local_owner"`
	LeaseTTL   cfgfile.Duration `json:"lease_ttl"`
	// HostLostAfter is how long a host may go unheard before the tasks
	// whose workspace is on it are blocked for the owner (D62); "0s"
	// turns that off, while retired hosts still count.
	HostLostAfter *cfgfile.Duration `json:"host_lost_after"`
	// Queue tunes the order in which queued work is claimed (D60).
	Queue struct {
		// WaitPerHour is what an hour in the queue adds to an action's
		// score; 0 turns waiting off.
		WaitPerHour *float64 `json:"wait_per_hour"`
	} `json:"queue"`
	// MainAgent runs the hosted main agent alongside the agents the owner
	// works in (D68).
	MainAgent *mainAgentConfig     `json:"main_agent"`
	Hosts     []app.HostConfig     `json:"hosts"`
	Repos     []app.RepoConfig     `json:"repos"`
	Templates []app.TemplateConfig `json:"templates"`
	Projects  []app.ProjectConfig  `json:"projects"`
}

// mainAgentConfig configures the hosted main agent (D68). Every field is
// optional.
type mainAgentConfig struct {
	// Instance names the main agent; default "main".
	Instance string `json:"instance"`
	TraexBin string `json:"traex_bin"`
	// TraexHome defaults to the home traex uses for the server's user,
	// so the main agent shares the owner's traex setup.
	TraexHome string `json:"traex_home"`
	Model     string `json:"model"`
	// Cwd defaults to main-agent in the data directory. It must be
	// outside any repository, so no project AGENTS.md is loaded (spike
	// A1); the main agent can write only there.
	Cwd string `json:"cwd"`
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
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
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
	if d := cfg.HostLostAfter; d != nil {
		if *d < 0 || *d > 0 && time.Duration(*d) < app.MinHostLostAfter {
			return fmt.Errorf("host_lost_after must be 0s or at least %s", app.MinHostLostAfter)
		}
		opts = append(opts, app.WithHostLostAfter(time.Duration(*d)))
	}
	if w := cfg.Queue.WaitPerHour; w != nil {
		if *w < 0 {
			return errors.New("queue.wait_per_hour must not be negative")
		}
		opts = append(opts, app.WithWaitPerHour(*w))
	}
	a := app.New(s, ids, time.Now, opts...)
	if err := syncConfig(ctx, a, ids, cfg); err != nil {
		return err
	}

	var (
		// On the tailnet, the same handler serves https on 443 and plain
		// http on 80; WireGuard already encrypts both (§6.4).
		listeners []net.Listener
		whois     netx.WhoIs
		tn        *netx.Tailnet
		secure    bool
	)
	owners := cfg.Owners
	for node, login := range cfg.OwnerNodes {
		if !slices.Contains(owners, login) {
			return fmt.Errorf("owner_nodes: %s belongs to %s, who is not in owners", node, login)
		}
	}
	if cfg.Tailnet != nil {
		tn, err = netx.JoinTailnet(ctx, netx.TailnetConfig{
			Hostname: cfg.Tailnet.Hostname, Dir: filepath.Join(dataDir, "tsnet"), AuthKey: os.Getenv("TS_AUTHKEY"),
			Ephemeral: cfg.Tailnet.Ephemeral, Logf: func(f string, args ...any) { log.Info(fmt.Sprintf(f, args...)) },
		})
		if err != nil {
			return err
		}
		defer tn.Close()
		plain, err := tn.Listen(":80")
		if err != nil {
			return err
		}
		listeners = []net.Listener{plain}
		// Without HTTPS certificates on the tailnet, clients use http.
		if l, err := tn.ListenTLS(":443"); err != nil {
			log.Warn("no https listener; clients must use http", "name", tn.DNSName(), "err", err)
		} else {
			listeners, secure = append(listeners, l), true
		}
		whois = tn
		log.Info("joined the tailnet", "name", tn.DNSName())
	} else {
		l, err := net.Listen("tcp", cfg.Listen)
		if err != nil {
			return err
		}
		listeners = []net.Listener{l}
		// Off the tailnet there is no peer identity; development only.
		whois = netx.Static{Identity: &netx.Identity{Node: "local", User: cfg.LocalOwner, Tags: []string{"tag:local"}}}
		if cfg.LocalOwner != "" {
			owners = []string{cfg.LocalOwner}
		}
		log.Warn("listening without the tailnet; every peer is taken as tag:local and the local owner", "addr", l.Addr().String())
	}

	cb := contextbuilder.New(a, s, ids, contextbuilder.Config{})
	ts := tools.New(a, s, cb, log)
	handlers := map[string]dispatch.Handler{}
	var front *mainthread.Manager
	if cfg.MainAgent != nil {
		if cfg.MainAgent.Instance == "" {
			cfg.MainAgent.Instance = "main"
		}
		if front, err = hostedMainThread(a, cb, ts, *cfg.MainAgent, dataDir, log); err != nil {
			return err
		}
		handlers[app.MainThreadDestination] = front
		log.Info("running the hosted main agent", "instance", cfg.MainAgent.Instance)
	}
	d := dispatch.New(a, handlers, wake, time.Now, dispatch.Config{}, log)
	runners := runnerapi.New(a, whois, secret, runnerapi.Config{}, log)
	mux := http.NewServeMux()
	mux.Handle(runnerapi.Path, runners.Handler())
	mux.Handle("/api/", api.New(a, s, ts, cb, whois, secret, api.Config{Owners: owners, OwnerNodes: cfg.OwnerNodes, HostedMainAgent: front != nil}, log).Handler())
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	// The workers stop with the process or when serving fails.
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	var wg sync.WaitGroup
	if front != nil {
		wg.Go(func() { _ = front.Run(runCtx) })
	}
	wg.Go(func() { _ = d.Run(runCtx) })
	wg.Go(func() { runners.SweepLeases(runCtx, a.LeaseTTL()/4) })
	wg.Go(func() { runners.SweepWorkspaces(runCtx, time.Minute) })
	wg.Go(func() {
		<-runCtx.Done()
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(runCtx), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	})
	if secure {
		wg.Go(func() {
			fetchCtx, cancel := context.WithTimeout(runCtx, time.Minute)
			defer cancel()
			if err := tn.FetchCert(fetchCtx); err != nil && runCtx.Err() == nil {
				log.Warn("no https certificate; clients must use http", "name", tn.DNSName(), "err", err)
			}
		})
	}
	log.Info("tend-server running")
	served := make(chan error, len(listeners))
	for _, l := range listeners {
		go func() { served <- server.Serve(l) }()
	}
	// The first listener to stop stops the server, which ends the others.
	err = <-served
	stop()
	wg.Wait()
	for range len(listeners) - 1 {
		<-served
	}
	if errors.Is(err, http.ErrServerClosed) {
		return ctx.Err()
	}
	return err
}

// hostedMainThread prepares the hosted main agent (D68).
func hostedMainThread(a *app.App, cb *contextbuilder.Builder, ts *tools.Service, cfg mainAgentConfig, dataDir string, log *slog.Logger) (*mainthread.Manager, error) {
	cwd := filepath.Join(dataDir, "main-agent")
	if cfg.Cwd != "" {
		var err error
		if cwd, err = cfgfile.Path(cfg.Cwd); err != nil {
			return nil, err
		}
	}
	// The owner's traex setup may connect tend's add-on (D46); the hosted
	// agent is no add-on session.
	env := []string{protocol.AddonEnv + "=off"}
	if cfg.TraexHome != "" {
		home, err := cfgfile.Path(cfg.TraexHome)
		if err != nil {
			return nil, err
		}
		env = append(env, "TRAE_HOME="+home)
	}
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		return nil, err
	}
	return mainthread.New(a, cb, ts, mainthread.Config{
		Instance: cfg.Instance,
		Thread:   mainthread.ThreadConfig(cwd, cfg.Model),
		Open: func(ctx context.Context) (harness.Session, error) {
			return traex.Start(ctx, traex.Options{Bin: cfg.TraexBin, Env: env, Dir: cwd, Version: "tend", Log: log})
		},
		Log: log,
	}), nil
}

// syncConfig brings hosts, repositories and templates into the ledger.
func syncConfig(ctx context.Context, a *app.App, ids protocol.IDGen, cfg config) error {
	payload, err := json.Marshal(app.ConfigPayload{Hosts: cfg.Hosts, Repos: cfg.Repos, Templates: cfg.Templates, Projects: cfg.Projects})
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
