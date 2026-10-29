package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/GhostFlying/tend/internal/protocol"
)

func runArchive(ctx context.Context, e *Env, args []string) error {
	if len(args) == 0 {
		return usagef("archive expects prepare, status, cancel or host")
	}
	action := args[0]
	if action != "prepare" && action != "status" && action != "cancel" && action != "host" {
		return usagef("archive expects prepare, status, cancel or host")
	}
	fs := newFlags(e, "archive "+action)
	socket := fs.String("socket", e.Getenv("TEND_UPGRADE_SOCKET"), "server local control socket")
	request := fs.String("request-id", "", "original ID for an identical retry")
	revision := fs.Int64("revision", -1, "original CAS revision when retrying")
	host := fs.String("host", "", "host to maintain or explicitly restore")
	mode := fs.String("mode", protocol.RunnerMaintenance, "maintenance or normal (host only)")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parse(fs, args[1:])
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return usagef("archive takes no positional arguments")
	}
	if *socket == "" {
		*socket = filepath.Join(e.Home, ".local/share/tend/server/upgrades.sock")
	}
	if !filepath.IsAbs(*socket) {
		return usagef("archive --socket must be absolute")
	}
	if action == "status" {
		if *request != "" || *revision != -1 || *host != "" {
			return usagef("status takes no request, revision or host")
		}
		var v protocol.ArchiveOnlineStatus
		if err := localUpgradeRequest(ctx, *socket, http.MethodGet, "/archive/status", nil, &v); err != nil {
			return err
		}
		if *asJSON {
			return json.NewEncoder(e.Stdout).Encode(v)
		}
		fmt.Fprintf(e.Stdout, "Prepared: %t; revision %d; online ready: %t; admitted owner writes: %d\n", v.Preparation.Prepared, v.Preparation.Revision, v.OnlineReady, v.ActiveOwnerRequests)
		for _, b := range v.Blockers {
			fmt.Fprintln(e.Stdout, b)
		}
		for _, h := range v.Hosts {
			fmt.Fprintf(e.Stdout, "%s: control revision %d, %s; blockers %v\n", h.HostID, h.Control.Revision, h.Control.Mode, h.Blockers)
		}
		return nil
	}
	if *request != "" && *revision < 0 {
		return usagef("an existing --request-id requires its original --revision")
	}
	if action == "host" && (*host == "" || (*mode != protocol.RunnerMaintenance && *mode != protocol.RunnerNormal)) {
		return usagef("host requires --host and maintenance or normal --mode")
	}
	if action != "host" && *host != "" {
		return usagef("--host is only valid for archive host")
	}
	if *revision < 0 {
		var v protocol.ArchiveOnlineStatus
		if err := localUpgradeRequest(ctx, *socket, http.MethodGet, "/archive/status", nil, &v); err != nil {
			return err
		}
		*revision = v.Preparation.Revision
		if action == "host" {
			found := false
			for _, h := range v.Hosts {
				if h.HostID == *host {
					*revision = h.Control.Revision
					found = true
					break
				}
			}
			if !found {
				return usagef("host is absent from current archive status")
			}
		}
	}
	if *request == "" {
		*request = e.IDs.New(protocol.PrefixRequest)
	}
	if !protocol.ValidID(protocol.PrefixRequest, *request) {
		return usagef("malformed request ID")
	}
	// Print the exact retry identity before sending; unknown does not get a new ID.
	fmt.Fprintf(e.Stderr, "Archive %s; request %s; revision %d\n", action, *request, *revision)
	var input any = protocol.ArchiveControlRequest{RequestID: *request, ExpectedControlRevision: *revision}
	if action == "host" {
		input = protocol.ArchiveHostControlRequest{RequestID: *request, ExpectedControlRevision: *revision, HostID: *host, Mode: *mode}
	}
	var result json.RawMessage
	if err := localUpgradeRequest(ctx, *socket, http.MethodPost, "/archive/"+action, input, &result); err != nil {
		return err
	}
	_, err = fmt.Fprintln(e.Stdout, string(result))
	return err
}
