package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"github.com/GhostFlying/tend/internal/protocol"
)

func runCommittedMaintenanceControl(ctx context.Context, e *Env, args []string) error {
	action := args[0]
	fs := newFlags(e, "updater maintenance "+action)
	socket := fs.String("socket", "", "fixed private updater socket; server socket for ledger/health")
	plan := fs.String("plan", "", "exact private committed handoff plan")
	id := fs.String("maintenance-id", "", "exact independent maintenance ledger identity")
	generation := fs.Uint64("expected-generation", 0, "current durable updater generation")
	request := fs.String("request-id", "", "distinct stable explicit authorization request")
	expires := fs.Int64("expires-at-ms", 0, "fresh bounded continuation expiry")
	window := fs.String("window-claim", "", "fresh operator resource window")
	evidence := fs.String("resource-evidence", "", "fresh private resource handoff file")
	evidenceSHA := fs.String("resource-sha256", "", "SHA-256 of the fresh resource handoff")
	firstCapture := fs.String("first-capture", "", "first complete retained-run inventory under archive preparation")
	secondCapture := fs.String("second-capture", "", "second identical complete prepared inventory")
	asJSON := fs.Bool("json", false, "print structured maintenance facts")
	_ = asJSON
	positional, err := parse(fs, args[1:])
	if err != nil {
		return err
	}
	if len(positional) != 0 || !filepath.IsAbs(*socket) {
		return usagef("maintenance requires an absolute --socket")
	}
	if action != "resume" && action != "recover-stop" && (*request != "" || *expires != 0 || *window != "" || *evidence != "" || *evidenceSHA != "") {
		return usagef("fresh authorization inputs require maintenance resume")
	}
	if action != "recover-stop" && (*firstCapture != "" || *secondCapture != "") {
		return usagef("inventory captures require maintenance recover-stop")
	}
	if action == "recover-stop" && (*expires != 0 || *window != "" || *evidence != "" || *evidenceSHA != "") {
		return usagef("recover-stop uses the fixed plan resource window")
	}
	if action == "ledger" {
		if *id == "" || *plan != "" || *generation != 0 {
			return usagef("maintenance ledger requires --maintenance-id only")
		}
		var v protocol.CommittedMaintenanceLedger
		if err := localUpgradeRequest(ctx, *socket, http.MethodGet, "/upgrades/maintenance/status?maintenance_id="+url.QueryEscape(*id), nil, &v); err != nil {
			return err
		}
		return json.NewEncoder(e.Stdout).Encode(v)
	}
	if !filepath.IsAbs(*plan) || *id != "" {
		return usagef("maintenance action requires exact absolute --plan")
	}
	raw, err := os.ReadFile(*plan)
	if err != nil {
		return err
	}
	var p struct {
		Maintenance protocol.CommittedMaintenancePlan `json:"maintenance"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	if p.Maintenance.MaintenanceID == "" || !protocol.ValidID(protocol.PrefixRequest, p.Maintenance.RequestID) {
		return usagef("invalid committed maintenance plan")
	}
	if action == "health" {
		if *generation != 0 {
			return usagef("maintenance health takes no generation")
		}
		var v protocol.CommittedMaintenanceHealth
		if err := localUpgradeRequest(ctx, *socket, http.MethodPost, "/updater/maintenance/health", p.Maintenance, &v); err != nil {
			return err
		}
		return json.NewEncoder(e.Stdout).Encode(v)
	}
	if *generation == 0 {
		return usagef("maintenance mutation requires --expected-generation")
	}
	op := action
	if action == "authorize" {
		op = "apply"
	}
	if action != "recover-stop" && action != "resume" && action != "prepare" && action != "authorize" && action != "cancel" && action != "finish" {
		return usagef("unknown committed maintenance action")
	}
	var v struct {
		State json.RawMessage `json:"state"`
		Error string          `json:"error,omitempty"`
	}
	payload := map[string]any{"maintenance": p.Maintenance, "expected_generation": *generation}
	if action == "recover-stop" {
		if !protocol.ValidID(protocol.PrefixRequest, *request) || !filepath.IsAbs(*firstCapture) || !filepath.IsAbs(*secondCapture) || p.Maintenance.ReceiptRecoveryStop == nil {
			return usagef("recover-stop requires an explicit plan, distinct request and two complete prepared captures")
		}
		r := protocol.MaintenanceReceiptRecoveryRequest{RequestID: *request, ExpectedGeneration: *generation}
		for i, path := range []string{*firstCapture, *secondCapture} {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			var rows []protocol.CommittedRetainedRun
			if err := json.Unmarshal(raw, &rows); err != nil {
				return usagef("capture must contain the complete retained-run inventory: %v", err)
			}
			if i == 0 {
				r.FirstCapture = rows
			} else {
				r.SecondCapture = rows
			}
		}
		payload["maintenance_receipt_recovery"] = r
	}
	if action == "resume" {
		if !protocol.ValidID(protocol.PrefixRequest, *request) || *expires <= 0 || *window == "" || !filepath.IsAbs(*evidence) || len(*evidenceSHA) != 64 {
			return usagef("maintenance resume requires request, expiry, window and fixed resource evidence")
		}
		payload["maintenance_continuation"] = protocol.MaintenanceContinuation{RequestID: *request, ExpectedGeneration: *generation, WindowClaim: *window, ExpiresAtMillis: *expires, ResourceEvidence: *evidence, ResourceSHA256: *evidenceSHA}
	}
	if err := localUpgradeRequest(ctx, *socket, http.MethodPost, "/maintenance/"+op, payload, &v); err != nil {
		return err
	}
	if v.Error != "" {
		return fmt.Errorf("maintenance rejected: %s", v.Error)
	}
	return json.NewEncoder(e.Stdout).Encode(v)
}
