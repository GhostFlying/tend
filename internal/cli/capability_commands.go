package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/GhostFlying/tend/internal/protocol"
)

// capabilityCommand binds a business command to its actual owner interface.
// Existing command parsers remain responsible for their established output.
type capabilityCommand struct {
	Name          string
	Route         string
	Queries       string
	Operation     string
	Target        string
	RevisionRoute string
	RevisionField string
	JSONDefault   bool
	Download      bool
	Stream        bool
}

var capabilityCommands = []capabilityCommand{
	{Name: "inbox list", Route: "/api/v1/inbox", Queries: "issue=issue_id kind status include-archived=include_archived after limit"},
	{Name: "session show", Route: "/api/v1/session"},
	{Name: "deployment show", Route: "/api/v1/deployment"},
	{Name: "repo list", Route: "/api/v1/repos"},
	{Name: "task list", Route: "/api/v1/tasks", Queries: "all finished project lifecycle sort before limit"},
	{Name: "task show", Route: "/api/v1/tasks/{id}"},
	{Name: "run logs", Route: "/api/v1/runs/{id}/logs", Queries: "after wait"},
	{Name: "run rollout", Route: "/api/v1/runs/{id}/rollout", Queries: "after before"},
	{Name: "artifact show", Route: "/api/v1/artifacts/{id}"},
	{Name: "choice show", Route: "/api/v1/work-item-choices/{id}"},
	{Name: "conversation list", Route: "/api/v1/agent-conversations", Queries: "search status after", JSONDefault: true},
	{Name: "conversation info", Route: "/api/v1/agent-conversations/{id}"},
	{Name: "conversation show", Route: "/api/v1/conversation?conversation_id={id}", Queries: "before after limit", JSONDefault: true},
	{Name: "conversation history", Route: "/api/v1/conversation?conversation_id={id}", Queries: "before after limit"},
	{Name: "conversation queue", Route: "/api/v1/conversation/queue", Queries: "conversation=conversation_id category after"},
	{Name: "conversation transcript", Route: "/api/v1/conversation/transcript", Queries: "conversation=conversation_id before"},
	{Name: "conversation block", Route: "/api/v1/conversation/blocks/{id}", Queries: "after"},
	{Name: "conversation rollout", Route: "/api/v1/agent-conversations/{id}/rollout", Queries: "after turn=turn_id"},
	{Name: "conversation window", Route: "/api/v1/agent-conversations/{id}/rollout/{seq}", Queries: "offset"},
	{Name: "conversation usage", Route: "/api/v1/agent-conversations/{id}/usage"},
	{Name: "conversation compactions", Route: "/api/v1/agent-conversations/{id}/compactions", Queries: "before"},
	{Name: "conversation settings show", Route: "/api/v1/conversation/settings", Queries: "conversation=conversation_id"},
	{Name: "conversation handoff-settings show", Route: "/api/v1/conversation/handoff"},
	{Name: "conversation sources", Route: "/api/v1/background/{id}/sources", Queries: "after"},
	{Name: "message original", Route: "/api/v1/messages/{id}"},
	{Name: "message associations", Route: "/api/v1/messages/{id}/associations"},
	{Name: "message list", Route: "/api/v1/messaging/messages", Queries: "after before latest conversation=conversation_id issue=issue_id channel=channel_id", JSONDefault: true},
	{Name: "issue notifications show", Route: "/api/v1/issue-notification-settings"},
	{Name: "attachment metadata", Route: "/api/v1/conversation/attachments/{id}/metadata"},
	{Name: "attachment url", Route: "/api/v1/conversation/attachments/{id}/download-url", Queries: "inline"},
	{Name: "file metadata", Route: "/api/v1/files/{id}/metadata"},
	{Name: "file url", Route: "/api/v1/files/{id}/download-url", Queries: "inline"},
	{Name: "file download", Route: "/api/v1/files/{id}", Download: true},
	{Name: "image show", Route: "/api/v1/conversation/images/{id}"},
	{Name: "image url", Route: "/api/v1/conversation/images/{id}/download-url", Queries: "inline"},
	{Name: "image download", Route: "/api/v1/conversation/images/{id}/content", Download: true},
	{Name: "usage", Route: "/api/v1/token-usage", Queries: "window offset model subject before"},
	{Name: "coordination metrics", Route: "/api/v1/coordination/metrics"},
	{Name: "ledger watch", Route: "/api/v1/events", Queries: "after", Stream: true},
	{Name: "archive list", Route: "/api/v1/archives"},
	{Name: "archive show", Route: "/api/v1/archives/{archive}"},
	{Name: "archive records", Route: "/api/v1/archives/{archive}/records/{kind}", Queries: "after limit"},
	{Name: "archive record", Route: "/api/v1/archives/{archive}/records/{kind}/{id}"},
	{Name: "archive originals", Route: "/api/v1/archives/{archive}/originals/{kind}", Queries: "after limit"},
	{Name: "archive original", Route: "/api/v1/archives/{archive}/originals/{kind}/{id}/metadata"},
	{Name: "archive original-url", Route: "/api/v1/archives/{archive}/originals/{kind}/{id}/download-url", Queries: "inline"},
	{Name: "archive download", Route: "/api/v1/archives/{archive}/originals/{kind}/{id}", Download: true},
	{Name: "archive resources", Route: "/api/v1/archives/{archive}/resources", Queries: "after limit"},
	{Name: "archive resource-url", Route: "/api/v1/archives/{archive}/resources/{id}/files/{index}/download-url", Queries: "inline"},
	{Name: "archive resource-download", Route: "/api/v1/archives/{archive}/resources/{id}/files/{index}", Download: true},
	{Name: "upgrade show", Route: "/api/v1/upgrades/{id}"},
	{Name: "choice respond", Operation: "work_item_choices.respond", Target: "work_item_choice", RevisionRoute: "/api/v1/work-item-choices/{id}", RevisionField: "version"},
	{Name: "message respond", Operation: "messaging.respond", Target: "interaction"},
	{Name: "message associate", Operation: "conversation.set_issue_associations", Target: "message", RevisionRoute: "/api/v1/messages/{id}/associations", RevisionField: "revision"},
	{Name: "message withdraw", Operation: "conversation.withdraw_message", Target: "message", RevisionRoute: "/api/v1/messages/{id}", RevisionField: "control.revision"},
	{Name: "conversation interrupt", Operation: "conversation.interrupt_reply", Target: "message"},
	{Name: "conversation resume-processing", Operation: "conversation.resume_processing", Target: "logical_turn", RevisionRoute: "-"},
	{Name: "conversation compact", Operation: "conversation.compact", Target: "conversation", RevisionRoute: "/api/v1/agent-conversations/{id}", RevisionField: "revision"},
	{Name: "conversation settings set", Operation: "conversation.set_settings", Target: "conversation", RevisionRoute: "/api/v1/conversation/settings", RevisionField: "revision"},
	{Name: "conversation handoff-settings set", Operation: "conversation.set_handoff_settings", Target: "conversation", RevisionRoute: "/api/v1/conversation/handoff", RevisionField: "revision"},
	{Name: "issue notifications set", Operation: "issues.set_notification_settings", Target: "issue_settings", RevisionRoute: "/api/v1/issue-notification-settings", RevisionField: "revision"},
	{Name: "host maintenance", Operation: "hosts.maintenance", Target: "host", RevisionRoute: "/api/v1/snapshot", RevisionField: "host"},
	{Name: "upgrade control", Operation: "upgrades.control", Target: "upgrade", RevisionRoute: "/api/v1/upgrades/{id}", RevisionField: "upgrade.revision"},
}

