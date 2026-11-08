package cli

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
)

func runConversation(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 && args[0] != "handoff" {
		return runDirectConversation(ctx, e, "conversation", args)
	}
	if len(args) == 0 || args[0] != "handoff" {
		return usagef("conversation expects handoff [--status] [--json]")
	}
	fs := newFlags(e, "conversation handoff")
	request := fs.String("request-id", "", "original request ID for an identical retry")
	dry := fs.Bool("dry-run", false, "print the original Envelope without sending")
	status := fs.Bool("status", false, "show indexed handoff status")
	asJSON := fs.Bool("json", false, "print the server's answer as JSON")
	pos, err := parse(fs, args[1:])
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return usagef("handoff takes no positional arguments")
	}
	c, err := e.client(protocol.ScopeOwner)
	if err != nil {
		return err
	}
	o := owner{e: e, c: c, json: *asJSON}
	if *status {
		if *request != "" || *dry {
			return usagef("status takes no mutation input")
		}
		var view protocol.ConversationHandoffView
		if err := c.do(ctx, "GET", "/api/v1/conversation/handoff", nil, &view); err != nil {
			return err
		}
		if *asJSON {
			return o.printJSON(view)
		}
		if !view.Enabled {
			_, err = fmt.Fprintln(e.Stdout, "Hosted conversation handoff is disabled.")
			return err
		}
		fmt.Fprintf(e.Stdout, "Handoff mode: index; pending: %t; generation: %d\n", view.Pending, view.Generation)
		if v := view.Maintenance; v != nil {
			fmt.Fprintf(e.Stdout, "Maintenance %s: %s (request %s)\n", v.Operation, v.Status, v.RequestID)
			if v.Reason != "" {
				fmt.Fprintf(e.Stdout, "Reason: %s\n", v.Reason)
			}
		}
		return nil
	}
	body, _ := json.Marshal(protocol.ConversationHandoffPayload{})
	if *request == "" {
		*request = e.IDs.New(protocol.PrefixRequest)
	}
	if !protocol.ValidID(protocol.PrefixRequest, *request) {
		return usagef("malformed request ID")
	}
	env := protocol.Envelope{RequestID: *request, Operation: "conversation.handoff", Target: protocol.Target{Type: "conversation"}, Payload: body}
	if *dry {
		return o.printJSON(env)
	}
	return sendDirectEnvelope(ctx, o, env)
}
