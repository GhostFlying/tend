package cli

import (
	"fmt"
	"slices"
	"strings"
)

// CapabilityBinding is an executable CLI entry for a public interface.
// Examples are exercised through Main by the coverage tests, not through a raw RPC client.
type CapabilityBinding struct {
	Capability string
	Command    string
	Example    []string
}

func binding(capability, command string, args ...string) CapabilityBinding {
	return CapabilityBinding{capability, command, append(strings.Fields(command), args...)}
}

// CapabilityBindings describes both existing commands and registered additions.
// The API contract checker compares this to the server's actual registrations.
func CapabilityBindings() []CapabilityBinding {
	var out []CapabilityBinding
	for _, cmd := range capabilityCommands {
		args := []string{}
		variables := commandVariables(cmd.Route)
		if cmd.Operation != "" && !slices.Contains([]string{"conversation.set_settings", "conversation.set_handoff_settings", "issues.set_notification_settings"}, cmd.Operation) {
			variables = []string{"id"}
		}
		for _, name := range variables {
			if name == "seq" || name == "index" {
				args = append(args, "1")
			} else {
				args = append(args, "sample")
			}
		}
		capability := "GET " + strings.Split(cmd.Route, "?")[0]
		if cmd.Operation != "" {
			capability = cmd.Operation
			args = append(args, "--data", `{}`)
			if cmd.RevisionRoute != "" {
				args = append(args, "--revision", "2")
			}
		}
		if cmd.Download {
			args = append(args, "--out", "{output}")
		}
		out = append(out, binding(capability, cmd.Name, args...))
	}
	for action, operation := range issueOperations {
		if strings.HasPrefix(action, "followups-") {
			continue
		}
		args := []string{"--data", `{}`}
		if action != "create" {
			args = append([]string{"sample", "--revision", "2", "--epoch", "3"}, args...)
		}
		out = append(out, binding(operation, "issue "+action, args...))
	}
	for _, action := range []string{"create", "update", "pause", "resume", "cancel", "trigger"} {
		args := []string{"sample", "--revision", "2", "--epoch", "3", "--data", `{}`}
		if action != "create" {
			args = append([]string{"sample", "sample"}, args[1:]...)
		}
		out = append(out, binding("issues.followups_"+action, "issue followup "+action, args...))
	}
	for _, action := range []string{"create", "update", "respond", "confirm", "withdraw", "present"} {
		args := []string{"--data", `{}`}
		if action != "create" {
			args = append([]string{"sample", "--revision", "2"}, args...)
		}
		out = append(out, binding("attentions."+action, "attention "+action, args...))
	}
	for _, action := range []string{"create", "update", "pause", "resume", "cancel", "rotate"} {
		args := []string{"--revision", "2", "--epoch", "3", "--data", `{}`}
		if action == "create" {
			args = append(args, "--issue", "sample")
		} else {
			args = append([]string{"sample"}, args...)
		}
		out = append(out, binding("webhooks."+action, "webhook "+action, args...))
	}
	out = append(out, legacyBindings()...)
	slices.SortFunc(out, func(a, b CapabilityBinding) int {
		return strings.Compare(a.Capability+"/"+a.Command, b.Capability+"/"+b.Command)
	})
	return out
}