func commandVariables(route string) []string {
	var out []string
	for {
		_, tail, ok := strings.Cut(route, "{")
		if !ok {
			return out
		}
		name, rest, ok := strings.Cut(tail, "}")
		if !ok {
			return out
		}
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
		route = rest
	}
}

func expandCommandRoute(route string, names, values []string) string {
	for i, name := range names {
		route = strings.ReplaceAll(route, "{"+name+"}", url.PathEscape(values[i]))
	}
	return route
}

func findCapabilityCommand(args []string) (capabilityCommand, []string, bool) {
	var selected capabilityCommand
	n := 0
	for _, command := range capabilityCommands {
		parts := strings.Fields(command.Name)
		if len(parts) > len(args) || len(parts) <= n {
			continue
		}
		if slices.Equal(parts, args[:len(parts)]) {
			selected = command
			n = len(parts)
		}
	}
	return selected, args[n:], n > 0
}

func runCapabilityCommand(ctx context.Context, e *Env, cmd capabilityCommand, args []string) error {
	fs := newFlags(e, cmd.Name)
	asJSON := fs.Bool("json", cmd.JSONDefault, "print the complete server response as JSON")
	out := fs.String("out", "", "download destination (required for download)")
	qstrings := map[string]*string{}
	qbools := map[string]*bool{}
	for _, field := range strings.Fields(cmd.Queries) {
		flag, param, ok := strings.Cut(field, "=")
		if !ok {
			param = flag
		}
		if slices.Contains([]string{"all", "finished", "latest", "inline", "include_archived"}, param) {
			qbools[param] = fs.Bool(flag, false, "enable "+param)
		} else {
			qstrings[param] = fs.String(flag, "", "server query parameter "+param)
		}
	}
	data := fs.String("data", "", "complete JSON payload")
	file := fs.String("file", "", "JSON payload file; - reads stdin")
	request := fs.String("request-id", "", "original request identity for an identical retry")
	revision := fs.Int64("revision", -1, "original expected control revision; first requests may read it")
	dry := fs.Bool("dry-run", false, "print the exact original Envelope without sending")
	reason := fs.String("reason", "", "reason retained in the request record")
	conversation := new(string)
	if cmd.Name == "conversation settings set" {
		conversation = fs.String("conversation", "", "exact conversation; default main")
	}
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if *revision < -1 {
		return usagef("revision must be nonnegative")
	}
	variables := commandVariables(cmd.Route)
	if cmd.Operation != "" && !slices.Contains([]string{"conversation.set_settings", "conversation.set_handoff_settings", "issues.set_notification_settings"}, cmd.Operation) {
		variables = []string{"id"}
	}
	if len(pos) != len(variables) {
		return usagef("%s requires %s", cmd.Name, strings.Join(variables, " "))
	}
	c, err := e.client(protocol.ScopeOwner)
	if err != nil {
		return err
	}
	if cmd.Operation == "" {
		if *data != "" || *file != "" || *request != "" || *revision != -1 || *dry || *reason != "" {
			return usagef("read commands take no mutation input")
		}
		if *out != "" && !cmd.Download {
			return usagef("out is only valid for downloads")
		}
		route := expandCommandRoute(cmd.Route, variables, pos)
		u, err := url.Parse(route)
		if err != nil {
			return err
		}
		query := u.Query()
		for k, v := range qstrings {
			if *v != "" {
				query.Set(k, *v)
			}
		}
		// Preserve explicit zero cursors such as --before 0.
		fs.Visit(func(f *flag.Flag) {
			for _, field := range strings.Fields(cmd.Queries) {
				name, param, ok := strings.Cut(field, "=")
				if !ok {
					param = name
				}
				if name == f.Name && qstrings[param] != nil {
					query.Set(param, *qstrings[param])
				}
			}
		})
		for k, v := range qbools {
			if *v {
				value := "1"
				if k == "include_archived" {
					value = "true"
				}
				query.Set(k, value)
			}
		}
		if cmd.Name == "message list" {
			if err := validateMessageListQuery(query); err != nil {
				return err
			}
		}
		if cmd.Name == "task list" {
			if err := normalizeTaskListQuery(query); err != nil {
				return err
			}
		}
		u.RawQuery = query.Encode()
		route = u.String()
		if cmd.Download {
			if *out == "" {
				return usagef("download requires --out")
			}
			return downloadOwnerFile(ctx, e, c, route, *out, *asJSON)
		}
		if cmd.Stream {
			return messagingWatch(ctx, c, e, route)
		}
		var value json.RawMessage
		if err := c.do(ctx, "GET", route, nil, &value); err != nil {
			return err
		}
		if cmd.Name == "task list" && !*asJSON {
			var page protocol.TaskPage
			if err := json.Unmarshal(value, &page); err != nil {
				return err
			}
			return printTaskPage(e, page)
		}
		return printCapabilityValue(e, value, *asJSON)
	}
	if *out != "" {
		return usagef("out is only valid for downloads")
	}
	body, err := issueInput(e, *data, *file)
	if err != nil {
		return err
	}
	if len(body) == 0 {
		body = []byte(`{}`)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || fields == nil {
		return usagef("payload must be a JSON object")
	}
	target := protocol.Target{Type: cmd.Target}
	if len(pos) > 0 {
		target.ID = pos[0]
	}
	if cmd.Name == "conversation settings set" {
		var supplied string
		if v, ok := fields["conversation_id"]; ok {
			if err := json.Unmarshal(v, &supplied); err != nil {
				return usagef("conversation_id must be a string")
			}
		}
		if *conversation != "" && supplied != "" && *conversation != supplied {
			return usagef("conversation differs from payload")
		}
		if *conversation == "" {
			*conversation = supplied
		}
		if *conversation != "" {
			fields["conversation_id"], _ = json.Marshal(*conversation)
		}
	}
	if *request != "" && !protocol.ValidID(protocol.PrefixRequest, *request) {
		return usagef("malformed request identity")
	}
	if cmd.RevisionRoute == "-" && *revision < 0 {
		return usagef("read the processing state and supply its original --revision")
	}
	if cmd.Operation == "conversation.interrupt_reply" {
		var message string
		if raw, ok := fields["message_id"]; ok {
			if json.Unmarshal(raw, &message) != nil || message != target.ID {
				return usagef("message_id differs from explicit message")
			}
		}
		fields["message_id"], _ = json.Marshal(target.ID)
	}
	if cmd.RevisionRoute != "" && *revision < 0 {
		if *request != "" {
			return usagef("retry requires original --revision")
		}
		route := expandCommandRoute(cmd.RevisionRoute, []string{"id"}, []string{target.ID})
		if *conversation != "" {
			route += "?conversation_id=" + url.QueryEscape(*conversation)
		}
		var value map[string]json.RawMessage
		if err = c.do(ctx, "GET", route, nil, &value); err != nil {
			return err
		}
		if cmd.RevisionField == "host" {
			var snap protocol.Snapshot
			raw, _ := json.Marshal(value)
			if err = json.Unmarshal(raw, &snap); err != nil {
				return err
			}
			found := false
			for _, host := range snap.Hosts {
				if host.HostID == target.ID {
					*revision = host.ControlRevision
					found = true
				}
			}
			if !found {
				return usagef("host has no maintenance control state")
			}
		} else {
			raw, _ := json.Marshal(value)
			for _, key := range strings.Split(cmd.RevisionField, ".") {
				var obj map[string]json.RawMessage
				if json.Unmarshal(raw, &obj) != nil {
					return errors.New("revision view is invalid")
				}
				raw = obj[key]
			}
			if json.Unmarshal(raw, revision) != nil {
				return errors.New("revision is unavailable; read the current object")
			}
		}
	}
	if cmd.RevisionRoute == "" && *revision != -1 {
		return usagef("this operation has no control revision")
	}
	if *request == "" {
		*request = e.IDs.New(protocol.PrefixRequest)
	}
	body, err = json.Marshal(fields)
	if err != nil {
		return err
	}
	env := protocol.Envelope{RequestID: *request, Operation: cmd.Operation, Target: target, Payload: body, Reason: *reason}
	if *revision >= 0 {
		env.ExpectedControlRevision = revision
	}
	if *dry {
		return (owner{e: e, json: true}).printJSON(env)
	}
	return sendDirectEnvelope(ctx, owner{e: e, c: c, json: true}, env)
}

func printCapabilityValue(e *Env, raw json.RawMessage, asJSON bool) error {
	if asJSON {
		return (owner{e: e, json: true}).printJSON(raw)
	}

	var formatted bytes.Buffer
	if err := json.Indent(&formatted, raw, "", "  "); err != nil {
		return err
	}
	_, err := fmt.Fprintln(e.Stdout, formatted.String())
	return err

}

// parseNonnegative is shared by local control flag parsing.
func parseNonnegative(raw string) (uint64, error) {
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, usagef("revision must be nonnegative")
	}
	return n, nil
}

