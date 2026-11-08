package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/GhostFlying/tend/internal/protocol"
)

func runMaintenance(ctx context.Context, e *Env, args []string) error {
	if len(args) == 0 || !slices.Contains(protocol.MaintenanceActions(), args[0]) {
		return usagef("maintenance expects a subcommand; run tend maintenance --help")
	}
	action := args[0]
	fs := newFlags(e, "maintenance "+action)
	defaultSocket := ""
	if e.Getenv != nil {
		defaultSocket = e.Getenv("TEND_MAINTENANCE_SOCKET")
	}
	socket := fs.String("socket", defaultSocket, "private maintenance control socket (or TEND_MAINTENANCE_SOCKET)")
	id := fs.String("id", "", "case or original request ID")
	after := fs.Int("after", 0, "page offset from previous next")
	limit := fs.Int("limit", 50, "page size, 1..100")
	revision := fs.Int64("revision", -1, "original expected control revision; required for retry")
	request := fs.String("request-id", "", "stable request identity for identical retry")
	reason := fs.String("reason", "", "diagnosis or reconciliation evidence")
	until := fs.Int64("until", 0, "defer until Unix seconds")
	mode := fs.String("mode", "", "backend adoption: when-idle or interrupt")
	session := fs.String("session-id", "", "original native session to adopt after an uncertain creation")
	turn := fs.String("turn-id", "", "original native turn receipt to reconcile")
	base := fs.String("base", "", "base branch for a serialized native independent review")
	head := fs.String("head", "", "exact candidate commit for independent review")
	releaseDir := fs.String("release", "", "verified standalone component directory")
	upgradeID := fs.String("upgrade-id", "", "original planned component upgrade ID")
	data := fs.String("data", "", "complete backend JSON for config adoption")
	file := fs.String("file", "", "backend JSON file, or - for stdin")
	dry := fs.Bool("dry-run", false, "print the complete command for review and replay")
	asJSON := fs.Bool("json", true, "print full structured output")
	pos, err := parse(fs, args[1:])
	if err != nil {
		return err
	}
	if len(pos) != 0 || !filepath.IsAbs(*socket) {
		return usagef("maintenance requires an absolute --socket and no positional arguments")
	}
	if *revision < -1 {
		return usagef("revision must be nonnegative")
	}
	read := slices.Contains([]string{"status", "cases", "show", "history", "session", "request", "upgrade-status"}, action) || action == "config" && *data == "" && *file == ""
	path := "/maintenance/" + action
	if read {
		if *request != "" || *revision != -1 || *reason != "" || *until != 0 || *mode != "" || *session != "" || *turn != "" || *base != "" || *head != "" || *data != "" || *file != "" || *dry || *releaseDir != "" || *upgradeID != "" {
			return usagef("read-only maintenance commands take no mutation input")
		}
		if action == "show" || action == "request" {
			if *id == "" {
				return usagef("--id is required")
			}
			path += "/" + url.PathEscape(*id)
		}
		if action == "cases" || action == "history" {
			if *after < 0 || *limit < 1 || *limit > 100 {
				return usagef("invalid pagination")
			}
			path += "?after=" + strconv.Itoa(*after) + "&limit=" + strconv.Itoa(*limit)
		}
		var value json.RawMessage
		if err := localUpgradeRequest(ctx, *socket, http.MethodGet, path, nil, &value); err != nil {
			return err
		}
		return printCapabilityValue(e, value, *asJSON)
	}
	input := protocol.MaintenanceRequest{RequestID: *request, ExpectedRevision: *revision, CaseID: *id, Reason: *reason, Until: *until, Mode: *mode, SessionID: *session}
	input.ReleaseDir = *releaseDir
	if input.ReleaseDir != "" {
		input.ReleaseDir, err = filepath.Abs(input.ReleaseDir)
		if err != nil {
			return err
		}
	}
	input.UpgradeID = *upgradeID
	input.TurnID = *turn
	input.Base = *base
	input.Head = *head
	if action == "config" {
		raw, err := issueInput(e, *data, *file)
		if err != nil {
			return err
		}
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if err := d.Decode(&input.Backend); err != nil {
			return usagef("invalid backend JSON: %v", err)
		}
		if d.Decode(new(any)) != io.EOF || input.Backend == nil {
			return usagef("backend must be one complete JSON object")
		}
	}
	if input.RequestID != "" && input.ExpectedRevision < 0 {
		return usagef("retry requires original --revision and input")
	}
	if input.ExpectedRevision < 0 {
		var s protocol.MaintenanceStatus
		if err := localUpgradeRequest(ctx, *socket, http.MethodGet, "/maintenance/status", nil, &s); err != nil {
			return err
		}
		input.ExpectedRevision = s.Revision
	}
	if input.RequestID == "" {
		input.RequestID = e.IDs.New(protocol.PrefixRequest)
	}
	if *dry {
		return (owner{e: e, json: true}).printJSON(map[string]any{"method": "POST", "path": path, "request": input})
	}
	fmt.Fprintf(e.Stderr, "Maintenance request %s; revision %d; preserve the original input for retry\n", input.RequestID, input.ExpectedRevision)
	var value json.RawMessage
	if err := localUpgradeRequest(ctx, *socket, http.MethodPost, path, input, &value); err != nil {
		return err
	}
	return printCapabilityValue(e, value, *asJSON)
}
