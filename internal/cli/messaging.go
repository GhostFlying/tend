package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

func runMessaging(ctx context.Context, e *Env, kind string, args []string) error {
	if len(args) == 0 {
		return usagef("%s requires an action", kind)
	}
	action := args[0]
	tail := args[1:]
	if kind == "channel" && action == "telegram" {
		if len(tail) == 0 {
			return usagef("telegram requires bind or unbind")
		}
		action = tail[0]
		tail = tail[1:]
	}
	fs := newFlags(e, kind+" "+action)
	reason := fs.String("reason", "", "reason retained in the operation record")
	dry := fs.Bool("dry-run", false, "print original Envelope without sending")
	fs.Bool("json", true, "print structured response")
	file := fs.String("file", "", "JSON payload file or - for stdin")
	data := fs.String("data", "", "JSON payload")
	request := fs.String("request-id", "", "original request ID for identical retry")
	revision := fs.Int64("revision", -1, "channel binding revision; required for an identical retry")
	user := fs.Int64("user-id", 0, "numeric Telegram owner user ID")
	after := fs.Int64("after", 0, "message or event cursor")
	channel := fs.String("channel", "", "channel filter")
	conversation := fs.String("conversation", "", "conversation filter")
	issue := fs.String("issue", "", "Issue filter")
	pos, err := parse(fs, tail)
	if err != nil {
		return err
	}
	c, err := e.client(protocol.ScopeOwner)
	if err != nil {
		return err
	}
	o := owner{e: e, c: c, json: true}
	base := "/api/v1/messaging/"
	path := ""
	op := ""
	target := protocol.Target{Type: "message"}
	if kind == "channel" {
		switch action {
		case "list":
			if len(pos) != 0 {
				return usagef("channel list takes no ID")
			}
			path = base + "channels"
		case "show":
			if len(pos) != 1 {
				return usagef("channel show needs one channel ID")
			}
			path = base + "channels/" + url.PathEscape(pos[0])
		case "bind", "unbind":
			if len(pos) != 0 {
				return usagef("binding takes no positional ID")
			}
			op = "messaging." + action
			target = protocol.Target{Type: "channel", ID: "telegram"}
		default:
			return usagef("unknown channel action")
		}
	} else {
		switch action {
		case "list":
			if len(pos) != 0 {
				return usagef("message list takes no ID")
			}
			q := url.Values{"after": {strconv.FormatInt(*after, 10)}, "channel_id": {*channel}, "conversation_id": {*conversation}, "issue_id": {*issue}}
			path = base + "messages?" + q.Encode()
		case "show", "receipts", "trace":
			if len(pos) != 1 {
				return usagef("message %s needs one message ID", action)
			}
			path = base + "messages/" + url.PathEscape(pos[0])
			if action != "show" {
				path += "/" + action
			}
		case "send", "notify":
			if len(pos) != 0 {
				return usagef("%s takes its content from --file or --data", action)
			}
			op = "messaging." + action
		case "reply", "read":
			if len(pos) != 1 {
				return usagef("%s needs one message ID", action)
			}
			op = "messaging." + action
			target.ID = pos[0]
		case "watch":
			if len(pos) != 0 {
				return usagef("watch takes no ID")
			}
			return messagingWatch(ctx, c, e, base+"events?after="+strconv.FormatInt(*after, 10))
		default:
			return usagef("unknown message action")
		}
	}
	if *after < 0 {
		return usagef("after must be nonnegative")
	}
	if path != "" {
		if *request != "" || *file != "" || *data != "" || *revision != -1 || *user != 0 {
			return usagef("read commands take no mutation input")
		}
		return readOwnerJSON(ctx, o, path)
	}
	if *channel != "" || *conversation != "" || *issue != "" || *after != 0 {
		return usagef("filters are only valid for list/watch")
	}
	body, err := issueInput(e, *data, *file)
	if err != nil {
		return err
	}
	if action == "bind" || action == "unbind" {
		if len(body) > 0 {
			return usagef("binding uses --user-id")
		}
		body, _ = json.Marshal(protocol.BindTelegram{UserID: *user})
		if *revision < 0 {
			if *request != "" {
				return usagef("binding retry requires original --revision")
			}
			var v protocol.MessagingChannel
			if err = c.do(ctx, "GET", base+"channels/telegram", nil, &v); err != nil {
				return err
			}
			*revision = v.Revision
		}
	}
	if action == "read" && len(body) == 0 {
		if *request != "" {
			return usagef("read retry requires the original content_revision payload")
		}
		var m protocol.MessagingMessage
		if err = c.do(ctx, "GET", base+"messages/"+url.PathEscape(target.ID), nil, &m); err != nil {
			return err
		}
		body, _ = json.Marshal(protocol.MessagingReadPayload{ContentRevision: m.ContentRevision})
	}
	if len(body) == 0 {
		return usagef("provide a JSON payload with --file or --data")
	}
	if *request == "" {
		*request = e.IDs.New(protocol.PrefixRequest)
	}
	env := protocol.Envelope{RequestID: *request, Operation: op, Target: target, Payload: body, Reason: *reason}
	if action == "bind" || action == "unbind" {
		env.ExpectedControlRevision = revision
	}
	if *dry {
		return o.printJSON(env)
	}
	return sendDirectEnvelope(ctx, o, env)
}

func messagingWatch(ctx context.Context, c *client, e *Env, path string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimSuffix(c.server, "/")+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if c.deploymentID != "" {
		req.Header.Set("X-Tend-Deployment-ID", c.deploymentID)
	}
	// The owner's ordinary HTTP timeout bounds individual requests, not an
	// intentionally persistent stream. Caller cancellation still owns its life.
	streamClient := *c.http
	streamClient.Timeout = 0
	resp, err := streamClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("messaging stream HTTP %d", resp.StatusCode)
	}
	scan := bufio.NewScanner(resp.Body)
	scan.Buffer(make([]byte, 4096), 2<<20)
	for scan.Scan() {
		if data, ok := strings.CutPrefix(scan.Text(), "data: "); ok {
			if _, err = fmt.Fprintln(e.Stdout, data); err != nil {
				return err
			}
		}
	}
	return scan.Err()
}
