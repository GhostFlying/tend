package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func runBoundedMaintenance(ctx context.Context, e *Env, args []string) error {
	if len(args) == 0 || !slices.Contains(protocol.BoundedMaintenanceActions(), args[0]) {
		return usagef("updater bounded expects plan, apply, force, extend, cancel, status, reconcile or takeover")
	}
	action := args[0]
	fs := newFlags(e, "updater bounded "+action)
	installer := fs.String("installer", "", "absolute reviewed independent installer artifact")
	installerSHA := fs.String("installer-sha256", "", "reviewed installer SHA-256")
	config := fs.String("config", "", "exact fixed private updater configuration")
	plan := fs.String("plan", "", "complete private source plan; original bytes are pinned before execution")
	authorization := fs.String("authorization", "", "typed independent bounded authorization; plan may fill factual bindings")
	request := fs.String("request-id", "", "original stable request identity; mutation flags must match the authorization")
	generation := fs.Uint64("expected-generation", 0, "fresh durable generation; must match the authorization")
	phase := fs.String("expected-phase", "", "fresh durable phase; must match the authorization")
	defaultTimeout := 370 * time.Second
	if action == "status" || action == "reconcile" {
		defaultTimeout = 10 * time.Second
	}
	timeout := fs.Duration("timeout", defaultTimeout, "finite installer command deadline; timeout preserves unknown effects")
	dry := fs.Bool("dry-run", false, "export exact installer arguments and typed inputs without executing")
	asJSON := fs.Bool("json", false, "preserve structured durable facts and force audit")
	_ = asJSON
	positional, err := parse(fs, args[1:])
	if err != nil {
		return err
	}
	provided := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { provided[f.Name] = true })
	if len(positional) != 0 || !filepath.IsAbs(*installer) || !filepath.IsAbs(*config) {
		return usagef("bounded maintenance requires absolute --installer and --config")
	}
	digest, err := hex.DecodeString(*installerSHA)
	if err != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != *installerSHA {
		return usagef("bounded maintenance requires canonical --installer-sha256")
	}
	if *timeout <= 0 || *timeout > 24*time.Hour {
		return usagef("bounded command timeout must be positive and at most 24h")
	}
	readOnly := action == "status" || action == "reconcile"
	mutating := !readOnly && action != "plan"
	if readOnly && (provided["plan"] || provided["authorization"] || provided["expected-generation"] || provided["expected-phase"]) {
		return usagef("bounded status/reconcile takes no plan, authorization or CAS inputs")
	}
	if *request != "" && !protocol.ValidID(protocol.PrefixRequest, *request) {
		return usagef("bounded request identity is malformed")
	}
	argv := []string{"bounded", action, "--config", *config}
	var inputs []boundedInstallerInput
	var auth *protocol.BoundedMaintenanceAuthorization
	var sourcePlan json.RawMessage
	if !readOnly {
		if !filepath.IsAbs(*plan) || !filepath.IsAbs(*authorization) {
			return usagef("bounded plan/mutation requires absolute --plan and --authorization")
		}
		raw, err := readBoundedJSONFile(*plan)
		if err != nil {
			return err
		}
		// The installer owns the complete source-plan type and its canonical
		// digest. The CLI pins its bytes without projecting away plan fields.
		var planObject map[string]json.RawMessage
		if json.Unmarshal(raw, &planObject) != nil || planObject == nil {
			return usagef("bounded source plan must be a complete JSON object")
		}
		sourcePlan = raw
		inputs = append(inputs, boundedInstallerInput{Flag: "--plan", Data: raw})
		argv = append(argv, "--plan", *plan)
		raw, err = readBoundedJSONFile(*authorization)
		if err != nil {
			return err
		}
		auth = new(protocol.BoundedMaintenanceAuthorization)
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(auth); err != nil {
			return usagef("invalid bounded authorization: %v", err)
		}
		if decoder.Decode(new(any)) != io.EOF {
			return usagef("trailing bounded authorization data")
		}
		if !protocol.ValidID(protocol.PrefixRequest, auth.RequestID) || !slices.Contains([]string{"server", "runner"}, auth.Role) || auth.Reason == "" || auth.ExpiresAtMillis <= 0 {
			return usagef("bounded authorization requires request_id, server/runner role, reason and expiry")
		}
		if provided["request-id"] && *request != auth.RequestID || provided["expected-generation"] && *generation != auth.ExpectedGeneration || provided["expected-phase"] && *phase != auth.ExpectedPhase {
			return usagef("bounded flags conflict with the original authorization")
		}
		*request = auth.RequestID
		*generation = auth.ExpectedGeneration
		*phase = auth.ExpectedPhase
		if mutating && (!boundedGenerationPresent(raw) || *phase == "" || !boundedSHA256(auth.PlanSHA256)) {
			return usagef("bounded mutation requires complete authorization generation, phase and plan digest")
		}
		inputs = append(inputs, boundedInstallerInput{Flag: "--authorization", Data: raw})
		argv = append(argv, "--authorization", *authorization)
	}
	if *request != "" {
		argv = append(argv, "--request-id", *request)
	}
	if !readOnly {
		argv = append(argv, "--expected-generation", strconv.FormatUint(*generation, 10))
	}
	if *phase != "" {
		argv = append(argv, "--expected-phase", *phase)
	}
	argv = append(argv, "--timeout", timeout.String(), "--json")
	next := []string{"tend", "updater", "bounded", "reconcile", "--installer", *installer, "--installer-sha256", *installerSHA, "--config", *config, "--timeout", "10s", "--json"}
	if *request != "" {
		next = append(next, "--request-id", *request)
	}
	if *dry {
		return json.NewEncoder(e.Stdout).Encode(struct {
			Action          string                                    `json:"action"`
			Installer       string                                    `json:"installer"`
			InstallerSHA256 string                                    `json:"installer_sha256"`
			Arguments       []string                                  `json:"arguments"`
			Plan            json.RawMessage                           `json:"plan,omitempty"`
			Authorization   *protocol.BoundedMaintenanceAuthorization `json:"authorization,omitempty"`
			NextCommand     []string                                  `json:"next_command"`
		}{action, *installer, *installerSHA, argv, sourcePlan, auth, next})
	}
	if mutating {
		fmt.Fprintf(e.Stderr, "Bounded maintenance %s; request %s; generation %d; phase %s\n", action, *request, *generation, *phase)
	}
	return runPinnedBoundedInstaller(ctx, e, boundedInstallerInvocation{
		Installer: *installer, InstallerSHA256: *installerSHA, Arguments: argv,
		Action: action, Mutating: mutating, RequestID: *request,
		ExpectedGeneration: *generation, ExpectedPhase: *phase,
		Timeout: *timeout, NextCommand: next, PinnedInputs: inputs,
	})
}

func boundedSHA256(value string) bool {
	b, err := hex.DecodeString(value)
	return err == nil && len(b) == sha256.Size && hex.EncodeToString(b) == value
}

func boundedGenerationPresent(raw []byte) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	value, ok := fields["expected_generation"]
	return ok && !bytes.Equal(bytes.TrimSpace(value), []byte("null"))
}

func readBoundedJSONFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 4<<20 {
		return nil, usagef("bounded input must be a regular JSON file of at most 4 MiB")
	}
	raw, err := io.ReadAll(io.LimitReader(f, (4<<20)+1))
	if err == nil && len(raw) > 4<<20 {
		return nil, usagef("bounded JSON input grew beyond 4 MiB")
	}
	return raw, err
}
