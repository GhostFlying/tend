// Package serverkit assembles the control service and its outer boundaries.
package serverkit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
	_ "time/tzdata"

	"github.com/GhostFlying/tend/internal/api"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/archive"
	"github.com/GhostFlying/tend/internal/cfgfile"
	"github.com/GhostFlying/tend/internal/contextbuilder"
	"github.com/GhostFlying/tend/internal/dispatch"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/traex"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/mainthread"
	"github.com/GhostFlying/tend/internal/messaging"
	"github.com/GhostFlying/tend/internal/messaging/telegram"
	"github.com/GhostFlying/tend/internal/netx"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runnerapi"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/tools"
	"github.com/GhostFlying/tend/internal/upgradectl"
	"github.com/GhostFlying/tend/internal/webui"
)

// Config is the server's configuration file.
type Config struct {
	Archives map[string]string `json:"archives,omitempty"`

	// DataDir holds the ledger and the tailnet state.
	DataDir string `json:"data_dir"`
	// Tailnet joins the tailnet as this node; without it the server
	// listens on Listen, for development.
	Tailnet *struct {
		Hostname  string `json:"hostname"`
		Ephemeral bool   `json:"ephemeral"`
	} `json:"tailnet"`
	Listen        string `json:"listen"`
	ControlSocket string `json:"control_socket"`
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
	// Deliverables retains obsolete fields so existing configurations still load.
	Deliverables struct {
		// Deprecated: accepted for compatibility; original files are not size limited.
		FileLimitBytes   int64 `json:"file_limit_bytes"`
		ResultLimitBytes int64 `json:"result_limit_bytes"`
	} `json:"deliverables"`
	// MainAgent runs the hosted main agent alongside the agents the owner
	// works in (D68).
	Upgrades  *UpgradeConfig       `json:"upgrades"`
	Telegram  *TelegramConfig      `json:"telegram,omitempty"`
	MainAgent *MainAgentConfig     `json:"main_agent"`
	Hosts     []app.HostConfig     `json:"hosts"`
	Repos     []app.RepoConfig     `json:"repos"`
	Templates []app.TemplateConfig `json:"templates"`
	Projects  []app.ProjectConfig  `json:"projects"`
}

// TelegramConfig references local credentials; identities are bound through owner CLI.
type TelegramConfig struct {
	TokenEnv string `json:"token_env"`
}

// MainAgentConfig configures the hosted main agent (D68). Every field is
// optional.
type MainAgentConfig struct {
	Named                 *hostedconfig.Configuration `json:"named,omitempty"`
	Recovery              *AgentRecoveryConfig        `json:"recovery"`
	ConversationID        string                      `json:"-"`
	BackgroundConcurrency int                         `json:"background_concurrency"`
	// Instance names the main agent; default "main".
	Instance string `json:"instance"`
	TraexBin string `json:"traex_bin"`
	// TraexHome defaults to the home traex uses for the server's user,
	// so the main agent shares the owner's traex setup.
	TraexHome          string `json:"traex_home"`
	Model              string `json:"model"`
	SummaryIdleMinutes *int64 `json:"summary_idle_minutes"`
	// Cwd defaults to main-agent in the data directory. It must be
	// outside any repository, so no project AGENTS.md is loaded (spike
	// A1); the main agent can write only there.
	Cwd string `json:"cwd"`
}

func serverHandler(runners, owner http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle(runnerapi.Prefix, runners)
	mux.Handle("/api/", owner)
	// The web client, on the same origin as the owner API (D70).
	mux.Handle("/", webui.Handler())
	return mux
}

func RunFile(ctx context.Context, path string, log *slog.Logger) error {
	if path == "" {
		return errors.New("-config is required")
	}
	var cfg Config
	if err := cfgfile.Load(path, &cfg); err != nil {
		return err
	}
	if cfg.MainAgent != nil && cfg.MainAgent.Named == nil {
		return errors.New("main_agent requires an explicit named configuration; omit main_agent for owner-only service")
	}
	secret, err := hex.DecodeString(os.Getenv("TEND_SERVER_SECRET"))
	if err != nil || len(secret) < 32 {
		return errors.New("TEND_SERVER_SECRET must hold at least 32 bytes, hex-encoded")
	}
	return Run(ctx, cfg, secret, Dependencies{}, log)
}

