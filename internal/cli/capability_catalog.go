package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
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
	for _, action := range []string{"status", "cases", "history", "session", "config", "upgrade-status"} {
		out = append(out, binding("local GET /maintenance/"+action, "maintenance "+action, "--socket", "{socket}", "--json"))
	}
	for _, action := range []string{"show", "request"} {
		out = append(out, binding("local GET /maintenance/"+action+"/{id}", "maintenance "+action, "--socket", "{socket}", "--id", "sample", "--json"))
	}
	for _, action := range []string{"check", "audit", "pause", "resume", "defer", "resolve", "config", "reconcile", "review", "upgrade-plan", "upgrade-apply"} {
		args := []string{"--socket", "{socket}", "--revision", "2", "--request-id", "{request}", "--json"}
		if action == "defer" || action == "resolve" {
			args = append(args, "--id", "sample", "--reason", "Reviewed original evidence", "--until", "9999999999")
		}
		if action == "reconcile" {
			args = append(args, "--session-id", "original", "--reason", "Native creation read back")
		}
		if action == "config" {
			args = append(args, "--mode", "when-idle", "--data", `{"agent":"codex","socket":"/native.sock","sandbox":"workspace-write","approval_policy":"never"}`)
		}
		if action == "upgrade-plan" {
			args = append(args, "--release", "{release}")
		}
		if action == "upgrade-apply" {
			args = append(args, "--upgrade-id", "original")
		}
		if action == "review" {
			args = append(args, "--base", "main", "--head", strings.Repeat("a", 40))
		}
		out = append(out, binding("local POST /maintenance/"+action, "maintenance "+action, args...))
	}
	for _, action := range protocol.CodexEnvironmentActions() {
		args := []string{"--id", "sample"}
		if action == "list" || strings.HasPrefix(action, "watch ") {
			args = nil
		}
		if action == "reconcile" {
			args = append(args, "--request-id", "original")
		}
		out = append(out, binding("local EXEC codex-env "+action, "codex-env "+action, args...))
	}
	out = append(out, binding(protocol.LocalServerCapabilities, "upgrade capabilities", "--binary", "{server-binary}", "--sha256", "{server-sha256}", "--json"))
	for _, action := range protocol.UpdaterSelfUpdateActions() {
		out = append(out, binding("local EXEC updater self-update "+action, "updater self-update "+action, "--installer", "{installer}", "--installer-sha256", "{installer-sha256}", "--config", "{config}", "--plan", "{handoff}", "--json"))
	}
	for _, action := range protocol.UpdaterHandoffActions() {
		out = append(out, binding("local EXEC updater handoff "+action, "updater handoff "+action, "--installer", "{installer}", "--installer-sha256", "{installer-sha256}", "--config", "{config}", "--plan", "{handoff}", "--json"))
	}

	for _, action := range protocol.CommittedHandoffActions() {
		out = append(out, binding("local EXEC updater maintenance "+action, "updater maintenance "+action, "--installer", "{installer}", "--installer-sha256", "{installer-sha256}", "--config", "{config}", "--plan", "{handoff}", "--json"))
	}
	for _, action := range protocol.BoundedMaintenanceActions() {
		args := []string{"--installer", "{installer}", "--installer-sha256", "{installer-sha256}", "--config", "{config}", "--json"}
		if action != "status" && action != "reconcile" {
			args = append(args, "--plan", "{handoff}", "--authorization", "{bounded-authorization}")
		}
		out = append(out, binding("local EXEC updater bounded "+action, "updater bounded "+action, args...))
	}
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
		if cmd.Name == "task manager-reconciliation" {
			args = append(args, "--run", "original")
		}
		capability := "GET " + strings.Split(cmd.Route, "?")[0]
		if cmd.Post {
			capability = "POST " + cmd.Route
			args = append(args, "--data", `{"conversation_id":"sample","root_thread_id":"native-root","samples":[]}`)
		}
		if cmd.Operation != "" {
			capability = cmd.Operation
			if cmd.Operation == protocol.OpContinueRetainedRun {
				args = append(args, "--data", retainedRunProofExample())
			} else if cmd.Operation == boundedRunnerOwnerOperation {
				args = append(args, "--data", boundedRunnerPlanExample())
			} else if cmd.Operation == protocol.OpReleaseBoundedRunnerAdmission {
				args = append(args, "--data", boundedRunnerAdmissionReleaseExample())
			} else if cmd.Operation == protocol.OpAnswerDecisionContinuation {
				args = append(args, "--data", `{"version":1,"text":"Wait for independent resume","continuation":{"mode":"wait","reason":"Await explicit continuation"}}`)
			} else if cmd.Operation == protocol.OpResumeDecisionContinuation || cmd.Operation == protocol.OpReconcileDecisionContinuation {
				args = append(args, "--data", `{"expected_revision":0}`)
			} else if cmd.Operation == protocol.OpReconcileManagerRun {
				args = append(args, "--mode", "read_receipts", "--run", "original", "--lease", "original-lease", "--close-request", "original-close", "--close-sha256", strings.Repeat("a", 64), "--stopped-request", "original-stopped", "--stopped-sha256", strings.Repeat("b", 64), "--assignment-epoch", "3", "--reason", "Read the original native receipt")
			} else {
				args = append(args, "--data", `{}`)
			}
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
	for _, action := range []string{"create", "update", "respond", "confirm", "withdraw", "present", "resolve", "escalate"} {
		args := []string{"--data", `{}`}
		if action != "create" {
			args = append([]string{"sample", "--revision", "2"}, args...)
		}
		if action == "resolve" || action == "escalate" {
			args = append(args, "--epoch", "3", "--reason", "Review original problem")
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
	out = append(out,
		binding("GET /api/v1/concurrency-groups", "agents concurrency list"),
		binding("GET /api/v1/concurrency-groups/{id}", "agents concurrency show", "sample"),
		binding("GET /api/v1/concurrency-history", "agents concurrency history"),
		binding("concurrency.create", "agents concurrency create", "sample", "--name", "Sample", "--min", "1", "--initial", "2", "--max", "8", "--revision", "0"),
		binding("concurrency.update", "agents concurrency update", "sample", "--data", `{"name":"Sample","min":1,"initial":2,"max":8}`, "--revision", "2"),
		binding("concurrency.bind", "agents concurrency bind", "sample", "--environment", "sample", "--runtime", "sample", "--revision", "2"),
		binding("concurrency.unbind", "agents concurrency unbind", "sample", "--environment", "sample", "--runtime", "sample", "--revision", "2"),
		binding("concurrency.delete", "agents concurrency delete", "sample", "--revision", "2"),
	)
	out = append(out, legacyBindings()...)
	slices.SortFunc(out, func(a, b CapabilityBinding) int {
		return strings.Compare(a.Capability+"/"+a.Command, b.Capability+"/"+b.Command)
	})
	return out
}

func legacyBindings() []CapabilityBinding {
	return []CapabilityBinding{
		binding("local POST /runner/v1/rpc", "usage host-sources", "sample", "--socket", "{socket}"),
		binding("local POST /runner/v1/rpc", "usage host-preview", "sample", "--socket", "{socket}", "--data", `{"run_id":"run","root_thread_id":"native-root","samples":[]}`),
		binding("local POST /runner/v1/rpc", "usage host-upload", "sample", "--socket", "{socket}", "--data", `{"run_id":"run","root_thread_id":"native-root","samples":[]}`),
		binding("POST /api/v1/session", "login", "--server", "{server}"),
		binding("POST /api/v1/ops", "issue create", "--data", `{}`),
		binding("GET /api/v1/snapshot", "status", "--json"),
		binding("GET /api/v1/issues", "issue list", "--json"),
		binding("GET /api/v1/issues/{id}", "issue show", "sample", "--json"),
		binding("GET /api/v1/issues/{id}/activity", "issue activity", "sample"),
		binding("GET /api/v1/issues/{id}/results", "issue results", "sample"),
		binding("GET /api/v1/issues/{id}/reuse-status", "issue reuse-status", "sample", "--json"),
		binding("GET /api/v1/issues/{id}/context", "issue context", "sample"),
		binding("GET /api/v1/issues/{id}/handoff-prompt", "issue handoff-prompt", "sample", "--json"),
		binding("GET /api/v1/issues/{id}/followups", "issue followups", "sample"),
		binding("GET /api/v1/issues/{id}/followups/{rule}/history", "issue followup-history", "sample", "sample"),
		binding("GET /api/v1/issues/{id}/deliveries", "issue deliveries", "sample"),
		binding("GET /api/v1/requests/{id}", "request show", "sample", "--json"),
		binding("GET /api/v1/categories", "category list"),
		binding("GET /api/v1/categories/{id}", "category show", "sample"),
		binding("GET /api/v1/hosts/{id}/agents", "agents show", "--host", "sample"),
		binding("GET /api/v1/agent-issues", "agents issues", "--environment", "sample", "--agent", "sample"),
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
		binding("GET /api/v1/messaging/feishu", "channel feishu show"),
		binding("GET /api/v1/messaging/feishu/inputs", "channel feishu inputs", "--after", "0", "--limit", "10", "--status", "pending"),
		binding("messaging.feishu_bind", "channel feishu bind", "--app-id", "app-fixture", "--tenant-key", "tenant-fixture", "--sender-id", "sender-fixture", "--chat-id", "chat-fixture", "--revision", "2"),
		binding("messaging.feishu_unbind", "channel feishu unbind", "--revision", "2"),
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
		binding("local GET /upgrades/maintenance/status", "updater maintenance ledger", "--socket", "{socket}", "--maintenance-id", "maintenance", "--json"),
		binding("local POST /updater/maintenance/health", "updater maintenance health", "--socket", "{socket}", "--plan", "{maintenance}", "--json"),
		binding("local POST /maintenance/prepare", "updater maintenance prepare", "--socket", "{socket}", "--plan", "{maintenance}", "--expected-generation", "2", "--json"),
		binding("local POST /maintenance/apply", "updater maintenance authorize", "--socket", "{socket}", "--plan", "{maintenance}", "--expected-generation", "2", "--json"),
		binding("local POST /maintenance/cancel", "updater maintenance cancel", "--socket", "{socket}", "--plan", "{maintenance}", "--expected-generation", "2", "--json"),
		binding("local POST /maintenance/recover-stop", "updater maintenance recover-stop", "--socket", "{socket}", "--plan", "{maintenance}", "--expected-generation", "2", "--request-id", "req_01M6F01JSS6Q9BFV6DJNSSBHEW", "--first-capture", "{first-capture}", "--second-capture", "{second-capture}", "--json"),
		binding("local POST /maintenance/resume", "updater maintenance resume", "--socket", "{socket}", "--plan", "{maintenance}", "--expected-generation", "2", "--request-id", "{request}", "--expires-at-ms", "9999999999999", "--window-claim", "continuation", "--resource-evidence", "{input}", "--resource-sha256", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "--json"),
		binding("local POST /maintenance/finish", "updater maintenance finish", "--socket", "{socket}", "--plan", "{maintenance}", "--expected-generation", "2", "--json"),
		binding("local GET /archive/status", "archive status", "--socket", "{socket}", "--json"),
		binding("local POST /archive/prepare", "archive prepare", "--socket", "{socket}", "--revision", "2", "--json"),
		binding("local POST /archive/cancel", "archive cancel", "--socket", "{socket}", "--revision", "2", "--json"),
		binding("local POST /archive/host", "archive host", "--socket", "{socket}", "--revision", "2", "--host", "sample", "--json"),
		binding("local GET /upgrades/recovery/status", "upgrade recover status", "--socket", "{socket}", "--ledger-socket", "{socket}", "--recovery-id", "forward", "--json"),
		binding("local GET /status", "upgrade recover status", "--socket", "{socket}", "--json"),
		binding("local POST /recovery/plan", "upgrade recover plan", "--socket", "{socket}", "--plan", "{recovery}", "--request-id", "{request}", "--upgrade-id", "original", "--recovery-id", "forward", "--json"),
		binding("local POST /recovery/apply", "upgrade recover apply", "--socket", "{socket}", "--plan", "{recovery}", "--request-id", "{request}", "--upgrade-id", "original", "--recovery-id", "forward", "--expected-generation", "2", "--json"),
		binding("local GET /upgrades/status", "upgrade status", "--socket", "{socket}", "--json"),
		binding("local POST /upgrades/start", "upgrade start", "--socket", "{socket}", "--release", "{release}", "--park-idle-managers", "--json"),
		binding("local POST /upgrades/retry", "upgrade retry", "--socket", "{socket}", "--upgrade-id", "sample", "--revision", "2", "--json"),
		binding("local POST /upgrades/cancel", "upgrade cancel", "--socket", "{socket}", "--upgrade-id", "sample", "--revision", "2", "--json"),
	}
}

var additionalHelpCommands = []string{"issue pr-policy show", "issue pr-policy set", "issue pr-policy clear", "repo local status", "channel telegram bind", "channel telegram unbind", "doctor", "host list", "host show", "queue", "logs", "resume", "takeover", "request replay", "issue replay", "issue schedules", "issue events", "issue webhooks", "observation list", "observation show", "observation refresh", "schedule list", "schedule show", "schedule create", "schedule update", "schedule pause", "schedule resume", "schedule cancel", "schedule trigger", "schedule history", "webhook list", "webhook show", "webhook create", "webhook update", "webhook pause", "webhook resume", "webhook cancel", "webhook rotate", "webhook receipts", "event trace", "event watch", "message trace", "attention read", "attention archive", "attention replay", "next-action replay", "issue followup create", "issue followup update", "issue followup pause", "issue followup resume", "issue followup cancel", "issue followup trigger"}

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
		if name == "issue materials" {
			out.WriteString("\nRead retained Issue context and original materials. --kind selects overview, issues, sources, facts, messages, tasks, artifacts, acceptances, questions, files, deliveries, reviews, deployment, delivery_authorization, capabilities, agent_capabilities, agent_capability_history or task_capabilities. Default overview returns the selected Issue context and separately identified legacy_contexts for retained historical work items. kind=issues discovers related Issues with --after and --limit; more=true and next identify the following page. Select a discovered Issue with the positional issue_id for a focused material read; no separate share grant is needed within the same named project. Other material kinds retain their own pages. --kind reviews uses --id <delivery_id> to select a delivery and --after/--limit to page its shared review list. shared_reviews.existing_evidence is a bounded contributor evidence index with candidate identities and original artifact/file references, not complete reports or check logs. Respect contributions_more and other truncation markers; read needed originals with --kind artifacts --id <artifact_id>, then download the original file IDs. Missing fields remain evidence not provided. --id otherwise selects one exact object. Use --after with the preceding next cursor and --limit 1..100; more=true means further pages remain. sources preserves linked message content, queued/withdrawn state and association versions; facts preserves original and superseded facts. File sources_more marks incomplete provenance metadata. The full JSON preserves source, historical state, versions, timestamps and original file size/SHA256.\nFor capabilities, omit --id to list current active and retired definitions, or supply a capability ID for its revision history. agent_capabilities lists current public declarations, optionally selecting --id environment/agent. agent_capability_history requires --id environment/agent. task_capabilities requires a related Task ID and returns its requirements and matching explanation; the worker's read_context may omit this ID to inspect its own Task and role. Nested capability pages retain their original pagination and unknown states. delivery_authorization requires --id with the original execution Task ID; its current valid/invalid_reason and historical settlement.eligible are separate facts. A later candidate or installation can invalidate new execution while the original completed deployment remains eligible for settlement.\nReading does not admit a new instruction, grant a deployment or schedule a Run. No request_id or control revision is needed; an interrupted read may be repeated. Task spec.inputs records historical inputs and is not an Issue reading whitelist. Related Issues in the same non-empty project share discoverable materials by default; other projects retain explicit sharing, and an explicit object-level share revocation applies to that target and object. Original frozen Task inputs remain historical sources across handoff. Use file/attachment download with a returned original file_id for the actual bytes.\n")
		}
		prefixName += " " + name
	}
	fmt.Fprintf(&out, "usage: %s <command> [arguments]\n\n", prefixName)
	for _, command := range names {
		fmt.Fprintf(&out, "  %s\n", command)
	}
	if name != "" {
		if name == "issue deployment-preparation" {
			out.WriteString("\nRead-only deployment preparation for this Issue: the current fixed server, complete active hosts, runtime/control revisions, installation receipts and relevant blockers. The full response preserves source, observed_at, event sequence and explicit unknown or missing facts. Heartbeat and process observations do not substitute for installation receipts.\nUse: tend issue deployment-preparation <issue_id> [--json]. No target selector is required. Reading never refreshes a provider, schedules an upgrade or authorizes deployment. An interrupted read can be repeated; it creates no mutation request.\n")
		}
		if name == "issue context" {
			out.WriteString("\nRead canonical Issue context diagnostics with --after-source <sequence>. Historical --after <numeric-string> is an equivalent alias; if both are supplied they must agree. applied_after, next, more and content_id identify the actual legacy page.\nUse --view incremental [--cursor <next_cursor>] [--limit 1..100] for current snapshot and subsequent event deltas (default 50 items). Keep next_cursor in the current session, follow more until the interval completes, then reuse it for later changes. Each fact/source index occurs on its own page, with full originals available through issue materials. through pins event membership; object values are current at read time and later events remain readable in the following interval. Cursor scope/history errors require a fresh snapshot without cursor. Incremental mode does not mix --after-source/--after. Reading creates no request or acknowledgement and may be repeated.\nprepared reports the historical baseline; dispatch_ready describes current context diagnostics and owned receipts. Ordinary source, definition or ancestor changes notify the responsible Agent in the existing coalesced window; their hashes do not require a new Task spec or per-Task permission.\nThese fields are not the admission result of issue create-task. Submit the formal task command with current Issue/source/assignment versions for that decision; do not prepare a legacy global baseline.\n")
		}
		if name == "issue handoff-prompt" {
			out.WriteString("\nRead-only Issue context handoff. --run adds only an exact Run controlled by this Issue; --json includes source/history cursors and versions. Reading never creates a session or transfers control. Follow the prompt's pagination instructions, then use takeover/resume with current controls for actual workspace work.\n")
		}
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
			if cmd.Post {
				out.WriteString("  --data <json> | --file <json-file|->  native usage preview; no changes are committed\n")
			}
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
	out.WriteString("\nAgent catalogs: agents show --host <id>; agents hosted; agents issues --environment <id> --agent <id> [--scope all|current|history] [--state <state>] [--search <text>] [--snapshot <id>] [--agent-revision <n>] [--after <cursor>] [--limit <n>].\nReads support --json. JSON inputs use --data or --file (use - for stdin).\nOwner writes retain --request-id and original control values; --dry-run exports the original Envelope.\nUse tend request show <id> to reconcile and tend request replay --file <original.json> for an identical retry.\n")
	if name == "task" || name == "task show" || name == "artifact show" || name == "file" || strings.HasPrefix(name, "file ") {
		out.WriteString("\nAcceptance evidence: task and artifact views include each acceptance_id and its exact files manifest. A file_id identifies original immutable bytes, including a blocked or changes_required verdict; a path in evidence JSON is not a stored file. Use file metadata <file_id>, then file download <file_id> --out <path> [--json]. Downloads verify size/SHA-256 and replace the output only after success; retry the same read after interruption. Task inputs retain source_acceptance_id when explicitly selected. Read commands have no mutation request, revision or dry-run.\n")
	}
	if name == "issue" || name == "issue create" || name == "issue create-task" || name == "issue set-acceptance-policy" {
		out.WriteString("\nIndependent acceptance: issue create accepts independent_acceptance: {required: true|false, reason: <text>}. A read-only investigation may use false and must still submit a legal durable report. Creator identity is server-frozen. issue create-task accepts the same independent_acceptance object for the new Task. The responsible Agent chooses self-check or independent review for ordinary work; an explicit human requirement remains binding. Existing Tasks retain their original frozen policy, and the current phase does not wait for an otherwise unused future reviewer.\n")
		out.WriteString("tend issue set-acceptance-policy <issue_id> --revision <revision> --epoch <epoch> --data '{\"required\":false,\"reason\":\"A bounded investigation needs no separate review\"}'\nIssue policy changes affect future tasks only; a worker cannot rewrite its existing frozen policy. Use --dry-run to save the original Envelope; unknown results require reconciliation and identical --request-id/payload/control values, never a new request. Read issue show/task show --json for creator and frozen task policy.\n")
	}
	if name == "issue" || name == "issue update" {
		out.WriteString("\nOrdinary Issue edits and source changes notify the responsible owner through coalesced events. The owner decides whether work changed and explicitly revises, pauses or continues affected work. Existing Task specs remain immutable; a source digest change alone does not invalidate admission or results. Optional continue_existing assessments retain expected_context_revision and task_dispositions with task_id, spec_version, expected_control_revision and reason as historical assessments, not a normal execution prerequisite.\n")
	}
	if name == "issue" || strings.HasPrefix(name, "issue pr-policy") {
		out.WriteString("\nIssue PR policy: show <issue_id>; set <issue_id> --mode auto|manual --repository <owner/repo> --reason <text>; clear <issue_id> --reason <text>. Auto grants protected integration after review and checks; deployment is separate. Missing policy is not automatic authorization; a new PR candidate reuses one Issue-level policy clarification while preparation, review and CI continue. Existing exact candidate approvals remain within their original scope. Optional --revision/--epoch select the original Issue version. --source-message and --source-revision preserve an actual owner source.\n")
		out.WriteString("Use --dry-run to save the complete update Envelope. If its outcome is unknown, read tend request show <original-id>, then issue replay --file <original-envelope.json>; do not rerun the policy helper against a newer plan.\n")
	}
	if name == "issue" || name == "issue update-delivery" {
		out.WriteString("\nContinuous delivery: update-delivery takes task_id, action and reason under the current Issue --revision and --epoch. prepare can register a real candidate from a live Task with expected_task_control_revision, without an intermediate final report or separate delivery Task. Actual server-verified review/checks and the Issue auto policy or original manual approval still apply. bind selects execution_task_id and its expected_task_control_revision; it may bind the same live Task and never revives a terminal Task. Existing completed sources retain their original historical delivery path. complete records original completed delivery evidence; it does not authorize another installation. Read issue materials --kind delivery_authorization --id <task_id> --json for execution versus settlement facts. Save --dry-run output and reconcile an unknown result by original request ID and identical Envelope, preserving both Issue and Task controls.\n")
	}
	if name == "issue" || name == "issue review-delivery" || name == "issue delivery-reviews" {
		out.WriteString("\nShared PR review: issue delivery-reviews <issue_id> [--delivery <id>] [--after <cursor>] [--limit <n>] reads associations, rounds, findings, dispositions and result validity. issue review-delivery <issue_id> --revision <issue-revision> --epoch <assignment-epoch> --data <payload> accepts action link, unlink, start, record, correct, resolve, assess, supplement, recheck or authorize_recheck with delivery_id and reason. Bind a task_id for a delivery link or reviewer_task_id for a review round; acceptance_id imports an original independent acceptance only when its immutable evidence.delivery_review binds provider, repository, number, head_sha, base_sha, requirements_digest and repository_check to the whole PR. A plain task acceptance is insufficient. supplement binds an actual report task by the original reviewer to add missing whole-PR evidence once without erasing the original acceptance or resetting the round budget. start with optional reviewer_role=accept prebinds the original Task/spec/artifact and actual acceptance reviewer/profile. Omit reviewer_role to retain report/import behavior. record reads the bound Task's actual artifact or immutable acceptance, never a caller-supplied verdict. correct with reviewer_task_id and expected_review_revision appends the original reviewer's actual completed report as an immutable correction of the same candidate and requirements. It preserves the original terminal Task, report and round count; it does not require another formal review round or owner-written verdict. The responsible Agent may choose existing ordinary report work when fresh evidence is needed.\n")
		out.WriteString("An acceptance reviewer uses submit_verdict evidence.delivery_review (provider, repository, number, head_sha, base_sha, requirements_digest, repository_check and optional concrete findings), not submit_result. It can read genuine pending links or its frozen round via read_observation shared_reviews within the existing observation scope. Actual changes_required retains findings for resolve and the same Task can use a genuine new artifact/acceptance Run for the focused recheck. blocked/inconclusive remain waiting facts; a new actual acceptance Run may recheck that same artifact without importing the old verdict. These facts are never approval or synthetic quality failures. The reviewer report uses report.shared_review with review_id, head_sha, base_sha, requirements_digest, verdict, repository_check and findings. resolve records finding dispositions (finding_id, action, reason); assess explicitly reuses an unchanged-candidate result for current requirements. An initial review plus one focused recheck is convergence advice, not an authorization limit. The responsible Agent may arrange further genuine reviews within the existing scope and actual execution budgets after resolving findings. blocked/inconclusive remain evidence, not an automatic human approval gate. Historical authorize_recheck receipts remain readable and the legacy owner command is retained, but a new human grant is not required merely because two rounds occurred. Every real round and prior authorization remains; no caller may fabricate a passed result. Preserve --request-id, original Issue revision/epoch and full payload for retries, or replay the saved Envelope.\n")
	}
	if name == "task" || name == "task show" || name == "observation show" {
		out.WriteString("\nDelivery authorization is a top-level server-read binding, not a field in task spec. Workers use their existing run-scoped read_observation tool to inspect it without owner credentials. A missing legacy field is unknown, not proof that authorization was granted.\n")
	}
	if name == "run maintenance-reconciliation" || name == "run continue-retained" {
		out.WriteString("\nRetained ordinary Orca Run recovery: maintenance-reconciliation is ledger-only; can_continue does not prove a live native turn or restored execution. Copy identity, every protection entry (including original_requests_json), protection_set_sha256 and task_revision from the exact original Run. continue-retained accepts the identity fields at the payload top level, the complete protections array, its hash and an independent reason; a cited source_message_id also requires expected_source_revision.\n")
		out.WriteString("Use --revision with that original Task control revision, an explicit --request-id and --file <proof.json> (or --data <json>). Save --dry-run output as the original Envelope before submitting. The command authorizes strict attachment through the original journal and same lease only; it does not send a continuation, create a session or clear historical protection/unknown facts. Missing native evidence, old runners, pending business or a new maintenance capture keep recovery blocked.\n")
		out.WriteString("If the result is unknown, retain the original ID, input, revision and Envelope. Run tend request show <original-id> before tend request replay --file <original-envelope.json>; replay exact bytes only, never replace the ID or refresh the proof to retry an unknown request.\n")
		out.WriteString("After bounded runner replacement, also copy recovery_authorization_id from the original Run diagnostic into the continuation payload. Ordinary host mode changes do not release that recovery quarantine; the independent continuation must match its original authorization and full frozen Run proof.\n")
	}
	if name == "decide" || name == "decision-continuation" || strings.HasPrefix(name, "decision-continuation ") {
		out.WriteString("\nLegacy decide preserves automatic handoff after answer and stopped receipts. Use decision-continuation answer for conditional scheduling; it requires the parent Task --revision plus complete version, option/text and explicit continuation. Free text never proves a typed dependency.\n")
		out.WriteString("tend decision-continuation answer <decision_id> --revision <task_control_revision> --data '{\"version\":1,\"text\":\"Proceed\",\"continuation\":{\"mode\":\"continue_now\"}}'\n")
		out.WriteString("tend decision-continuation answer <decision_id> --revision <task_control_revision> --data '{\"version\":1,\"text\":\"Wait\",\"continuation\":{\"mode\":\"wait\",\"reason\":\"Await an independent resume decision\"}}'\n")
		out.WriteString("tend decision-continuation answer <decision_id> --revision <task_control_revision> --data '{\"version\":1,\"text\":\"After the dependency\",\"continuation\":{\"mode\":\"dependency\",\"reason\":\"Await exact dependency completion\",\"dependency\":{\"task_id\":\"<dependency_task>\",\"spec_version\":1}}}'\n")
		out.WriteString("Read decision-continuation show <decision_id> --json for policy, state, blocked_reason, continuation revision and parent task_control_revision. resume explicitly releases a wait; reconcile checks exact ordinary execution Task/spec completion without accepting a caller readiness claim. Manager Task dependencies are rejected; historical unsupported dependencies stay blocked. Both preserve physical-stop and current-contract guards.\n")
		out.WriteString("tend decision-continuation resume <decision_id> --revision <task_control_revision> --request-id <new-request-id> --data '{\"expected_revision\":1}' --dry-run\nUse reconcile with the same input shape for dependency reconciliation. On unknown outcome keep the original request ID, payload, both revisions and saved Envelope; run tend request show <original-id>, then request replay --file <original-envelope.json> for an identical retry.\n")
	}
	if name == "updater bounded" || strings.HasPrefix(name, "updater bounded ") {
		out.WriteString("\nBounded server/runner maintenance uses the reviewed independent installer, including status and reconcile when the old control socket is unavailable. Inputs: --installer <absolute-file> --installer-sha256 <reviewed-sha256> --config <absolute-file>.\n")
		out.WriteString("plan reads --plan <complete-source-plan> and --authorization <typed-json>; the installer fills factual source/target, generation, phase and canonical plan_sha256. Save its complete output before apply. apply, force, extend, cancel and takeover require an independent stable request_id and fresh expected_generation/expected_phase in that authorization; optional matching flags never replace them. force explicitly accepts retained unknown work; takeover independently reconciles the old updater and writer.\n")
		out.WriteString("authorization supports role (server/runner), source, target, reason, expires_at_ms, timeouts and force_on_drain_timeout. The timeout policy fields are overall_ms, handoff_ms, fence_ms, drain_ms, stop_ms, kill_ms, backup_ms, start_health_ms and complete_ms. Defaults are 360000, 60000, 30000, 60000, 30000, 10000, 30000, 90000 and 30000. Phase budgets sum to 340 seconds; the component/host total remains 360 seconds. Budget renewal requires a distinct extend request and fresh CAS; status or replay cannot renew it.\n")
		out.WriteString("takeover additionally binds updater_source (PID, start_ticks, launch_id and sha256) plus updater_lock (device/inode); data_identity binds the original ledger or journal separately. Those exact local identities are checked by the installer, not inferred from a server summary.\n")
		out.WriteString("--dry-run exports the exact installer arguments and typed inputs without executing. status/reconcile require only the fixed configuration and optionally --request-id to inspect the original request; their command timeout defaults to 10s. Other installer commands default to a 370s outer wait via --timeout. A mutation timeout reports unknown and prints the original request/CAS plus the next reconcile command. Keep the original plan, authorization and request; reconcile before repeating exact bytes. Installer termination does not prove that its managed service or native workers exited. Installed, healthy, linked and restored remain separate durable facts.\n")
	}
	if name == "host authorize-bounded-maintenance" {
		out.WriteString("\nProvide the complete typed RunnerBoundedMaintenancePlan with --data <json> or --file <file|->. It binds host_id, configured node_selector, runner_instance_id, runner_generation, the absolute release_dir, source, journal, target, expected_control_revision and the independent authorization. The nested authorization source/target/data_identity must equal the outer source/target/journal. A configured selector is not physical node proof; the independent installer checks local process and journal identities.\n")
		out.WriteString("If an original force-authorized launch exited before an accepted runner Hello, source_operation may bind its operation_id, kind=upgrade, phase=launch_exited, operation_sha256, launch_id, protocol, storage_version and original_request_id plus the owned launch_exit proof. In this mode source and authorization.source are empty process identities; the original force grant, its failed target, journal and retained protection set must match. Use an independent new authorization request and successor launch. The server and installer verify the original grant and local owned exit; a missing process search result alone is not exit proof.\n")
		out.WriteString("The owner Envelope uses authorization.request_id and expected_control_revision from the original plan; optional --request-id/--revision must match them. No revision or request is refreshed automatically. --dry-run saves the original Envelope. On unknown outcome, use tend request show <original-id>, then tend request replay --file <original-envelope.json> with the original bytes. Grant, physical stop, installation, retained native work and independent resume are separate facts.\n")
	}
	if name == "host release-bounded-admission" {
		out.WriteString("\nProvide the complete typed BoundedRunnerAdmissionRelease with --data <json> or --file <file|->. It binds request_id, host_id, recovery_authorization_id, the exact observed successor binding, runner_generation, journal, expected_control_revision and reason. The owner Envelope uses the payload request and host CAS; optional flags must match and no state is refreshed automatically.\n")
		out.WriteString("This independent owner decision releases global admission only after the successor is reconciled. It keeps every old Run quarantine, unknown native request, held slot and workspace; it does not declare old work complete or make an unobserved successor Ready. Use --dry-run to save the original Envelope. On unknown outcome, inspect tend request show <original-id> before an identical tend request replay --file <original-envelope.json>.\n")
	}
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
