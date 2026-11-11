package cli

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"

	"github.com/GhostFlying/tend/internal/protocol"
)

func runAttention(ctx context.Context, e *Env, args []string) error {
	if len(args) == 0 {
		return usagef("attention expects list|show|create|update|respond|resolve|escalate|confirm|withdraw|read|archive|replay")
	}
	action := args[0]
	fs := newFlags(e, "attention "+action)
	identity := fs.String("id", "", "stable new business object ID; retain it across submissions")
	data := fs.String("data", "", "inline JSON payload or original Envelope; latest_action_at omitted preserves, 0 clears the deadline")
	file := fs.String("file", "", "JSON file; - reads stdin")
	req := fs.String("request-id", "", "original request identity for retry")
	revision := fs.Int64("revision", -1, "attention revision; resolve/escalate use responsibility.revision")
	epoch := fs.Int64("epoch", -1, "resolve/escalate expected responsibility assignment epoch")
	inferredEpoch := false
	reason := fs.String("reason", "", "explicit control reason")
	dry := fs.Bool("dry-run", false, "print the complete request without sending")
	issue := fs.String("issue", "", "origin Issue filter")
	recipient := fs.String("recipient", "owner", "owner, agent, main or all")
	responsibleIssue := fs.String("responsible-issue", "", "current handling Issue filter")
	status := fs.String("status", "", "business state filter")
	kind := fs.String("kind", "", "attention kind filter")
	actionable := fs.Bool("actionable-only", false, "only decisions and actions requiring a response")
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
		q := url.Values{"issue_id": {*issue}, "status": {*status}, "kind": {*kind}, "limit": {strconv.Itoa(*limit)}, "recipient": {*recipient}, "responsible_issue_id": {*responsibleIssue}}
		if *after != "" {
			q.Set("after", *after)
		}
		if *all {
			q.Set("include_archived", "true")
		}
		if *actionable {
			q.Set("actionable_only", "true")
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
		if err := validateBusinessEnvelope(env); err != nil {
			return err
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
		target.ID = *identity
	} else if *identity != "" {
		return usagef("--id is only valid for create")
	}
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
			if action == "resolve" || action == "escalate" {
				if current.Responsibility == nil {
					return usagef("attention has no delegated responsibility")
				}
				*revision = current.Responsibility.Revision
				if *epoch < 0 && *req == "" {
					*epoch = current.Responsibility.AssignmentEpoch
					inferredEpoch = true
				}
			}
		}
	}
	if len(body) == 0 {
		body = []byte(`{}`)
	}
	if !json.Valid(body) {
		return usagef("payload must be JSON")
	}
	if action == "resolve" || action == "escalate" {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
			return usagef("handling payload must be a JSON object")
		}
		if *epoch >= 0 {
			if _, explicit := fields["expected_assignment_epoch"]; !explicit || !inferredEpoch {
				fields["expected_assignment_epoch"], _ = json.Marshal(*epoch)
			}
		}
		if *reason != "" {
			if _, found := fields["reason"]; !found {
				fields["reason"], _ = json.Marshal(*reason)
			}
		}
		if _, ok := fields["expected_assignment_epoch"]; !ok {
			return usagef("resolve/escalate require the original --epoch or expected_assignment_epoch with an explicit --revision")
		}
		body, _ = json.Marshal(fields)
	}
	allocateIdentity := *req == ""
	if *req == "" {
		*req = e.IDs.New(protocol.PrefixRequest)
	} else if !protocol.ValidID(protocol.PrefixRequest, *req) {
		return usagef("invalid request identity")
	}
	env := protocol.Envelope{RequestID: *req, Operation: op, Target: target, Reason: *reason, Payload: body}
	if action != "create" {
		env.ExpectedControlRevision = revision
	}
	if err := prepareBusinessEnvelope(e, &env, allocateIdentity); err != nil {
		return err
	}
	if *dry {
		return o.printJSON(env)
	}
	return sendIssueEnvelope(ctx, o, env)
}
func attentionOperation(op string) bool {
	switch op {
	case "attentions.create", "attentions.update", "attentions.respond", "attentions.confirm", "attentions.withdraw", "attentions.present", "attentions.resolve", "attentions.escalate":
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
