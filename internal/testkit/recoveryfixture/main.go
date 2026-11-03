// recoveryfixture is an isolated service for the opt-in systemd recovery test.
// It has no network credentials, runner transport, models or production defaults.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/updater"
	"github.com/GhostFlying/tend/internal/upgradectl"
)

var mode = "successor"

type fixtureConfig struct {
	DB       string `json:"db"`
	Socket   string `json:"socket"`
	Updater  string `json:"updater"`
	Releases string `json:"releases"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	path := flag.String("config", "", "isolated fixture configuration")
	flag.Parse()
	raw, err := os.ReadFile(*path)
	if err != nil {
		return err
	}
	var cfg fixtureConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer cancel()
	db, err := store.Open(ctx, cfg.DB, time.Now)
	if err != nil {
		return err
	}
	defer db.Close()
	ids := protocol.NewULIDGen(time.Now, nil)
	opts := []app.Option{}
	recovery := os.Getenv("TEND_FORWARD_RECOVERY_ID")
	if mode == "successor" && recovery != "" {
		opts = append(opts, app.WithForwardRecoveryStartup(recovery))
	}
	a := app.New(db, ids, time.Now, opts...)
	actual, err := (updater.LinuxProcess{}).Inspect(ctx, os.Getpid())
	if err != nil {
		return err
	}
	c, err := upgradectl.New(upgradectl.Config{Instance: "fixture-server", ProcessInstance: actual.LaunchID, BinarySHA256: actual.SHA256, ControlSocket: cfg.Socket, UpdaterSocket: cfg.Updater, ReleaseDir: cfg.Releases, Interval: 50 * time.Millisecond, RecoveryReady: func(ctx context.Context) error { _, err := a.CurrentUpgrade(ctx); return err }}, a, ids, func(context.Context) (app.ServerObservation, error) {
		return app.ServerObservation{Healthy: true, MainDrained: false}, nil
	}, nil)
	if err != nil {
		return err
	}
	if mode == "successor" && recovery != "" {
		if err := c.InitializeRecovery(ctx, recovery); err != nil {
			return err
		}
	}
	return c.Run(ctx)
}
