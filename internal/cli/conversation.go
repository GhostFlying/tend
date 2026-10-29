package cli

import (
	"context"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
)

func runConversation(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 && args[0] != "handoff" {
		return runDirectConversation(ctx, e, "conversation", args)
	}
	if len(args) == 0 || args[0] != "handoff" {
		return usagef("conversation expects handoff [--prepare-only | --status] [--json]")
	}
	fs := newFlags(e, "conversation handoff")
	prepare := fs.Bool("prepare-only", false, "prepare a summary without switching the native thread")
	status := fs.Bool("status", false, "show shared handoff and summary status")
	asJSON := fs.Bool("json", false, "print the server's answer as JSON")
	pos, err := parse(fs, args[1:])
	if err != nil {
		return err
	}
	if len(pos) != 0 || (*status && *prepare) {
		return usagef("handoff takes no positional arguments; --status and --prepare-only are exclusive")
	}
	c, err := e.client(protocol.ScopeOwner)
	if err != nil {
		return err
	}
	o := owner{e: e, c: c, json: *asJSON}
	if *status {
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
		if view.IdleMinutes == 0 {
			fmt.Fprintln(e.Stdout, "Automatic summary preparation: disabled")
		} else {
			fmt.Fprintf(e.Stdout, "Automatic summary preparation: idle %d minutes\n", view.IdleMinutes)
		}
		fmt.Fprintf(e.Stdout, "Settings revision: %d; handoff pending: %t; preparing: %t\n", view.Revision, view.Pending, view.Preparing)
		if v := view.Maintenance; v != nil {
			fmt.Fprintf(e.Stdout, "Maintenance %s: %s (request %s)\n", v.Operation, v.Status, v.RequestID)
			if v.Reason != "" {
				fmt.Fprintf(e.Stdout, "Reason: %s\n", v.Reason)
			}
		}
		if view.Summary == nil {
			fmt.Fprintln(e.Stdout, "No prepared summary.")
		} else {
			fmt.Fprintf(e.Stdout, "Summary %s; current: %t\n", view.Summary.ID, view.Summary.Current)
		}
		return nil
	}
	return o.write(ctx, "conversation.handoff", protocol.Target{Type: "conversation"}, nil, protocol.ConversationHandoffPayload{PrepareOnly: *prepare})
}
