package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/GhostFlying/tend/internal/protocol"
)

func runUpdaterHandoff(ctx context.Context, e *Env, args []string) error {
	if len(args) < 2 || (args[0] != "handoff" && args[0] != "maintenance") {
		return usagef("updater expects handoff plan|apply|status")
	}
	action := args[1]
	if args[0] == "maintenance" && (action == "recover-stop" || action == "prepare" || action == "authorize" || action == "cancel" || action == "finish" || action == "resume" || action == "ledger" || action == "health") {
		return runCommittedMaintenanceControl(ctx, e, args[1:])
	}
	if !protocol.ValidUpdaterHandoffAction(action) && !(args[0] == "maintenance" && (action == "prepare" || action == "authorize" || action == "cancel" || action == "finish")) {
		return usagef("handoff expects plan, apply or status")
	}
	fs := newFlags(e, "updater handoff "+action)
	resumeID := fs.String("resume-request-id", "", "distinct stable request ID for guarded installer continuation")
	generation := fs.Uint64("expected-generation", 0, "current installer journal generation for continuation")
	installer := fs.String("installer", "", "absolute path to the reviewed new tend-updater artifact (first hop)")
	installerSHA := fs.String("installer-sha256", "", "reviewed installer SHA-256")
	config := fs.String("config", "", "fixed private updater configuration")
	plan := fs.String("plan", "", "private handoff plan; plan resolves missing factual digests, apply requires exact output")
	asJSON := fs.Bool("json", false, "print structured journal and guards")
	_ = asJSON
	positional, err := parse(fs, args[2:])
	if err != nil {
		return err
	}
	if len(positional) != 0 || !filepath.IsAbs(*installer) || !filepath.IsAbs(*config) || len(*installerSHA) != 64 || (action != "status" && !filepath.IsAbs(*plan)) {
		return usagef("handoff requires absolute --installer, --config, --plan and --installer-sha256")
	}
	file, err := os.Open(*installer)
	if err != nil {
		return err
	}
	defer file.Close()
	st, err := file.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return usagef("installer must be a regular executable artifact")
	}
	directory, err := os.MkdirTemp("", "tend-reviewed-installer-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	executable := filepath.Join(directory, "installer")
	pinned, err := os.OpenFile(executable, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	sum := sha256.New()
	if _, err := io.Copy(io.MultiWriter(sum, pinned), file); err != nil {
		_ = pinned.Close()
		return err
	}
	if err := pinned.Close(); err != nil {
		return err
	}
	if hex.EncodeToString(sum.Sum(nil)) != *installerSHA {
		return usagef("installer SHA-256 differs from reviewed artifact")
	}
	argv := []string{args[0], action, "--config", *config}
	if *plan != "" {
		argv = append(argv, "--plan", *plan)
	}
	if *resumeID != "" {
		if action == "status" || !protocol.ValidID(protocol.PrefixRequest, *resumeID) || *generation == 0 {
			return usagef("continuation requires plan/apply, a valid --resume-request-id and --expected-generation")
		}
		argv = append(argv, "--resume-request-id", *resumeID, "--expected-generation", strconv.FormatUint(*generation, 10))
	} else if *generation != 0 {
		if args[0] == "maintenance" && (action == "prepare" || action == "authorize" || action == "cancel" || action == "finish") {
			argv = append(argv, "--expected-generation", strconv.FormatUint(*generation, 10))
		} else {
			return usagef("expected-generation requires resume-request-id")
		}
	}
	if err := os.Chmod(executable, 0o700); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, executable, argv...)
	cmd.Stdout = e.Stdout
	cmd.Stderr = e.Stderr
	return cmd.Run()
}

type recoveryStatusView struct {
	Plan      protocol.ForwardRecoveryPlan `json:"plan"`
	Phase     string                       `json:"phase"`
	Receipt   *protocol.RecoveryReceipt    `json:"receipt,omitempty"`
	LastError string                       `json:"last_error,omitempty"`
}

