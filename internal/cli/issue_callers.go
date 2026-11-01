package cli

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"

	"github.com/GhostFlying/tend/internal/protocol"
)

func runIssueCaller(ctx context.Context, e *Env, args []string) error {
	action := args[0]
	tail := args[1:]
	control := ""
	if action == "followup" {
		if len(tail) == 0 {
			return usagef("issue followup expects create|update|pause|resume|cancel|trigger")
		}
		control = tail[0]
		tail = tail[1:]
	}
	fs := newFlags(e, "issue "+action)
	fs.Bool("json", true, "print the complete structured result")
	data := fs.String("data", "", "JSON payload")
	file := fs.String("file", "", "JSON payload file; - reads stdin")
	request := fs.String("request-id", "", "original request identity for retry")
	revision := fs.Int64("revision", -1, "original Issue or rule revision")
	epoch := fs.Int64("epoch", -1, "original assignment epoch")
	reason := fs.String("reason", "", "reason for changing this rule")
	dry := fs.Bool("dry-run", false, "print the exact Envelope without sending")
	after := fs.String("after", "", "followup cursor")
	afterSource := fs.Int64("after-source", 0, "source message sequence cursor")
	afterRevision := fs.Int64("after-revision", 0, "history revision cursor")
	afterOccurrence := fs.String("after-occurrence", "", "occurrence history cursor")
	pos, err := parse(fs, tail)
	if err != nil {
		return err
	}
	want := 1
	if action == "followup-history" || action == "followup" && control != "create" {
		want = 2
	}
	if len(pos) != want {
		return usagef("issue %s requires an Issue ID%s", action, map[bool]string{true: " and rule ID"}[want == 2])
	}
	c, err := e.client(protocol.ScopeOwner)
	if err != nil {
		return err
	}
	base := "/api/v1/issues/" + url.PathEscape(pos[0])
	query := url.Values{}
	var path string
	var value any
	switch action {
	case "context":
		query.Set("after", strconv.FormatInt(*afterSource, 10))
		path = base + "/context"
		value = &protocol.IssueContextView{}
	case "followups":
		query.Set("after", *after)
		path = base + "/followups"
		value = &protocol.IssueFollowupPage{}
	case "followup-history":
		query.Set("after_revision", strconv.FormatInt(*afterRevision, 10))
		query.Set("after_occurrence", *afterOccurrence)
		path = base + "/followups/" + url.PathEscape(pos[1]) + "/history"
		value = &protocol.IssueFollowupHistory{}
	case "deliveries":
		path = base + "/deliveries"
		value = &protocol.IssueDeliveryPage{}
	}
	if path != "" {
		if len(query) > 0 {
			path += "?" + query.Encode()
		}
		if err := c.do(ctx, "GET", path, nil, value); err != nil {
			return err
		}
		return json.NewEncoder(e.Stdout).Encode(value)
	}
	operation := "issues.refresh_delivery"
	target := protocol.Target{Type: "issue", ID: pos[0]}
	if action == "followup" {
		switch control {
		case "create", "update", "pause", "resume", "cancel", "trigger":
		default:
			return usagef("unknown followup action %q", control)
		}
		operation = "issues.followups_" + control
		if control != "create" {
			target = protocol.Target{Type: "followup", ID: pos[1]}
		}
	}
	if *revision == 0 || *revision < -1 || *epoch == 0 || *epoch < -1 {
		return usagef("revision and epoch must be positive")
	}
	body, err := issueInput(e, *data, *file)
	if err != nil {
		return err
	}
	if len(body) == 0 {
		body = []byte(`{}`)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return usagef("payload must be a JSON object")
	}
	var supplied int64
	if raw, ok := fields["expected_assignment_epoch"]; ok {
		if json.Unmarshal(raw, &supplied) != nil || supplied < 1 {
			return usagef("expected_assignment_epoch must be positive")
		}
	}
	if *epoch > 0 {
		if supplied > 0 && supplied != *epoch {
			return usagef("--epoch differs from payload")
		}
		supplied = *epoch
	}
	if *request != "" && (*revision < 1 || supplied < 1) {
		return usagef("retry requires original --revision and --epoch")
	}
	if *revision < 1 || supplied < 1 {
		var issue protocol.IssueView
		if err := c.do(ctx, "GET", base, nil, &issue); err != nil {
			return err
		}
		if supplied < 1 {
			supplied = issue.AssignmentEpoch
		}
		if *revision < 1 {
			*revision = issue.Revision
			if target.Type == "followup" {
				var h protocol.IssueFollowupHistory
				if err := c.do(ctx, "GET", base+"/followups/"+url.PathEscape(pos[1])+"/history", nil, &h); err != nil {
					return err
				}
				*revision = h.Rule.Revision
			}
		}
	}
	fields["expected_assignment_epoch"], _ = json.Marshal(supplied)
	payload, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	if *request == "" {
		*request = e.IDs.New(protocol.PrefixRequest)
	}
	env := protocol.Envelope{RequestID: *request, Operation: operation, Target: target, ExpectedControlRevision: revision, Reason: *reason, Payload: payload}
	if *dry {
		return json.NewEncoder(e.Stdout).Encode(env)
	}
	return sendIssueEnvelope(ctx, owner{e: e, c: c, json: true}, env)
}
