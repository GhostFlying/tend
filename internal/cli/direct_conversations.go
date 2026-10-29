package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"github.com/GhostFlying/tend/internal/protocol"
)

// runDirectConversation keeps frozen request/revision input explicit. It uses
// the same owner HTTP client and app envelope rather than an agent identity.
func runDirectConversation(ctx context.Context, e *Env, kind string, args []string) error {
	if len(args) == 0 {
		return usagef("%s needs an action", kind)
	}
	action := args[0]
	fs := newFlags(e, kind+" "+action)
	data := fs.String("data", "", "JSON payload")
	file := fs.String("file", "", "JSON payload file or - for stdin")
	request := fs.String("request-id", "", "original request identity for an identical retry")
	revision := fs.Int64("revision", -1, "original task control revision (run message only)")
	after := fs.Int64("after", 0, "message sequence cursor")
	provider := fs.String("provider", "", "optional native entry provider: orca or cove")
	pos, err := parse(fs, args[1:])
	if err != nil {
		return err
	}
	c, err := e.client(protocol.ScopeOwner)
	if err != nil {
		return err
	}
	o := owner{e: e, c: c, json: true}
	path := ""
	switch kind + " " + action {
	case "conversation list":
		if len(pos) != 0 {
			return usagef("conversation list takes no ID")
		}
		path = "/api/v1/agent-conversations"
	case "conversation show":
		if len(pos) != 1 {
			return usagef("conversation show takes one conversation ID")
		}
		path = "/api/v1/conversation?conversation_id=" + url.QueryEscape(pos[0]) + fmt.Sprintf("&after=%d", *after)
	case "run native-entry":
		if len(pos) != 1 {
			return usagef("run native-entry takes one exact run ID")
		}
		path = "/api/v1/runs/" + url.PathEscape(pos[0]) + "/native-entry?provider=" + url.QueryEscape(*provider)
	case "run messages":
		if len(pos) != 1 {
			return usagef("run messages takes one exact run ID")
		}
		path = "/api/v1/runs/" + url.PathEscape(pos[0]) + fmt.Sprintf("/messages?after=%d", *after)
	case "source show":
		if len(pos) != 1 {
			return usagef("source show takes one source message ID")
		}
		path = "/api/v1/sources/" + url.PathEscape(pos[0])
	}
	if *provider != "" && kind+" "+action != "run native-entry" {
		return usagef("provider is only valid for native-entry")
	}
	if path != "" {
		if *data != "" || *file != "" || *request != "" || *revision != -1 {
			return usagef("read commands take no mutation input")
		}
		var v json.RawMessage
		if err := c.do(ctx, "GET", path, nil, &v); err != nil {
			return err
		}
		return o.printJSON(v)
	}
	body, err := issueInput(e, *data, *file)
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return usagef("provide the JSON payload with --data or --file")
	}
	target := protocol.Target{}
	operation := ""
	switch kind + " " + action {
	case "run message":
		if len(pos) != 1 || *revision < 1 {
			return usagef("run message requires exact run ID and --revision; payload names task/spec/generation/text")
		}
		operation = protocol.OpRunsSendMessage
		target = protocol.Target{Type: "run", ID: pos[0]}
	case "conversation consult":
		if len(pos) != 0 {
			return usagef("conversation consult takes its registered agent_ref and title in the JSON payload")
		}
		operation = "conversation.consult"
		target.Type = "conversation"
	case "conversation promote":
		if len(pos) != 1 {
			return usagef("conversation promote needs the exact consultation ID and selected source versions")
		}
		operation = "conversation.promote"
		target = protocol.Target{Type: "conversation", ID: pos[0]}
	case "conversation send":
		if len(pos) != 1 {
			return usagef("conversation send requires explicit conversation ID")
		}
		body, err = directRecipient(body, pos[0])
		if err != nil {
			return err
		}
		operation = "conversation.submit_message"
		target.Type = "message"
	case "source inject":
		if len(pos) != 1 {
			return usagef("source inject requires explicit conversation ID")
		}
		body, err = directRecipient(body, pos[0])
		if err != nil {
			return err
		}
		operation = "conversation.inject_source"
		target.Type = "message"
	default:
		return usagef("unknown %s action %q", kind, action)
	}
	if operation != protocol.OpRunsSendMessage && *revision != -1 {
		return usagef("revision is only valid for a run supplement")
	}
	if *request == "" {
		*request = e.IDs.New(protocol.PrefixRequest)
	}
	if !protocol.ValidID(protocol.PrefixRequest, *request) {
		return usagef("request-id is malformed")
	}
	env := protocol.Envelope{RequestID: *request, Operation: operation, Target: target, Payload: body}
	if *revision >= 0 {
		env.ExpectedControlRevision = revision
	}
	return sendDirectEnvelope(ctx, o, env)
}
func sendDirectEnvelope(ctx context.Context, o owner, env protocol.Envelope) error {
	fmt.Fprintf(o.e.Stderr, "request %s (%s)", env.RequestID, env.Operation)
	if env.ExpectedControlRevision != nil {
		fmt.Fprintf(o.e.Stderr, " --revision %d", *env.ExpectedControlRevision)
	}
	fmt.Fprintln(o.e.Stderr, "; preserve this identity and payload for any retry")
	result, err := o.sendEnvelope(ctx, env)
	if err != nil {
		return err
	}
	if err := o.printJSON(result); err != nil {
		return err
	}
	if result.Error != nil {
		return result.Error
	}
	if result.Operation == nil {
		return errors.New("server returned no operation")
	}
	if result.Operation.Error != nil {
		return result.Operation.Error
	}
	if result.Operation.Status != protocol.OpSucceeded && result.Operation.Status != protocol.OpAccepted {
		return protocol.Errorf(protocol.CodeUnknownOutcome, "request outcome is unknown; inspect the original request before any new action")
	}
	return nil
}

// Keep unrecognized input intact for the server's strict payload decoder.
func directRecipient(raw []byte, id string) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		return nil, usagef("payload must be a JSON object")
	}
	if v, ok := fields["conversation_id"]; ok {
		var selected string
		if err := json.Unmarshal(v, &selected); err != nil {
			return nil, err
		}
		if selected != "" && selected != id {
			return nil, usagef("payload recipient differs from explicit conversation")
		}
	}
	fields["conversation_id"], _ = json.Marshal(id)
	return json.Marshal(fields)
}
