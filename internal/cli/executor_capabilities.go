package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
)

func runLocalCapabilityCommand(ctx context.Context, e *Env, args []string) error {
	if len(args) == 0 {
		return usagef("agents local capabilities verify|status|reconcile --socket <path>; verify never calls a model; reconcile reads the original request")
	}
	action := args[0]
	fs := newFlags(e, "agents local capabilities "+action)
	socket := fs.String("socket", "", "runner-private control socket")
	data := fs.String("data", "", "complete verification/reconciliation JSON; preserve original request_id and expected_revision")
	file := fs.String("file", "", "complete original input file; - reads stdin")
	dry := fs.Bool("dry-run", false, "print exact local input without probing or changing it")
	fs.Bool("json", true, "print complete evidence/request status")
	pos, err := parse(fs, args[1:])
	if err != nil {
		return err
	}
	if !filepath.IsAbs(*socket) || !slices.Contains([]string{"verify", "status", "reconcile"}, action) {
		return usagef("valid capability action and absolute --socket are required")
	}
	method, path, input := http.MethodPost, "/agents/capabilities/"+action, any(nil)
	if action == "status" || action == "reconcile" {
		if len(pos) != 1 || *data != "" || *file != "" || !protocol.ValidID(protocol.PrefixRequest, pos[0]) {
			return usagef("status/reconcile require the original request-id as one argument")
		}
		if action == "status" {
			method = http.MethodGet
			path = "/agents/capabilities/requests/" + url.PathEscape(pos[0])
		} else {
			input = protocol.ReconcileLocalCapabilityRequest{RequestID: pos[0]}
		}
	} else {
		if len(pos) != 0 {
			return usagef("verify accepts --data or --file without positional arguments")
		}
		raw, err := issueInput(e, *data, *file)
		if err != nil {
			return err
		}
		var p protocol.VerifyLocalCapabilityRequest
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&p); err != nil {
			return usagef("complete verification JSON required")
		}
		if decoder.Decode(new(any)) != io.EOF {
			return usagef("one verification JSON object required")
		}
		if !protocol.ValidID(protocol.PrefixRequest, p.RequestID) || p.ExpectedRevision < 1 || p.MaxAttempts != 1 || p.ProbeKind != "tool_inventory" || p.TimeoutMillis < 1 || p.TimeoutMillis > 30000 || p.Pin.Validate(p.Pin.Identity.ExecutionEnvironmentID) != nil || p.Pin.Purpose != "" {
			return usagef("verify requires original request_id/CAS, exact worker pin, tool_inventory, max_attempts=1 and timeout_ms in 1..30000")
		}
		if _, err := protocol.CanonicalCapabilityReferences([]protocol.CapabilityReference{p.Capability}); err != nil {
			return err
		}
		input = p
	}
	o := owner{e: e, json: true}
	if *dry {
		return o.printJSON(map[string]any{"method": method, "path": path, "request": input})
	}
	var out protocol.LocalCapabilityRequestStatus
	if err := localUpgradeRequest(ctx, *socket, method, path, input, &out); err != nil {
		return err
	}
	return o.printJSON(out)
}