func legacyBindings() []CapabilityBinding {
	return []CapabilityBinding{
		binding("POST /api/v1/session", "login", "--server", "{server}"),
		binding("POST /api/v1/ops", "issue create", "--data", `{}`),
		binding("GET /api/v1/snapshot", "status", "--json"),
		binding("GET /api/v1/issues", "issue list", "--json"),
		binding("GET /api/v1/issues/{id}", "issue show", "sample", "--json"),
		binding("GET /api/v1/issues/{id}/activity", "issue activity", "sample"),
		binding("GET /api/v1/issues/{id}/results", "issue results", "sample"),
		binding("GET /api/v1/issues/{id}/context", "issue context", "sample"),
		binding("GET /api/v1/issues/{id}/followups", "issue followups", "sample"),
		binding("GET /api/v1/issues/{id}/followups/{rule}/history", "issue followup-history", "sample", "sample"),
		binding("GET /api/v1/issues/{id}/deliveries", "issue deliveries", "sample"),
		binding("GET /api/v1/requests/{id}", "request show", "sample", "--json"),
		binding("GET /api/v1/categories", "category list"),
		binding("GET /api/v1/categories/{id}", "category show", "sample"),
		binding("GET /api/v1/hosts/{id}/agents", "agents show", "--host", "sample"),
		binding("GET /api/v1/hosted-agents", "agents hosted"),
		binding("GET /api/v1/repos/{id}", "repo show", "sample"),
		binding("GET /api/v1/sources/{id}", "source show", "sample"),
		binding("GET /api/v1/runs/{id}/messages", "run messages", "sample"),
		binding("GET /api/v1/runs/{id}/native-entry", "run native-entry", "sample"),
		binding("GET /api/v1/attentions", "attention list"),
		binding("GET /api/v1/inbox", "inbox list"),
		binding("GET /api/v1/attentions/{id}", "attention show", "sample"),
		binding("GET /api/v1/next-actions", "next-action list"),
		binding("GET /api/v1/planner-entries/{id}", "next-action show", "sample"),
		binding("GET /api/v1/deadlines", "issue deadline list"),
		binding("GET /api/v1/deadlines/{id}", "issue deadline show", "sample"),
		binding("GET /api/v1/conversation/attachments/{id}", "attachment download", "sample", "--out", "{output}"),
		binding("GET /api/v1/conversation/handoff", "conversation handoff", "--status", "--json"),
		binding("POST /api/v1/conversation/attachments", "attachment upload", "{input}"),
		binding("GET /api/v1/messaging/channels", "channel list"),
		binding("GET /api/v1/messaging/channels/{channel}", "channel show", "telegram"),
		binding("GET /api/v1/messaging/messages/{id}", "message show", "sample"),
		binding("GET /api/v1/messaging/messages/{id}/{view}", "message receipts", "sample"),
		binding("GET /api/v1/messaging/events", "message watch"),
		binding("GET /api/v1/diagnostics/{kind}", "event list", "--json"),
		binding("GET /api/v1/diagnostics/{kind}/{id}", "event show", "sample", "--json"),
		binding("GET /api/v1/diagnostics/events/{id}/sources", "event sources", "sample"),
		binding("PUT /api/v1/webhook-keys/{id}", "webhook create", "--issue", "sample", "--revision", "2", "--epoch", "3", "--secret-file", "{secret}", "--data", `{"key_id":"sample"}`),
		binding("categories.put", "category put", "sample", "--title", "Sample", "--revision", "2"),
		binding("repos.register", "repo register", "sample", "--remote", "https://example.com/repo.git", "--revision", "2"),
		binding("decisions.respond", "decide", "sample", "accept", "--data", `{"version":2,"option":"accept"}`),
		binding("actions.create", "retry", "sample", "--data", `{"kind":"implement","generation":2}`, "--revision", "2"),
		binding("control.set_mode", "pause", "sample", "--data", `{"mode":"paused"}`, "--revision", "2"),
		binding("tasks.rate", "rate", "sample", "p1", "--data", `{"rating":"p1"}`, "--revision", "2"),
		binding("tasks.cancel", "cancel", "sample", "--data", `{}`, "--revision", "2"),
		binding("conversation.handoff", "conversation handoff"),
		binding("conversation.consult", "conversation consult", "--data", `{}`),
		binding("conversation.promote", "conversation promote", "sample", "--data", `{}`),
		binding("conversation.submit_message", "conversation send", "sample", "--data", `{"body":"hello"}`),
		binding("conversation.inject_source", "source inject", "sample", "--data", `{"text":"source"}`),
		binding("runs.send_message", "run message", "sample", "--revision", "2", "--data", `{}`),
		binding("planner.entries_put", "next-action put", "--data", `{}`),
		binding("issues.deadlines_put", "issue deadline put", "--data", `{}`),
		binding("issues.deadlines_cancel", "issue deadline cancel", "sample", "--revision", "2"),
		binding("messaging.send", "message send", "--data", `{"body":"hello"}`),
		binding("messaging.reply", "message reply", "sample", "--data", `{"body":"hello"}`),
		binding("messaging.notify", "message notify", "--data", `{"body":"hello"}`),
		binding("messaging.read", "message read", "sample", "--data", `{"content_revision":2}`),
		binding("messaging.bind", "channel bind", "--user-id", "1", "--revision", "2"),
		binding("messaging.unbind", "channel unbind", "--revision", "2"),
		binding("local POST /agents/register", "agents local register", "--socket", "{socket}"),
		binding("local GET /agents/registration", "agents local registration", "--socket", "{socket}"),
		binding("local GET /repositories", "repo local list", "--socket", "{socket}"),
		binding("local POST /repositories/bind", "repo local bind", "--socket", "{socket}", "--revision", "2", "--data", `{"repo":"sample","remote_url":"https://example.com/repo.git"}`),
		binding("local GET /agents/status", "agents local status", "--socket", "{socket}"),
		binding("local POST /agents/validate", "agents local validate", "--socket", "{socket}"),
		binding("local POST /agents/reload", "agents local reload", "--socket", "{socket}", "--revision", "2"),
		binding("local POST /agents/reconcile", "agents local reconcile", "--socket", "{socket}", "--request-id", "{request}"),
		binding("local GET /archive/status", "archive status", "--socket", "{socket}", "--json"),
		binding("local POST /archive/prepare", "archive prepare", "--socket", "{socket}", "--revision", "2", "--json"),
		binding("local POST /archive/cancel", "archive cancel", "--socket", "{socket}", "--revision", "2", "--json"),
		binding("local POST /archive/host", "archive host", "--socket", "{socket}", "--revision", "2", "--host", "sample", "--json"),
		binding("local GET /upgrades/status", "upgrade status", "--socket", "{socket}", "--json"),
		binding("local POST /upgrades/start", "upgrade start", "--socket", "{socket}", "--release", "{release}", "--json"),
		binding("local POST /upgrades/retry", "upgrade retry", "--socket", "{socket}", "--upgrade-id", "sample", "--revision", "2", "--json"),
		binding("local POST /upgrades/cancel", "upgrade cancel", "--socket", "{socket}", "--upgrade-id", "sample", "--revision", "2", "--json"),
	}
}

