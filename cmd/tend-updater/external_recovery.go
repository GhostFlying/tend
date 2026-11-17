package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"time"

	"github.com/GhostFlying/tend/internal/updater"
)

func runExternalRecovery(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("external-recover", flag.ContinueOnError)
	var request updater.ExternalRecoveryRequest
	flags.StringVar(&request.ConfigFile, "config", "", "fixed updater configuration")
	flags.StringVar(&request.Release, "release", "", "verified complete release directory")
	flags.StringVar(&request.Unit, "unit", "", "exact systemd user service (otherwise match its configuration)")
	flags.StringVar(&request.RequestID, "request-id", "", "stable external recovery identity")
	flags.BoolVar(&request.Force, "force", false, "authorize stopping all processes in the fixed service")
	flags.BoolVar(&request.Status, "status", false, "read the original installer record")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return flag.ErrHelp
	}
	cfg, err := load(request.ConfigFile)
	if err != nil {
		return err
	}
	bounded, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	record, err := updater.ExternalRecovery(bounded, cfg, request)
	if printErr := json.NewEncoder(out).Encode(record); printErr != nil {
		return printErr
	}
	return err
}
