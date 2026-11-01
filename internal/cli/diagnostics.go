package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

func runHostQueue(ctx context.Context, e *Env, args []string) error {
	fs := newFlags(e, args[0])
	asJSON := fs.Bool("json", false, "print complete structured facts")
	project := fs.String("project", "", "project filter")
	tail := args[1:]
	kind := "queue"
	action := "list"
	if args[0] == "host" {
		kind = "host"
		if len(tail) > 0 {
			action = tail[0]
			tail = tail[1:]
		}
	}
	pos, err := parse(fs, tail)
	if err != nil {
		return err
	}
	if (kind == "queue" && len(pos) != 0) || (kind == "host" && ((action != "list" && action != "show") || (action == "list" && len(pos) != 0) || (action == "show" && len(pos) != 1))) {
		return usagef("host list|show <id>; queue [--project <project>]")
	}
	if kind == "host" && *project != "" {
		return usagef("host catalog is global; project applies only to queue")
	}
	c, err := e.client(protocol.ScopeOwner)
	if err != nil {
		return err
	}
	var snap protocol.Snapshot
	route := "/api/v1/snapshot"
	if *project != "" {
		route += "?project=" + url.QueryEscape(*project)
	}
	if err = c.do(ctx, "GET", route, nil, &snap); err != nil {
		return err
	}
	var value any = snap.Queue
	if kind == "host" {
		value = snap.Hosts
		if action == "show" {
			found := false
			for _, h := range snap.Hosts {
				if h.HostID == pos[0] {
					value = h
					found = true
					break
				}
			}
			if !found {
				return protocol.Errorf(protocol.CodeNotFound, "host not found")
			}
		}
	}
	raw, _ := json.Marshal(value)
	return printCapabilityValue(e, raw, *asJSON)
}

type diagnosticCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Details any    `json:"details,omitempty"`
}

func runDoctor(ctx context.Context, e *Env, args []string) error {
	fs := newFlags(e, "doctor")
	asJSON := fs.Bool("json", false, "print checks as JSON")
	socket := fs.String("socket", "", "optional local runner control socket")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return usagef("doctor takes only flags")
	}
	c, err := e.client(protocol.ScopeOwner)
	if err != nil {
		return err
	}
	checks := []diagnosticCheck{}
	failed := false
	check := func(name, route string, out any) bool {
		if err := c.do(ctx, "GET", route, nil, out); err != nil {
			checks = append(checks, diagnosticCheck{name, "unavailable", err.Error()})
			failed = true
			return false
		}
		checks = append(checks, diagnosticCheck{name, "ok", out})
		return true
	}
	var deployment map[string]string
	if check("deployment", "/api/v1/deployment", &deployment) && c.deploymentID != "" && deployment["deployment_id"] != c.deploymentID {
		checks[len(checks)-1].Status = "abnormal"
		failed = true
	}
	var session protocol.SessionView
	check("session", "/api/v1/session", &session)
	var snapshot protocol.Snapshot
	if check("ledger", "/api/v1/snapshot", &snapshot) {
		for _, host := range snapshot.Hosts {
			state := "unavailable"
			if host.Online != nil {
				if *host.Online {
					state = "ok"
				} else {
					state = "abnormal"
					failed = true
				}
			}
			if state == "unavailable" {
				failed = true
			}
			checks = append(checks, diagnosticCheck{"host:" + host.HostID, state, host})
		}
	}
	var metrics protocol.CoordinationMetrics
	check("coordination", "/api/v1/coordination/metrics", &metrics)
	var settings protocol.ConversationSettingsView
	if check("conversation", "/api/v1/conversation/settings", &settings) {
		if !settings.Enabled {
			checks[len(checks)-1].Status = "disabled"
		} else if !settings.Available {
			checks[len(checks)-1].Status = "unavailable"
			failed = true
		}
	}
	if *socket != "" {
		var local protocol.LocalAgentConfigurationResult
		if err := localUpgradeRequest(ctx, *socket, http.MethodGet, "/agents/status", nil, &local); err != nil {
			checks = append(checks, diagnosticCheck{"local-agents", "unavailable", err.Error()})
			failed = true
		} else {
			state := "ok"
			if local.Current == nil || local.Current.State == "empty" {
				state = "disabled"
			}
			if local.ErrorCode != "" || (local.Current != nil && local.Current.PendingRequestID != "") {
				state = "abnormal"
				failed = true
			}
			checks = append(checks, diagnosticCheck{"local-agents", state, local})
		}
	}
	if *asJSON {
		if err := json.NewEncoder(e.Stdout).Encode(checks); err != nil {
			return err
		}
	} else {
		for _, v := range checks {
			fmt.Fprintf(e.Stdout, "%s: %s\n", v.Name, v.Status)
			if local, ok := v.Details.(protocol.LocalAgentConfigurationResult); ok && local.Current != nil && local.Current.PendingRequestID != "" {
				fmt.Fprintf(e.Stdout, "  Pending reload: %s; reconcile this original request before another reload.\n", local.Current.PendingRequestID)
			}

			if v.Status == "unavailable" {
				fmt.Fprintf(e.Stdout, "  %v\n", v.Details)
			}
		}
		fmt.Fprintf(e.Stdout, "Queue: %d pending actions; use tend queue --json for blocking reasons.\n", len(snapshot.Queue))
	}
	if failed {
		return errors.New("diagnostic checks found unavailable or abnormal state")
	}
	return nil
}

