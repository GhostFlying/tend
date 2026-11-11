package cli

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

func runExtensions(ctx context.Context, e *Env, args []string) error {
	if len(args) == 0 {
		return usagef("extensions requires skill|plugin|mcp|category|agent|resolve|plan|publish|releases|rollback|status|inventory|doctor")
	}
	group := args[0]
	if group == "reconcile" {
		return usagef("read extensions status or extensions releases <release-id> for actual business state")
	}
	action := group
	tail := args[1:]
	if slices.Contains([]string{"skill", "plugin", "mcp", "category", "agent"}, group) {
		if len(tail) == 0 {
			return usagef("extensions %s requires a subcommand", group)
		}
		action = tail[0]
		tail = tail[1:]
	}
	fs := newFlags(e, "extensions "+group+" "+action)
	fs.Bool("json", true, "print complete structured output")
	data := fs.String("data", "", "complete import, resource or binding JSON")
	file := fs.String("file", "", "complete JSON file; - reads stdin")
	agentsFile := fs.String("agents-md", "", "read the complete instruction body from this file")
	kind := fs.String("kind", "", "filter inventory by skill, plugin or mcp")
	id := fs.String("id", "", "filter inventory or binding by stable ID")
	version := fs.String("version", "", "exact resource version to remove or show")
	category := fs.String("category", "", "one execution category ID")
	environment := fs.String("environment", "", "complete Agent execution environment ID")
	agent := fs.String("agent", "", "Agent local ID within the execution environment")
	release := fs.String("release", "", "published release ID; resolve defaults to the draft")
	after := fs.String("after", "", "opaque continuation cursor")
	limit := fs.Int("limit", 100, "page size, 1..200")
	revision := fs.Int64("revision", -1, "expected catalog revision; retries require the original value")
	request := fs.String("request-id", "", "original request ID when retrying")
	reason := fs.String("reason", "", "reason retained with the control request")
	dry := fs.Bool("dry-run", false, "preview without staging content or changing control state")
	pos, err := parse(fs, tail)
	if err != nil {
		return err
	}
	if *limit < 1 || *limit > 200 {
		return usagef("limit must be 1..200")
	}
	input, err := issueInput(e, *data, *file)
	if err != nil {
		return err
	}
	c, err := e.client(protocol.ScopeOwner)
	if err != nil {
		return err
	}
	print := func(v any) error { return json.NewEncoder(e.Stdout).Encode(v) }
	query := url.Values{"limit": {fmt.Sprint(*limit)}, "after": {*after}}
	for k, v := range map[string]string{"kind": *kind, "id": *id, "category": *category, "environment": *environment, "agent": *agent, "release": *release, "version": *version} {
		if v != "" {
			query.Set(k, v)
		}
	}
	view := ""
	switch group {
	case "inventory", "status", "doctor", "resolve", "plan", "releases":
		view = group
	case "skill", "plugin", "mcp":
		if action == "list" || action == "show" {
			view = "inventory"
			query.Set("kind", group)
			if len(pos) == 1 {
				query.Set("id", pos[0])
			} else if len(pos) != 0 || action == "show" && *id == "" {
				return usagef("%s %s requires one ID", group, action)
			}
		}
	case "category", "agent":
		if action == "show" {
			view = "categories"
			if group == "agent" {
				view = "agents"
				if *environment == "" {
					return usagef("agent requires --environment")
				}
				local := *agent
				if len(pos) == 1 {
					local = pos[0]
				}
				if local == "" || len(pos) > 1 {
					return usagef("agent show requires one local ID and --environment")
				}
				query.Set("id", *environment+"/"+local)
			} else if len(pos) == 1 {
				query.Set("id", pos[0])
			}
		}

	}
	if view != "" {
		if len(input) > 0 || *agentsFile != "" || *request != "" || *revision >= 0 {
			return usagef("read commands do not accept mutation input")
		}
		if group == "releases" && len(pos) == 1 {
			var out protocol.ExtensionRelease
			if err = c.do(ctx, "GET", "/api/v1/extensions/releases/"+url.PathEscape(pos[0]), nil, &out); err != nil {
				return err
			}
			return print(out)
		}
		var out json.RawMessage
		if err = c.do(ctx, "GET", "/api/v1/extensions/"+view+"?"+query.Encode(), nil, &out); err != nil {
			return err
		}
		if *version != "" && view == "inventory" {
			var page protocol.ExtensionResourcePage
			if err = json.Unmarshal(out, &page); err != nil {
				return err
			}
			page.Items = slices.DeleteFunc(page.Items, func(r protocol.ExtensionResource) bool { return r.Version != *version })
			return print(page)
		}
		return print(out)
	}
	if *request != "" && *revision < 0 {
		return usagef("retry requires the original --revision with --request-id")
	}
	var state protocol.ExtensionState
	retryPublish := group == "publish" && *request != ""
	if retryPublish && len(input) == 0 {
		return usagef("publish with --request-id requires the complete original payload via --data/--file; inspect extensions status and extensions releases <release-id> before another action")
	}
	if *revision < 0 {
		if err = c.do(ctx, "GET", "/api/v1/extensions/state", nil, &state); err != nil {
			return err
		}
		if *revision < 0 {
			*revision = state.Revision
		}
	}
	if *request == "" {
		*request = e.IDs.New(protocol.PrefixRequest)
	}
	env := protocol.Envelope{RequestID: *request, Target: protocol.Target{Type: "extensions", ID: "catalog"}, ExpectedControlRevision: revision, Reason: *reason}
	var payload any
	switch group {
	case "skill", "plugin", "mcp":
		switch action {
		case "add", "update":
			if len(input) == 0 {
				return usagef("%s %s requires complete --data or --file input", group, action)
			}
			var resource protocol.ExtensionResource
			if group == "mcp" {
				if err = decodeExtensionInput(input, &resource); err != nil {
					return err
				}
				resource.Kind = "mcp"
			} else {
				var in protocol.ExtensionImport
				if err = decodeExtensionInput(input, &in); err != nil {
					return err
				}
				in.Kind = group
				if len(pos) > 1 || len(pos) == 1 && pos[0] != in.ID {
					return usagef("resource ID must match complete input")
				}
				if *dry {
					return print(map[string]any{"import": in, "request_id": *request, "expected_control_revision": *revision, "publish_required": true})
				}
				if e.ImportExtension == nil {
					return fmt.Errorf("extension package installer is unavailable")
				}
				pkg, err := e.ImportExtension(ctx, in)
				if err != nil {
					return err
				}
				resource = pkg.Resource
				sum := sha256.Sum256([]byte("extensions.stage.v1:" + *request))
				encoded := base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding).EncodeToString(sum[:16])
				stageID := "req_0" + encoded[:25]
				stage := protocol.ExtensionStageRequest{RequestID: stageID, ExpectedControlRevision: *revision, Package: pkg}
				var response protocol.APIResponse
				fmt.Fprintf(e.Stderr, "Staging request %s; draft request %s; revision %d\n", stageID, *request, *revision)
				if err = c.do(ctx, "POST", "/api/v1/extensions/stage", stage, &response); err != nil {
					return err
				}
				if err = extensionReceipt(response, stageID); err != nil {
					return err
				}
			}
			if len(pos) > 1 || len(pos) == 1 && pos[0] != resource.ID {
				return usagef("resource ID must match complete input")
			}
			env.Operation = "extensions.resource_put"
			payload = protocol.ExtensionEditPayload{Resource: &resource}
		case "remove":
			if len(pos) != 1 || *version == "" {
				return usagef("remove requires one ID and --version")
			}
			env.Operation = "extensions.resource_remove"
			payload = protocol.ExtensionEditPayload{Kind: group, ID: pos[0], Version: *version}
		default:
			return usagef("%s expects add|update|remove|show|list", group)
		}
	case "category", "agent":
		if action != "set" || len(input) == 0 {
			return usagef("%s set requires a complete binding via --data or --file", group)
		}
		var b protocol.ExtensionBinding
		if err = decodeExtensionInput(input, &b); err != nil {
			return err
		}
		if group == "category" {
			if len(pos) == 1 {
				b.CategoryID = pos[0]
			}
			if b.CategoryID == "" || b.Agent != (protocol.AgentIdentity{}) {
				return usagef("category set requires a category binding")
			}
		} else {
			if *environment != "" {
				b.Agent.ExecutionEnvironmentID = *environment
			}
			if *agent != "" {
				b.Agent.LocalID = *agent
			}
			if len(pos) == 1 {
				b.Agent.LocalID = pos[0]
			}
			if b.CategoryID != "" || b.Agent.ExecutionEnvironmentID == "" || b.Agent.LocalID == "" {
				return usagef("agent set requires a complete Agent identity")
			}
		}
		if *agentsFile != "" {
			raw, err := os.ReadFile(*agentsFile)
			if err != nil {
				return err
			}
			b.AgentsMD = string(raw)
		}
		env.Operation = "extensions.binding_set"
		payload = protocol.ExtensionEditPayload{Binding: &b}
	case "publish":
		if len(input) > 0 {
			var supplied protocol.ExtensionPublishPayload
			if err = decodeExtensionInput(input, &supplied); err != nil {
				return err
			}
			env.Operation = "extensions.publish"
			payload = supplied
			break
		}
		var plan protocol.ExtensionPlan
		if err = c.do(ctx, "GET", "/api/v1/extensions/plan", nil, &plan); err != nil {
			return err
		}
		env.Operation = "extensions.publish"
		payload = protocol.ExtensionPublishPayload{Release: protocol.ExtensionRelease{ID: plan.Candidate.ID, CompositionVersion: protocol.ExtensionCompositionVersion, Catalog: plan.Draft}, Manifest: plan.Candidate}
	case "rollback":
		if len(pos) != 1 {
			return usagef("rollback requires one retained release ID")
		}
		env.Operation = "extensions.rollback"
		payload = protocol.ExtensionPublishPayload{Release: protocol.ExtensionRelease{ID: pos[0]}}
	default:
		return usagef("unknown extensions command %q", strings.Join(args, " "))
	}
	env.Payload, err = json.Marshal(payload)
	if err != nil {
		return err
	}
	if *dry {
		return print(env)
	}
	fmt.Fprintf(e.Stderr, "Extension request %s; revision %d\n", *request, *revision)
	response, err := (owner{e: e, c: c}).sendEnvelope(ctx, env)
	if err != nil {
		return err
	}
	if err = extensionReceipt(response, *request); err != nil {
		return err
	}
	return print(response.Operation)
}

func decodeExtensionInput(raw []byte, out any) error {
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return usagef("invalid complete extension input: %s", err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return usagef("extension input must contain one JSON value")
	}
	return nil
}

func extensionReceipt(response protocol.APIResponse, request string) error {
	if response.Operation == nil {
		return fmt.Errorf("missing extension receipt for trace %s; inspect extensions status and the actual release", request)
	}
	if response.Operation.Error != nil {
		return response.Operation.Error
	}
	if response.Operation.Status != protocol.OpSucceeded {
		return fmt.Errorf("extension trace %s is %s; inspect extensions status and the actual release before another action", request, response.Operation.Status)
	}
	return nil
}
