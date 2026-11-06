package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/updater"
)

type boundedExecutor func(context.Context, updater.Config, string, string, string, string, uint64, string) (any, error)

func runBounded(ctx context.Context, args []string, out io.Writer) error {
	return runBoundedWith(ctx, args, out, updater.BoundedCommand)
}

func runBoundedWith(ctx context.Context, args []string, out io.Writer, execute boundedExecutor) error {
	if len(args) == 0 || !slices.Contains(protocol.BoundedMaintenanceActions(), args[0]) {
		return errors.New("updater: bounded expects plan, apply, force, extend, cancel, status, reconcile or takeover")
	}
	action := args[0]
	flags := flag.NewFlagSet("tend-updater bounded "+action, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	config := flags.String("config", "", "fixed private configuration")
	plan := flags.String("plan", "", "complete source plan")
	authorization := flags.String("authorization", "", "independent typed bounded authorization")
	request := flags.String("request-id", "", "original stable request identity")
	generation := flags.Uint64("expected-generation", 0, "original durable generation")
	phase := flags.String("expected-phase", "", "original durable phase")
	readOnly := action == "status" || action == "reconcile"
	defaultTimeout := 370 * time.Second
	if readOnly {
		defaultTimeout = 10 * time.Second
	}
	timeout := flags.Duration("timeout", defaultTimeout, "finite outer command deadline")
	flags.Bool("json", false, "print the complete durable result")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	provided := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { provided[f.Name] = true })
	if flags.NArg() != 0 || !filepath.IsAbs(*config) || *timeout <= 0 || *timeout > 24*time.Hour {
		return errors.New("updater: bounded requires an absolute config, no positional arguments and a finite positive timeout at most 24h")
	}
	commandContext, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	if err := commandContext.Err(); err != nil {
		return err
	}
	if readOnly && (provided["plan"] || provided["authorization"] || provided["expected-generation"] || provided["expected-phase"]) {
		return errors.New("updater: bounded status/reconcile take no plan, authorization or CAS")
	}
	if *request != "" && !protocol.ValidID(protocol.PrefixRequest, *request) {
		return errors.New("updater: malformed bounded request identity")
	}
	mutating := !readOnly && action != "plan"
	if !readOnly {
		if !filepath.IsAbs(*plan) || !filepath.IsAbs(*authorization) {
			return errors.New("updater: bounded plan/mutation requires absolute plan and authorization files")
		}
		auth, err := readBoundedAuthorization(*authorization, mutating)
		if err != nil {
			return err
		}
		if !protocol.ValidID(protocol.PrefixRequest, auth.RequestID) || !slices.Contains([]string{"server", "runner"}, auth.Role) || auth.Reason == "" || auth.ExpiresAtMillis <= 0 {
			return errors.New("updater: bounded authorization requires a stable request, role, reason and expiry")
		}
		if provided["request-id"] && *request != auth.RequestID || provided["expected-generation"] && *generation != auth.ExpectedGeneration || provided["expected-phase"] && *phase != auth.ExpectedPhase {
			return errors.New("updater: bounded flags conflict with the original authorization")
		}
		*request, *generation, *phase = auth.RequestID, auth.ExpectedGeneration, auth.ExpectedPhase
		digest, digestErr := hex.DecodeString(auth.PlanSHA256)
		if mutating && (*phase == "" || digestErr != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != auth.PlanSHA256) {
			return errors.New("updater: bounded mutation requires complete generation, phase and plan digest")
		}
	}
	cfg, err := load(*config)
	if err != nil {
		return err
	}
	if execute == nil {
		return errors.New("updater: bounded installer execution unavailable")
	}
	result, err := execute(commandContext, cfg, action, *plan, *authorization, *request, *generation, *phase)
	if result == nil && commandContext.Err() != nil {
		outcome := "observation_unavailable"
		if mutating {
			outcome = "unknown"
		}
		result = map[string]any{"outcome": outcome, "action": action, "request_id": *request, "expected_generation": *generation, "expected_phase": *phase,
			"next_action": "Read bounded status and reconcile the original request before repeating it; managed service exit is not established."}
	}
	if result != nil {
		if printErr := json.NewEncoder(out).Encode(result); printErr != nil {
			return printErr
		}
	} else if err == nil {
		return errors.New("updater: bounded installer returned no durable result")
	}
	if commandContext.Err() != nil {
		return fmt.Errorf("updater: bounded command deadline ended; preserve the original request and authorization: %w", errors.Join(commandContext.Err(), err))
	}
	return err
}

func readBoundedAuthorization(path string, requireGeneration bool) (protocol.BoundedMaintenanceAuthorization, error) {
	var auth protocol.BoundedMaintenanceAuthorization
	f, err := os.Open(path)
	if err != nil {
		return auth, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return auth, err
	}
	if !info.Mode().IsRegular() || info.Size() > 4<<20 {
		return auth, errors.New("updater: bounded authorization must be a regular file at most 4 MiB")
	}
	raw, err := io.ReadAll(io.LimitReader(f, (4<<20)+1))
	if err != nil {
		return auth, err
	}
	if len(raw) > 4<<20 {
		return auth, errors.New("updater: bounded authorization exceeds 4 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&auth); err != nil {
		return auth, err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return auth, errors.New("updater: trailing bounded authorization data")
	}
	if requireGeneration {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return auth, err
		}
		generation, ok := fields["expected_generation"]
		if !ok || bytes.Equal(bytes.TrimSpace(generation), []byte("null")) {
			return auth, errors.New("updater: bounded mutation requires an explicit expected_generation, including legacy zero")
		}
	}
	return auth, nil
}
