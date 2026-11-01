package cli

import (
	"context"
	"encoding/json"
	"github.com/GhostFlying/tend/internal/protocol"
	"net/url"
	"strconv"
)

func runAttention(ctx context.Context, e *Env, args []string) error {
	if len(args) == 0 {
		return usagef("attention expects list|show|create|update|respond|confirm|withdraw|read|archive|replay")
	}
	action := args[0]
	fs := newFlags(e, "attention "+action)
	data := fs.String("data", "", "inline JSON payload or original Envelope; latest_action_at omitted preserves, 0 clears the deadline")
	file := fs.String("file", "", "JSON file; - reads stdin")
	req := fs.String("request-id", "", "original request identity for retry")
	revision := fs.Int64("revision", -1, "original expected attention revision")
	reason := fs.String("reason", "", "explicit control reason")
	dry := fs.Bool("dry-run", false, "print the complete request without sending")
	issue := fs.String("issue", "", "Issue filter")
	status := fs.String("status", "", "business state filter")
	kind := fs.String("kind", "", "attention kind filter")
	limit := fs.Int("limit", 50, "page size, 1..100")
	fs.Bool("json", true, "print structured response")
	all := fs.Bool("all", false, "include archived")
	after := fs.String("after", "", "continuation cursor from the previous page")
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
		if len(pos) != 0 {
			return usagef("attention list takes no ID")
		}
		q := url.Values{"issue_id": {*issue}, "status": {*status}, "kind": {*kind}, "limit": {strconv.Itoa(*limit)}}
		if *after != "" {
			q.Set("after", *after)
		}
		if *all {
			q.Set("include_archived", "true")
		}
		return readOwnerJSON(ctx, o, "/api/v1/attentions?"+q.Encode())
	}
	if action == "show" {
		if len(pos) != 1 {
			return usagef("attention show takes an ID")
		}
		return readOwnerJSON(ctx, o, "/api/v1/attentions/"+url.PathEscape(pos[0]))
	}
	body, err := issueInput(e, *data, *file)
	if err != nil {
		return err
	}
	if action == "replay" {
		var env protocol.Envelope
		if err := json.Unmarshal(body, &env); err != nil {
			return usagef("replay requires the original Envelope")
		}
		if !attentionOperation(env.Operation) {
			return usagef("not an attention operation")
		}
		if *dry {
			return o.printJSON(env)
		}
		return sendIssueEnvelope(ctx, o, env)
	}
	op := "attentions." + action
	if action == "read" || action == "archive" {
		op = "attentions.present"
		if len(body) == 0 {
			if action == "read" {
				body = []byte(`{"read":true}`)
			} else {
				body = []byte(`{"archived":true}`)
			}
		}
	}
	if !attentionOperation(op) {
		return usagef("unknown attention action")
	}
	target := protocol.Target{Type: "attention"}
	if action == "create" {
		if len(pos) != 0 || *revision != -1 {
			return usagef("new attention has no ID or revision")
		}
	} else {
		if len(pos) != 1 || *revision == 0 || *revision < -1 {
			return usagef("attention control requires one ID and positive revision")
		}
		target.ID = pos[0]
		if *req != "" && *revision < 1 {
			return usagef("retry requires the original --revision")
		}
		if *revision < 1 {
			var current protocol.AttentionView
			if err := c.do(ctx, "GET", "/api/v1/attentions/"+url.PathEscape(target.ID), nil, &current); err != nil {
				return err
			}
			*revision = current.Revision
		}
	}
	if len(body) == 0 {
		body = []byte(`{}`)
	}
	if !json.Valid(body) {
		return usagef("payload must be JSON")
	}
	if *req == "" {
		*req = e.IDs.New(protocol.PrefixRequest)
	} else if !protocol.ValidID(protocol.PrefixRequest, *req) {
		return usagef("invalid request identity")
	}
	env := protocol.Envelope{RequestID: *req, Operation: op, Target: target, Reason: *reason, Payload: body}
	if action != "create" {
		env.ExpectedControlRevision = revision
	}
	if *dry {
		return o.printJSON(env)
	}
	return sendIssueEnvelope(ctx, o, env)
}
func attentionOperation(op string) bool {
	switch op {
	case "attentions.create", "attentions.update", "attentions.respond", "attentions.confirm", "attentions.withdraw", "attentions.present":
		return true
	}
	return false
}

func readOwnerJSON(ctx context.Context, o owner, path string) error {
	var raw json.RawMessage
	if err := o.c.do(ctx, "GET", path, nil, &raw); err != nil {
		return err
	}
	return o.printJSON(raw)
}