// The paginated task endpoint is the common mode for all CLI filters. Its
// default lifecycle set keeps an unqualified CLI listing limited to open work.
func normalizeTaskListQuery(query url.Values) error {
	all, finished := query.Get("all") == "1", query.Get("finished") == "1"
	if all && finished {
		return usagef("all and finished select different task listing modes")
	}
	lifecycle := query.Get("lifecycle")
	if finished {
		if lifecycle == "" {
			lifecycle = string(protocol.TaskCompleted) + "," + string(protocol.TaskCancelled)
		}
		for _, state := range strings.Split(lifecycle, ",") {
			if !protocol.TaskLifecycle(state).Terminal() {
				return usagef("finished can select only completed or cancelled tasks")
			}
		}
	} else if !all && lifecycle == "" {
		lifecycle = strings.Join([]string{string(protocol.TaskDraft), string(protocol.TaskQueued), string(protocol.TaskRunning), string(protocol.TaskWaiting), string(protocol.TaskAwaitingAcceptance), string(protocol.TaskChangesRequired)}, ",")
	}
	query.Set("all", "1")
	query.Del("finished")
	if lifecycle != "" {
		query.Set("lifecycle", lifecycle)
	}
	return nil
}

func printTaskPage(e *Env, page protocol.TaskPage) error {
	writer := tabwriter.NewWriter(e.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "TASK\tTITLE\tSTATE\tDETAILS")
	for _, task := range page.Tasks {
		fmt.Fprintf(writer, "%s\t%s\t%s / %s\t%s\n", task.TaskID, strings.ReplaceAll(task.Title, "\n", " "), task.Lifecycle, task.ControlMode, blocked(task.BlockReason))
	}
	if len(page.Tasks) == 0 {
		fmt.Fprintln(writer, "No matching tasks.")
	}
	if page.Next != "" {
		fmt.Fprintf(writer, "Next cursor: %s\n", page.Next)
	}
	return writer.Flush()
}

// The API's recent-history view is global. Reject combinations it cannot
// honor; forward --after listings retain channel/conversation/Issue filters.
func validateMessageListQuery(query url.Values) error {
	if !query.Has("before") && query.Get("latest") != "1" {
		return nil
	}
	if query.Has("after") || query.Get("channel_id") != "" || query.Get("conversation_id") != "" || query.Get("issue_id") != "" {
		return usagef("before/latest is an unfiltered recent-history mode; use after with channel/conversation/issue filters")
	}
	return nil
}
