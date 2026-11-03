// Command tend-server runs the control service using a HuJSON configuration.
// Secrets come from TEND_SERVER_SECRET and, for tailnet joining, TS_AUTHKEY.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/serverkit"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(os.Args) > 1 && os.Args[1] == "archive" {
		if err := runOfflineArchive(ctx, os.Args[2:], os.Stdout, os.Stderr, defaultArchiveDependencies()); err != nil {
			log.Error("offline archive command failed", "err", err)
			os.Exit(1)
		}
		return
	}
	capabilities := flag.Bool("capabilities", false, "print read-only capabilities and the actual executable SHA256 without loading configuration")
	path := flag.String("config", "", "configuration file (HuJSON)")
	flag.Parse()
	if *capabilities {
		report, err := release.CurrentServerCapabilities()
		if err != nil {
			log.Error("capability probe unavailable", "err", err)
			os.Exit(1)
		}
		if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
			os.Exit(1)
		}
		return
	}
	if err := serverkit.RunFile(ctx, *path, log); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("tend-server stopped", "err", err)
		os.Exit(1)
	}
}