func runLocalAgentCommand(ctx context.Context, e *Env, args []string) error {
	if len(args) == 0 {
		return usagef("agents local status|validate|reload|reconcile|register|registration --socket <path>")
	}
	fs := newFlags(e, "agents local "+args[0])
	dry := fs.Bool("dry-run", false, "print original local control input without mutation")
	socket := fs.String("socket", "", "runner-private control socket")
	request := fs.String("request-id", "", "original configuration request")
	revision := fs.String("revision", "", "original configuration revision")
	fs.Bool("json", true, "print configuration receipt as JSON")
	pos, err := parse(fs, args[1:])
	if err != nil {
		return err
	}
	if len(pos) != 0 || !filepath.IsAbs(*socket) {
		return usagef("agents local requires --socket and no positional arguments")
	}
	if !slices.Contains([]string{"status", "validate", "reload", "reconcile", "register", "registration"}, args[0]) {
		return usagef("unsupported local agent operation")
	}
	var rev *uint64
	if *revision != "" {
		n, err := parseNonnegative(*revision)
		if err != nil {
			return err
		}
		rev = &n
	}
	if *dry {
		var input any
		switch args[0] {
		case "register":
			if *request != "" || rev != nil {
				return usagef("registration identity is owned by the runner; use registration to inspect it")
			}
		case "reload":
			if *request != "" && rev == nil {
				return usagef("retry requires original --revision")
			}
			if *request == "" {
				*request = e.IDs.New(protocol.PrefixRequest)
			}
			if rev == nil {
				var current protocol.LocalAgentConfigurationResult
				if err := localUpgradeRequest(ctx, *socket, http.MethodGet, "/agents/status", nil, &current); err != nil {
					return err
				}
				if current.Current == nil {
					return errors.New("local revision unavailable")
				}
				n := current.Current.Revision
				rev = &n
			}
			input = protocol.ReloadLocalAgentsRequest{RequestID: *request, ExpectedRevision: *rev}
		case "reconcile":
			if *request == "" || rev != nil {
				return usagef("reconcile requires --request-id and no --revision")
			}
			input = protocol.ReconcileLocalAgentsRequest{RequestID: *request}
		default:
			return usagef("dry-run applies only to reload, reconcile or register")
		}
		if *request != "" && !protocol.ValidID(protocol.PrefixRequest, *request) {
			return usagef("malformed request identity")
		}
		return (owner{e: e, json: true}).printJSON(map[string]any{"method": "POST", "path": "/agents/" + args[0], "request": input})
	}
	local := *e
	local.HTTP = nil
	if code := RunLocalAgents(ctx, local, *socket, args[0], *request, rev); code != 0 {
		return errors.New("local agent control failed; retain original request identity")
	}
	return nil
}

func runRequestReplay(ctx context.Context, e *Env, args []string) error {
	fs := newFlags(e, "request replay")
	data := fs.String("data", "", "original Envelope")
	file := fs.String("file", "", "original Envelope file; - reads stdin")
	dry := fs.Bool("dry-run", false, "print original Envelope")
	fs.Bool("json", true, "print complete receipt")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return usagef("replay takes --data or --file")
	}
	raw, err := issueInput(e, *data, *file)
	if err != nil {
		return err
	}
	var env protocol.Envelope
	if err = json.Unmarshal(raw, &env); err != nil {
		return usagef("original Envelope required")
	}
	if err = env.Validate(); err != nil {
		return err
	}
	if !ownerOperationSupported(env.Operation) {
		return usagef("operation is not an owner capability")
	}
	if *dry {
		return (owner{e: e, json: true}).printJSON(env)
	}
	c, err := e.client(protocol.ScopeOwner)
	if err != nil {
		return err
	}
	return sendDirectEnvelope(ctx, owner{e: e, c: c, json: true}, env)
}

func ownerOperationSupported(operation string) bool {
	for _, v := range CapabilityBindings() {
		if v.Capability == operation && !strings.Contains(operation, " ") {
			return true
		}
	}
	return false
}