var additionalHelpCommands = []string{"repo local status", "channel telegram bind", "channel telegram unbind", "doctor", "host list", "host show", "queue", "logs", "resume", "takeover", "request replay", "issue replay", "issue schedules", "issue events", "issue webhooks", "observation list", "observation show", "observation refresh", "schedule list", "schedule show", "schedule create", "schedule update", "schedule pause", "schedule resume", "schedule cancel", "schedule trigger", "schedule history", "webhook list", "webhook show", "webhook create", "webhook update", "webhook pause", "webhook resume", "webhook cancel", "webhook rotate", "webhook receipts", "event trace", "event watch", "message trace", "attention read", "attention archive", "attention replay", "next-action replay", "issue followup create", "issue followup update", "issue followup pause", "issue followup resume", "issue followup cancel", "issue followup trigger"}

func commandUsage(prefix []string) string {
	if len(prefix) > 0 && prefix[0] == "planner" {
		alias := append([]string(nil), prefix...)
		alias[0] = "next-action"
		return strings.ReplaceAll(commandUsage(alias), "next-action", "planner")
	}

	name := strings.Join(prefix, " ")
	lines := map[string]bool{}
	for _, v := range CapabilityBindings() {
		if name == "" || v.Command == name || strings.HasPrefix(v.Command, name+" ") {
			lines[v.Command] = true
		}
	}
	for _, command := range additionalHelpCommands {
		if name == "" || command == name || strings.HasPrefix(command, name+" ") {
			lines[command] = true
		}
	}
	names := make([]string, 0, len(lines))
	for command := range lines {
		names = append(names, command)
	}
	slices.Sort(names)
	var out strings.Builder
	prefixName := "tend"
	if name != "" {
		prefixName += " " + name
	}
	fmt.Fprintf(&out, "usage: %s <command> [arguments]\n\n", prefixName)
	for _, command := range names {
		fmt.Fprintf(&out, "  %s\n", command)
	}
	if name != "" {
		for _, entry := range legacyBindings() {
			if entry.Command == name {
				fmt.Fprintf(&out, "\nExample: tend %s\n", strings.Join(entry.Example, " "))
				break
			}
		}
		for _, cmd := range capabilityCommands {
			if cmd.Name != name {
				continue
			}
			fmt.Fprintf(&out, "\nArguments: %s\n", strings.Join(commandVariables(cmd.Route), " "))
			if cmd.Operation != "" {
				fmt.Fprintf(&out, "Operation: %s; target: %s\n  --data <json> | --file <json-file|->\n  --request-id <original-id> --dry-run --reason <text>\n", cmd.Operation, cmd.Target)
				if cmd.Target != "issue_settings" && cmd.Name != "conversation settings set" && cmd.Name != "conversation handoff-settings set" {
					out.WriteString("  positional ID selects the exact target\n")
				}
				if cmd.RevisionRoute != "" {
					out.WriteString("  --revision <original-control-revision>\n")
				}
			}
			for _, query := range strings.Fields(cmd.Queries) {
				flag, param, ok := strings.Cut(query, "=")
				if !ok {
					param = flag
				}
				fmt.Fprintf(&out, "  --%s <value>  server parameter %s\n", flag, param)
			}
			if cmd.Download {
				out.WriteString("  --out <path>  save verified original bytes\n")
			}
		}
	}
	out.WriteString("\nAgent catalogs: agents show --host <id>; agents hosted.\nReads support --json. JSON inputs use --data or --file (use - for stdin).\nOwner writes retain --request-id and original control values; --dry-run exports the original Envelope.\nUse tend request show <id> to reconcile and tend request replay --file <original.json> for an identical retry.\n")
	return out.String()
}

func hasCommandChildren(prefix []string) bool {
	name := strings.Join(prefix, " ") + " "
	for _, entry := range CapabilityBindings() {
		if strings.HasPrefix(entry.Command, name) {
			return true
		}
	}
	for _, command := range additionalHelpCommands {
		if strings.HasPrefix(command, name) {
			return true
		}
	}
	// Planner is an established alias of next-action; all its actions use the
	// same FlagSet parsers and do not create a separate business capability.
	return strings.Join(prefix, " ") == "planner"
}