func runForwardRecovery(ctx context.Context, e *Env, args []string) error {
	if len(args) == 0 {
		return usagef("upgrade recover expects plan, apply or status")
	}
	action := args[0]
	if action != "plan" && action != "apply" && action != "status" {
		return usagef("upgrade recover expects plan, apply or status")
	}
	fs := newFlags(e, "upgrade recover "+action)
	socket := fs.String("socket", "", "fixed private updater control socket")
	ledgerSocket := fs.String("ledger-socket", "", "new server private control socket; directly read ledger for --recovery-id")
	planFile := fs.String("plan", "", "exact private forward plan from reviewed handoff")
	requestID := fs.String("request-id", "", "stable request ID matching plan; retain for replay")
	generation := fs.Uint64("expected-generation", 0, "current updater state generation for apply")
	upgradeID := fs.String("upgrade-id", "", "filter original upgrade identity")
	recoveryID := fs.String("recovery-id", "", "filter recovery identity")
	phase := fs.String("phase", "", "filter recovery phase (status)")
	after := fs.String("after", "", "exclusive recovery ID cursor (status)")
	limit := fs.Int("limit", 50, "status page size, 1..200")
	dry := fs.Bool("dry-run", false, "perform read-only recovery guards")
	asJSON := fs.Bool("json", false, "print structured recovery evidence")
	_ = asJSON
	positional, err := parse(fs, args[1:])
	if err != nil {
		return err
	}
	if len(positional) != 0 || !filepath.IsAbs(*socket) {
		return usagef("recover requires an absolute --socket")
	}
	if *limit < 1 || *limit > 200 {
		return usagef("recover limit must be 1..200")
	}
	var response struct {
		State json.RawMessage `json:"state"`
	}
	if action == "status" {
		if *planFile != "" || *requestID != "" || *generation != 0 || *dry {
			return usagef("recover status does not take write inputs")
		}
		if err := localUpgradeRequest(ctx, *socket, http.MethodGet, "/status", nil, &response); err != nil {
			return err
		}
		var state struct {
			Generation uint64                        `json:"generation"`
			Recovery   *recoveryStatusView           `json:"recovery"`
			Recoveries map[string]recoveryStatusView `json:"recoveries"`
		}
		if err := json.Unmarshal(response.State, &state); err != nil {
			return err
		}
		if state.Recoveries == nil {
			state.Recoveries = map[string]recoveryStatusView{}
		}
		if state.Recovery != nil {
			state.Recoveries[state.Recovery.Plan.RecoveryID] = *state.Recovery
		}
		ids := []string{}
		for id, r := range state.Recoveries {
			if id > *after && (*upgradeID == "" || r.Plan.Original.UpgradeID == *upgradeID) && (*recoveryID == "" || id == *recoveryID) && (*phase == "" || r.Phase == *phase) {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		next := ""
		if len(ids) > *limit {
			ids = ids[:*limit]
			next = ids[len(ids)-1]
		}
		items := []recoveryStatusView{}
		for _, id := range ids {
			items = append(items, state.Recoveries[id])
		}
		output := map[string]any{"generation": state.Generation, "items": items, "next_cursor": next, "ledger_observed": false}
		if *ledgerSocket != "" {
			if !filepath.IsAbs(*ledgerSocket) || *recoveryID == "" {
				return usagef("ledger-socket requires an absolute path and --recovery-id")
			}
			var ledger protocol.ForwardRecoveryLedger
			if err := localUpgradeRequest(ctx, *ledgerSocket, http.MethodGet, "/upgrades/recovery/status?recovery_id="+url.QueryEscape(*recoveryID), nil, &ledger); err != nil {
				return err
			}
			if ledger.Plan.RecoveryID != *recoveryID {
				return usagef("server returned a different recovery ledger identity")
			}
			output["ledger"] = ledger
			output["ledger_observed"] = true
			if local, ok := state.Recoveries[*recoveryID]; ok {
				output["linked_receipt_matches"] = ledger.Linked && ledger.Receipt != nil && local.Receipt != nil && *ledger.Receipt == *local.Receipt
			}
		}
		return json.NewEncoder(e.Stdout).Encode(output)
	}
	if *ledgerSocket != "" {
		return usagef("ledger-socket is only valid for status")
	}
	if !filepath.IsAbs(*planFile) || *requestID == "" || *upgradeID == "" || *recoveryID == "" || *phase != "" || *after != "" {
		return usagef("recover plan/apply requires --plan, --request-id, --upgrade-id and --recovery-id")
	}
	raw, err := os.ReadFile(*planFile)
	if err != nil {
		return err
	}
	var p protocol.ForwardRecoveryPlan
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return usagef("trailing recovery plan data")
	}
	if p.RequestID != *requestID || p.RecoveryID != *recoveryID || p.Original.UpgradeID != *upgradeID {
		return usagef("recovery flags conflict with the reviewed plan")
	}
	if action == "apply" && *generation == 0 {
		return usagef("recover apply requires --expected-generation")
	}
	if *dry {
		action = "plan"
	}
	input := struct {
		Recovery           protocol.ForwardRecoveryPlan `json:"recovery"`
		ExpectedGeneration uint64                       `json:"expected_generation"`
	}{p, *generation}
	fmt.Fprintf(e.Stderr, "Recovery %s; request %s; original %s\n", p.RecoveryID, p.RequestID, p.Original.UpgradeID)
	if err := localUpgradeRequest(ctx, *socket, http.MethodPost, "/recovery/"+action, input, &response); err != nil {
		return err
	}
	return json.NewEncoder(e.Stdout).Encode(response.State)
}
