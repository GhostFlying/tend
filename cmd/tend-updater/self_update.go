package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/updater"
	"slices"
)

func runSelfUpdate(ctx context.Context, args []string, out io.Writer) error {
	if len(args) < 1 {
		return errors.New("usage: tend-updater self-update plan|apply|status --config <file> --plan <file>")
	}
	action := args[0]
	if !slices.Contains(protocol.UpdaterSelfUpdateActions(), action) {
		return errors.New("updater: self-update expects plan, apply or status")
	}
	flags := flag.NewFlagSet("self-update "+action, flag.ContinueOnError)
	config := flags.String("config", "", "fixed private updater configuration")
	planFile := flags.String("plan", "", "private original self-update plan (plan replaces it with resolved evidence)")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || *config == "" {
		return errors.New("updater: fixed configuration required")
	}
	cfg, err := load(*config)
	if err != nil {
		return err
	}
	if action == "status" {
		j, err := updater.SelfUpdateJournalStatus(cfg)
		if err != nil {
			return err
		}
		status, observationErr := updater.SelfUpdateCall(ctx, updater.SocketPath(cfg), nil)
		result := map[string]any{"journal": j, "observed": observationErr == nil}
		if observationErr == nil {
			result["observation"] = status.Observation
		} else {
			result["observation_error"] = observationErr.Error()
		}
		return json.NewEncoder(out).Encode(result)
	}
	p, err := updater.ReadSelfUpdatePlan(*planFile)
	// Independent dispatch passes the durable journal rather than another plan
	// file. Read its exact embedded plan without regenerating the request.
	if err != nil && action == "apply" {
		j, e := updater.SelfUpdateJournalStatus(cfg)
		if e == nil && *planFile == cfg.StateDir+"/self-update.json" {
			p, err = j.Plan, nil
		}
	}
	if err != nil {
		return err
	}
	if action == "plan" {
		p.ConfigFile = *config
		p, err = updater.PrepareSelfUpdatePlan(ctx, cfg, p)
		if err != nil {
			return err
		}
		raw, err := json.MarshalIndent(p, "", "  ")
		if err != nil {
			return err
		}
		// Apply always consumes this exact plan. The operator can review stdout and
		// the private file; no process action is issued during plan.
		temp, err := os.CreateTemp(cfg.StateDir, ".self-plan-")
		if err != nil {
			return err
		}
		name := temp.Name()
		defer os.Remove(name)
		if err = temp.Chmod(0o600); err == nil {
			_, err = temp.Write(append(raw, '\n'))
		}
		if err == nil {
			err = temp.Sync()
		}
		closeErr := temp.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		if err = os.Rename(name, *planFile); err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(p)
	}
	if action != "apply" {
		return errors.New("updater: self-update expects plan, apply or status")
	}
	j, err := updater.ApplySelfUpdate(ctx, cfg, p)
	_ = json.NewEncoder(out).Encode(j)
	return err
}
