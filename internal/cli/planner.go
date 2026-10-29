package cli

import (
	"context"
	"encoding/json"
	"github.com/GhostFlying/tend/internal/protocol"
	"net/url"
)

func runPlanningRecord(ctx context.Context, e *Env, args []string, deadline bool) error {
	noun := "next-action"
	path := "/api/v1/planner-entries/"
	op := "planner.entries_put"
	target := protocol.Target{Type: "planner_entry"}
	if deadline {
		noun = "issue deadline"
		path = "/api/v1/deadlines/"
		op = "issues.deadlines_put"
		target.Type = "deadline"
	}
	if len(args) == 0 {
		return usagef("%s expects list|show|put|cancel|replay", noun)
	}
	action := args[0]
	fs := newFlags(e, noun+" "+action)
	data := fs.String("data", "", "inline payload or original Envelope")
	file := fs.String("file", "", "JSON file; - reads stdin")
	request := fs.String("request-id", "", "original request for retry")
	revision := fs.Int64("revision", -1, "original expected revision")
	dry := fs.Bool("dry-run", false, "print original request")
	reason := fs.String("reason", "", "control reason")
	issue := fs.String("issue", "", "Issue filter")
	kind := fs.String("kind", "", "user_action or issue_priority")
	pos, err := parse(fs, args[1:])
	if err != nil {
		return err
	}
	c, err := e.client(protocol.ScopeOwner)
	if err != nil {
		return err
	}
	o := owner{e: e, c: c, json: true}
	if action == "list" {
		query := url.Values{"issue_id": {*issue}, "kind": {*kind}}
		list := "/api/v1/next-actions"
		if deadline {
			list = "/api/v1/deadlines"
		}
		return readOwnerJSON(ctx, o, list+"?"+query.Encode())
	}
	if action == "show" {
		if len(pos) != 1 {
			return usagef("show takes an ID")
		}
		return readOwnerJSON(ctx, o, path+url.PathEscape(pos[0]))
	}
	body, err := issueInput(e, *data, *file)
	if err != nil {
		return err
	}
	if len(body) == 0 {
		body = []byte(`{}`)
	}
	if action == "replay" {
		var env protocol.Envelope
		if err := json.Unmarshal(body, &env); err != nil {
			return usagef("original Envelope required")
		}
		if env.Operation != op && !(deadline && env.Operation == "issues.deadlines_cancel") {
			return usagef("wrong replay operation")
		}
		if *dry {
			return o.printJSON(env)
		}
		return sendIssueEnvelope(ctx, o, env)
	}
	if action != "put" && !(deadline && action == "cancel") {
		return usagef("unknown %s action", noun)
	}
	if len(pos) > 1 || *revision == 0 || *revision < -1 {
		return usagef("put accepts at most one ID and positive revision")
	}
	if action == "cancel" {
		op = "issues.deadlines_cancel"
		if len(pos) != 1 {
			return usagef("cancel takes a deadline ID")
		}
	}
	if len(pos) == 1 {
		target.ID = pos[0]
		if *request != "" && *revision < 1 {
			return usagef("retry requires original --revision")
		}
		if *revision < 1 {
			var current struct {
				Revision int64 `json:"revision"`
			}
			if err := c.do(ctx, "GET", path+url.PathEscape(target.ID), nil, &current); err != nil {
				return err
			}
			*revision = current.Revision
		}
	} else if *revision != -1 {
		return usagef("new record has no current revision")
	}
	if *request == "" {
		*request = e.IDs.New(protocol.PrefixRequest)
	} else if !protocol.ValidID(protocol.PrefixRequest, *request) {
		return usagef("invalid request identity")
	}
	env := protocol.Envelope{RequestID: *request, Operation: op, Target: target, Payload: body, Reason: *reason}
	if target.ID != "" {
		env.ExpectedControlRevision = revision
	}
	if *dry {
		return o.printJSON(env)
	}
	return sendIssueEnvelope(ctx, o, env)
}
