package cli

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"

	"github.com/GhostFlying/tend/internal/protocol"
)

func runFeishu(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 && args[0] == "inputs" {
		return runFeishuInputs(ctx, e, args[1:])
	}
	if len(args) == 0 {
		return usagef("channel feishu expects show, bind, unbind or inputs")
	}
	action := args[0]
	if action != "show" && action != "bind" && action != "unbind" {
		return usagef("unknown Feishu action")
	}
	fs := newFlags(e, "channel feishu "+action)
	appID := fs.String("app-id", "", "exact Feishu application ID")
	tenant := fs.String("tenant-key", "", "exact tenant ID")
	sender := fs.String("sender-id", "", "authorized owner's open_id")
	chat := fs.String("chat-id", "", "fixed private owner chat ID")
	request := fs.String("request-id", "", "original request ID for an identical retry")
	revision := fs.Int64("revision", -1, "original binding CAS revision; required for retry")
	reason := fs.String("reason", "", "reason retained in the operation record")
	dry := fs.Bool("dry-run", false, "print the complete original Envelope without mutation")
	fs.Bool("json", true, "print structured binding or operation")
	pos, err := parse(fs, args[1:])
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return usagef("Feishu commands take no positional IDs")
	}
	c, err := e.client(protocol.ScopeOwner)
	if err != nil {
		return err
	}
	o := owner{e: e, c: c, json: true}
	if action == "show" {
		if *request != "" || *revision != -1 || *appID != "" || *tenant != "" || *sender != "" || *chat != "" || *dry || *reason != "" {
			return usagef("show takes no mutation flags")
		}
		return readOwnerJSON(ctx, o, "/api/v1/messaging/feishu")
	}
	if *revision < -1 {
		return usagef("revision must be nonnegative")
	}
	if *request != "" && *revision < 0 {
		return usagef("retry needs the original revision")
	}
	input := protocol.BindFeishu{AppID: *appID, TenantKey: *tenant, SenderID: *sender, ChatID: *chat}
	if action == "bind" && (*appID == "" || *tenant == "" || *sender == "" || *chat == "") {
		return usagef("bind requires app-id, tenant-key, sender-id and chat-id")
	}
	if action == "unbind" && (*appID != "" || *tenant != "" || *sender != "" || *chat != "") {
		return usagef("unbind takes no new destination")
	}
	if *revision < 0 {
		var b protocol.FeishuBindingView
		if err = c.do(ctx, "GET", "/api/v1/messaging/feishu", nil, &b); err != nil {
			return err
		}
		*revision = b.Revision
	}
	if *request == "" {
		*request = e.IDs.New(protocol.PrefixRequest)
	}
	body, _ := json.Marshal(input)
	env := protocol.Envelope{RequestID: *request, Operation: "messaging.feishu_" + action, Target: protocol.Target{Type: "channel", ID: "feishu"}, ExpectedControlRevision: revision, Payload: body, Reason: *reason}
	if *dry {
		return o.printJSON(env)
	}
	return sendDirectEnvelope(ctx, o, env)
}

func runFeishuInputs(ctx context.Context, e *Env, args []string) error {
	fs := newFlags(e, "channel feishu inputs")
	after := fs.Int64("after", 0, "exclusive input sequence cursor")
	limit := fs.Int("limit", 100, "page size, 1..100")
	status := fs.String("status", "", "pending|accepted|unknown|succeeded|failed")
	fs.Bool("json", true, "print structured input/request state")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 || *after < 0 || *limit < 1 || *limit > 100 {
		return usagef("invalid input pagination")
	}
	switch *status {
	case "", "pending", "accepted", "unknown", "succeeded", "failed":
	default:
		return usagef("invalid input status")
	}
	c, err := e.client(protocol.ScopeOwner)
	if err != nil {
		return err
	}
	q := url.Values{"after": {strconv.FormatInt(*after, 10)}, "limit": {strconv.Itoa(*limit)}, "status": {*status}}
	return readOwnerJSON(ctx, owner{e: e, c: c, json: true}, "/api/v1/messaging/feishu/inputs?"+q.Encode())
}