// Run assembles and serves the control service with explicit outer boundaries.
func Run(ctx context.Context, cfg Config, secret []byte, deps Dependencies, log *slog.Logger) error {
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	dataDir, err := cfgfile.Path(cfg.DataDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	if err := prepareUpgradeConfig(cfg.Upgrades, dataDir); err != nil {
		return err
	}

	s, err := store.Open(ctx, filepath.Join(dataDir, "tend.db"), now)
	if err != nil {
		return err
	}
	defer s.Close()
	var ids protocol.IDGen = protocol.NewULIDGen(now, nil)
	if deps.IDs != nil {
		ids = deps.IDs
	}
	wake := dispatch.NewSignal()
	readWake := dispatch.NewSignal()
	opts := []app.Option{app.WithNotifier(dispatch.Fanout{wake, readWake})}
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
	observationOption, err := deploymentObservation(cfg.Upgrades, ids)
	if err != nil {
		return err
	}
	opts = append(opts, observationOption)
	opts = append(opts, app.WithFollowups(cfg.MainAgent != nil))
	if cfg.MainAgent != nil && cfg.MainAgent.Recovery != nil {
		policy, err := cfg.MainAgent.Recovery.policy()
		if err != nil {
			return err
		}
		opts = append(opts, app.WithAgentRecoveryPolicy(policy))
	}
	var named *hostedconfig.Prepared
	if cfg.MainAgent != nil && cfg.MainAgent.Named != nil {
		named, err = hostedconfig.Prepare(*cfg.MainAgent.Named)
		if err != nil {
			return err
		}
		opts = append(opts, app.WithHostedConfiguration(named))
	}
	a := app.New(s, ids, now, opts...)
	if err := syncConfig(ctx, a, ids, cfg); err != nil {
		return err
	}
	if deps.Configure != nil {
		if err := deps.Configure(ctx, a, ids); err != nil {
			return err
		}
	}
	if named != nil {
		if err := syncHostedConfiguration(ctx, a, ids, named); err != nil {
			return err
		}
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
	if deps.Listeners != nil {
		listeners, whois, secure = deps.Listeners, deps.WhoIs, deps.Secure
	} else if cfg.Tailnet != nil {
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

	defer func() {
		for _, listener := range listeners {
			_ = listener.Close()
		}
	}()

	cb := contextbuilder.New(a, s, ids, contextbuilder.Config{})
	ts := tools.New(a, s, cb, log)
	handlers := map[string]dispatch.Handler{}
	var front *mainthread.Manager
	var background *mainthread.BackgroundPool
	if cfg.MainAgent != nil {
		if cfg.MainAgent.Instance == "" {
			cfg.MainAgent.Instance = "main"
		}
		if front, err = hostedMainThread(a, cb, ts, *cfg.MainAgent, dataDir, now, deps.OpenSession, log); err != nil {
			return err
		}
		handlers[app.MainThreadDestination] = front
		handlers[app.MainThreadControlDestination] = front.Control()
		log.Info("running the hosted main agent", "instance", cfg.MainAgent.Instance)
	}
	if cfg.MainAgent != nil {
		background = &mainthread.BackgroundPool{App: a, Front: front, Limit: cfg.MainAgent.BackgroundConcurrency, New: func(id string) (*mainthread.Manager, error) {
			c := *cfg.MainAgent
			c.Instance = cfg.MainAgent.Instance + "/" + id
			c.ConversationID = id
			return hostedMainThread(a, cb, ts, c, dataDir, now, deps.OpenSession, log)
		}}
		handlers[app.MainThreadControlDestination] = background
	}
	var telegramClient TelegramClient
	if cfg.Telegram != nil {
		if cfg.Telegram.TokenEnv == "" {
			return errors.New("telegram.token_env is required")
		}
		open := deps.OpenTelegram
		if open == nil {
			open = func(a *app.App, token string, log *slog.Logger) (TelegramClient, error) {
				return telegram.New(a, token, nil, "", log)
			}
		}
		telegramClient, err = open(a, os.Getenv(cfg.Telegram.TokenEnv), log)
		if err != nil {
			return err
		}
		prepareCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err = telegramClient.Prepare(prepareCtx)
		cancel()
		if err != nil {
			return err
		}
		handlers[app.MessagingDestination] = telegramClient
	} else if err = a.DisableTelegramRuntime(ctx); err != nil {
		return err
	}
	readDispatch := dispatch.New(a, map[string]dispatch.Handler{app.MessagingReadDestination: messaging.ReadHandler{App: a}}, readWake, now, dispatch.Config{}, log)
	d := dispatch.New(a, handlers, wake, now, dispatch.Config{}, log)
	releaseDir := ""
	if cfg.Upgrades != nil {
		releaseDir = cfg.Upgrades.ReleaseDir
	}
	runners := runnerapi.New(a, whois, secret, runnerapi.Config{ReleaseDir: releaseDir}, log)
	upgradeController, err := prepareUpgradeController(cfg.Upgrades, a, front, background, ids, log)
	if err != nil {
		return err
	}
	archives, closeArchives, err := prepareArchives(ctx, cfg.Archives, log, archive.Open)
	if err != nil {
		return err
	}
	defer closeArchives()
	localSocket, err := archiveControlSocket(cfg.ControlSocket, cfg.Upgrades, dataDir)
	if err != nil {
		return err
	}
	archiveControl := upgradectl.ArchiveControl{App: a, ObserveHosted: func(ctx context.Context) (bool, error) {
		if front != nil {
			ready, err := front.ArchiveDrainStatus(ctx)
			if err != nil || !ready {
				return false, err
			}
		}
		if background != nil {
			return background.ArchiveDrainStatus(ctx)
		}
		return true, nil
	}}
	ownerConfig := api.Config{Archives: archives, Owners: owners, OwnerNodes: cfg.OwnerNodes, HostedMainAgent: front != nil}
	if front != nil {
		ownerConfig.ConversationSettings = front.Settings
		ownerConfig.ConversationHandoff = front.HandoffStatus
	}
	owner := api.New(a, s, whois, secret, ownerConfig, log)
	server := &http.Server{Handler: serverHandler(runners.Handler(), owner.Handler()), ReadHeaderTimeout: 10 * time.Second}
	// Shutdown waits for requests to finish; event streams never do.
	server.RegisterOnShutdown(owner.EndStreams)

	runtime := &Runtime{App: a, Store: s, IDs: ids, Tools: ts, Listeners: listeners}
	if deps.Initialize != nil {
		if err := deps.Initialize(ctx, runtime); err != nil {
			return err
		}
	}

	// The workers stop with the process or when serving fails.
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	var wg sync.WaitGroup
	backgroundErrors := make(chan error, 1)
	if deps.Background != nil {
		wg.Go(func() {
			if err := deps.Background(runCtx, runtime); err != nil && runCtx.Err() == nil {
				log.Error("injected background stopped", "err", err)
				backgroundErrors <- err
				stop()
			}
		})
	}
	if telegramClient != nil {
		wg.Go(func() { telegramClient.Run(runCtx) })
	}
	wg.Go(func() { a.RunIssueStatusNotifications(runCtx, log) })
	if localSocket != "" {
		wg.Go(func() {
			var err error
			if upgradeController != nil {
				err = upgradeController.RunWith(runCtx, archiveControl.Handler())
			} else {
				err = upgradectl.ServeLocal(runCtx, localSocket, archiveControl.Handler())
			}
			if err != nil && runCtx.Err() == nil {
				log.Error("local server control stopped", "err", err)
				stop()
			}
		})
	}
	if front != nil {
		wg.Go(func() { _ = front.Run(runCtx) })
		wg.Go(func() { a.RunFollowups(runCtx, log) })
	}
	if background != nil {
		wg.Go(func() { background.Run(runCtx) })
	}
	wg.Go(func() { _ = d.Run(runCtx) })
	wg.Go(func() { _ = readDispatch.Run(runCtx) })
	wg.Go(func() { runners.SweepLeases(runCtx, a.LeaseTTL()/4) })
	wg.Go(func() { runners.SweepWorkspaces(runCtx, time.Minute) })
	shutdownResult := make(chan error, 1)
	wg.Go(func() {
		<-runCtx.Done()
		shutdownResult <- shutdownHTTP(context.WithoutCancel(runCtx), server, 10*time.Second, log)
	})
	if secure && tn != nil {
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
	shutdownErr := <-shutdownResult
	select {
	case backgroundErr := <-backgroundErrors:
		shutdownErr = errors.Join(shutdownErr, backgroundErr)
	default:
	}
	for range len(listeners) - 1 {
		<-served
	}
	if errors.Is(err, http.ErrServerClosed) {
		if shutdownErr != nil {
			return shutdownErr
		}
		return ctx.Err()
	}
	return errors.Join(err, shutdownErr)
}

// hostedMainThread prepares the hosted main agent (D68).
func hostedMainThread(a *app.App, cb *contextbuilder.Builder, ts *tools.Service, cfg MainAgentConfig, dataDir string, now func() time.Time, open SessionOpener, log *slog.Logger) (*mainthread.Manager, error) {
	idle, err := summaryIdleMinutes(cfg.SummaryIdleMinutes)
	if err != nil {
		return nil, err
	}
	cwd, err := mainAgentWorkingDirectory(cfg, dataDir)
	if err != nil {
		return nil, err
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
		Instance:           cfg.Instance,
		ConversationID:     cfg.ConversationID,
		MaintenanceEnabled: cfg.ConversationID == "",
		SummaryIdleMinutes: idle,
		Now:                now,
		Thread:             mainthread.ThreadConfig(cwd, cfg.Model),
		OpenNamed: func(ctx context.Context, selected hostedconfig.Selection) (harness.Session, error) {
			if open != nil {
				return open(ctx, SessionSpec{Config: cfg, Selection: &selected, Cwd: cwd, Env: env})
			}
			if selected.RunnerManaged {
				if selected.RunnerRunID == "" {
					return nil, errors.New("manager awaits its runner claim")
				}
				return mainthread.RunnerSession(ctx, a, selected.RunnerRunID), nil
			}
			bin, err := hostedExecutable(selected.Runtime.ExecutableRef)
			if err != nil {
				return nil, err
			}
			return traex.Start(ctx, traex.Options{Bin: bin, Env: env, Dir: cwd, Version: "tend", Log: log, HostedCoordination: true})
		},
		Open: func(ctx context.Context) (harness.Session, error) {
			if open != nil {
				return open(ctx, SessionSpec{Config: cfg, Cwd: cwd, Env: env})
			}
			return traex.Start(ctx, traex.Options{Bin: cfg.TraexBin, Env: env, Dir: cwd, Version: "tend", Log: log})
		},
		Log: log,
	}), nil
}

// mainAgentWorkingDirectory keeps auto-mode writable directories independent
// across logical conversations, including when the main agent has a custom cwd.
func mainAgentWorkingDirectory(cfg MainAgentConfig, dataDir string) (string, error) {
	if cfg.ConversationID != "" && cfg.ConversationID != "main" {
		key := sha256.Sum256([]byte(cfg.ConversationID))
		return filepath.Join(dataDir, "background-agents", fmt.Sprintf("%x", key)), nil
	}
	if cfg.Cwd != "" {
		return cfgfile.Path(cfg.Cwd)
	}
	return filepath.Join(dataDir, "main-agent"), nil
}

// syncConfig brings hosts, repositories and templates into the ledger.
func syncConfig(ctx context.Context, a *app.App, ids protocol.IDGen, cfg Config) error {
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
