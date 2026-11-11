package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
)

func runUpdaterBackup(ctx context.Context, e *Env, args []string) error {
	if len(args) == 0 || !slices.Contains([]string{"status", "preview", "verify", "prune", "protect-log"}, args[0]) {
		return usagef("updater backup expects status, preview, verify, prune or protect-log")
	}
	action := args[0]
	flags := newFlags(e, "updater backup "+action)
	socket := flags.String("socket", "", "fixed updater control socket")
	file := flags.String("file", "", "exact verification, policy or saved preview JSON; - reads stdin")
	asJSON := flags.Bool("json", true, "print full checkpoint, protection and reclaim evidence")
	positional, err := parse(flags, args[1:])
	if err != nil {
		return err
	}
	if len(positional) != 0 || !filepath.IsAbs(*socket) {
		return usagef("backup requires an absolute --socket and no positional arguments")
	}
	var input any = map[string]any{}
	if action == "verify" || action == "prune" || action == "protect-log" || action == "preview" && *file != "" {
		if *file == "" {
			return usagef("backup verification/prune requires --file with the complete saved input")
		}
		raw, err := issueInput(e, "", *file)
		if err != nil {
			return err
		}
		switch action {
		case "protect-log":
			var value protocol.DiagnosticLogProtection
			if err = json.Unmarshal(raw, &value); err != nil {
				return err
			}
			input = map[string]any{"diagnostic_log_protection": value}
		case "verify":
			var value protocol.BackupVerification
			if err = json.Unmarshal(raw, &value); err != nil {
				return err
			}
			input = map[string]any{"backup_verification": value}
		case "prune":
			var value protocol.BackupPreview
			if err = json.Unmarshal(raw, &value); err != nil {
				return err
			}
			if value.SHA256 == "" {
				return usagef("prune input must be the exact saved preview result")
			}
			input = map[string]any{"backup_prune": protocol.BackupPruneRequest{Preview: value}}
		case "preview":
			var value protocol.BackupPolicy
			if err = json.Unmarshal(raw, &value); err != nil {
				return err
			}
			input = map[string]any{"backup_policy": value}
		}
	} else if *file != "" {
		return usagef("backup status does not accept mutation input")
	}
	method := http.MethodPost
	if action == "status" {
		method = http.MethodGet
	}
	var response struct {
		Result json.RawMessage `json:"result"`
		Error  string          `json:"error"`
	}
	if err = localUpgradeRequest(ctx, *socket, method, "/backup/"+action, input, &response); err != nil {
		return err
	}
	return printCapabilityValue(e, response.Result, *asJSON)
}
