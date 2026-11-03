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
	DB              string `json:"db"`
	Socket          string `json:"socket"`
	Updater         string `json:"updater"`
	Releases        string `json:"releases"`
	HostedDrained   *bool  `json:"hosted_drained,omitempty"`
	ReceiptRecovery bool   `json:"receipt_recovery,omitempty"`
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
	// A real retained process with no listener/registration reproduces a failed
	// candidate without pretending that its missing DB row ever existed.
	if mode == "unavailable" {
		<-ctx.Done()
		return nil
	}
	db, err := store.Open(ctx, cfg.DB, time.Now)
	if err != nil {
		return err
	}
	defer db.Close()
	ids := protocol.NewULIDGen(time.Now, nil)
	if mode == "legacy" && cfg.ReceiptRecovery {
		if err := db.Write(ctx, func(tx *store.Tx) error {
			var count int
			if err := tx.Q().QueryRowContext(ctx, `SELECT COUNT(*) FROM native_turn_receipts_pending`).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				return nil
			}
			message, request := ids.New(protocol.PrefixMessage), ids.New(protocol.PrefixRequest)
			if _, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: message, ConversationID: "main", Author: "owner", Body: "Retained native receipt fixture."}); err != nil {
				return err
			}
			if err := tx.PutMainTurn(ctx, store.MainTurn{RequestID: request, MessageID: message, ThreadID: "fixture-original-native", TurnID: "fixture-original-turn", Status: store.TurnInterrupted}, 0); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO native_turn_receipts_pending(request_id) VALUES(?)`, request)
			return err
		}); err != nil {
			return err
		}
	}
	opts := []app.Option{}
	recovery := os.Getenv("TEND_FORWARD_RECOVERY_ID")
	if mode == "successor" && recovery != "" {
		opts = append(opts, app.WithForwardRecoveryStartup(recovery))
	}
	maintenance := os.Getenv("TEND_COMMITTED_MAINTENANCE_ID")
	if mode == "successor" && maintenance != "" {
		opts = append(opts, app.WithCommittedMaintenanceStartup(maintenance))
	}
	a := app.New(db, ids, time.Now, opts...)
	actual, err := (updater.LinuxProcess{}).Inspect(ctx, os.Getpid())
	if err != nil {
		return err
	}
	c, err := upgradectl.New(upgradectl.Config{Instance: "fixture-server", ProcessInstance: actual.LaunchID, BinarySHA256: actual.SHA256, ControlSocket: cfg.Socket, UpdaterSocket: cfg.Updater, ReleaseDir: cfg.Releases, Interval: 50 * time.Millisecond, RecoveryReady: func(ctx context.Context) error {
		return db.Read(ctx, func(tx *store.ReadTx) error {
			var schema int64
			return tx.Q().QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&schema)
		})
	}}, a, ids, func(context.Context) (app.ServerObservation, error) {
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
	if mode == "successor" && maintenance != "" {
		if err := c.InitializeCommittedMaintenance(ctx, maintenance); err != nil {
			return err
		}
	}
	archive := upgradectl.ArchiveControl{App: a, ObserveHosted: func(context.Context) (bool, error) {
		if cfg.ReceiptRecovery {
			return false, nil
		}
		return cfg.HostedDrained == nil || *cfg.HostedDrained, nil
	}}
	return c.RunWith(ctx, archive.Handler())
}
