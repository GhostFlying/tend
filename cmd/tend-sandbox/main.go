// Command tend-sandbox serves the real Web client and APIs with isolated scripts.
// It is a development harness, never a release component or a production mode.
//
// Quick start: make sandbox, or make sandbox SANDBOX_DATA=<private-demo-dir>.
// Pass SANDBOX_ARGS=-reset to reseed a verified sandbox directory. For HMR,
// run Vite with TEND_DEV_SERVER pointing to the sandbox's HTTP origin.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/GhostFlying/tend/internal/sandbox"
)

func main() {
	var options sandbox.Options
	flag.StringVar(&options.Listen, "listen", "127.0.0.1:8790", "development HTTP listener")
	flag.StringVar(&options.Data, "data", "", "required empty or sandbox-marked data directory")
	flag.BoolVar(&options.Reset, "reset", false, "wipe and reseed a verified sandbox directory")
	flag.StringVar(&options.Scenario, "scenario", "demo", "sandbox scenario")
	flag.BoolVar(&options.FastPacing, "fast-pacing", false, "near-zero script delays for automated tests")
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := sandbox.Run(ctx, options, log); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("sandbox stopped", "err", err)
		os.Exit(1)
	}
}
