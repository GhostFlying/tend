package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

func runDetailShow(ctx context.Context, e *Env, name, route string, args []string) error {
	fs := newFlags(e, name)
	fs.Usage = func() { fmt.Fprint(e.Stderr, auditReadHelp(name)) }
	asJSON := fs.Bool("json", false, "print JSON")
	fields := fs.String("fields", "", "comma-separated top-level JSON fields")
	revision := int64(-1)
	if name == "issue show" {
		fs.Int64Var(&revision, "revision", -1, "historical Issue revision")
	}
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("%s takes one ID", name)
	}
	if revision == 0 || revision < -1 {
		return usagef("revision must be positive when provided")
	}
	var selected []string
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "fields" {
			selected = strings.Split(*fields, ",")
		}
	})
	for i, field := range selected {
		field = strings.TrimSpace(field)
		if field == "" || strings.ContainsAny(field, ".[] /\t\r\n") {
			return usagef("--fields takes comma-separated top-level JSON field names")
		}
		selected[i] = field
	}
	path := expandCommandRoute(route, []string{"id"}, pos)
	if revision > 0 {
		path += "?revision=" + strconv.FormatInt(revision, 10)
	}
	c, err := e.client(protocol.ScopeOwner)
	if err != nil {
		return err
	}
	o := owner{e: e, c: c}
	if name == "issue show" && len(selected) == 0 {
		var view protocol.IssueView
		if err := c.do(ctx, "GET", path, nil, &view); err != nil {
			return err
		}
		if *asJSON {
			return o.printJSON(view)
		}
		return printIssueView(e, view)
	}

	var raw json.RawMessage
	if err := c.do(ctx, "GET", path, nil, &raw); err != nil {
		return err
	}
	if len(selected) == 0 {
		return printCapabilityValue(e, raw, *asJSON)
	}
	var full map[string]json.RawMessage
	if err := json.Unmarshal(raw, &full); err != nil {
		return err
	}
	projected := make(map[string]json.RawMessage, len(selected))
	for _, name := range selected {
		if value, ok := full[name]; ok {
			projected[name] = value
		}
	}
	return o.printJSON(projected)
}

func validateWaitSelection(query url.Values) error {
	runID, status := query.Get("run_id"), query.Get("status")
	if query.Has("run_id") && (runID == "" || strings.TrimSpace(runID) != runID) {
		return usagef("--run must be a nonempty original Run ID")
	}
	if query.Has("status") && !slices.Contains([]string{"stopping", "waiting", "continued", "cancelled"}, status) {
		return usagef("--status must be one of stopping, waiting, continued or cancelled")
	}
	return nil
}

// Older servers return the full Task history. Keep the same selection on the
// CLI and carry the matched JSON records through without retyping their values.
func selectWaits(raw json.RawMessage, runID, status string) (json.RawMessage, error) {
	var waits []json.RawMessage
	if err := json.Unmarshal(raw, &waits); err != nil {
		return nil, err
	}
	selected := make([]json.RawMessage, 0, len(waits))
	for _, record := range waits {
		var key struct {
			RunID  string `json:"run_id"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal(record, &key); err != nil {
			return nil, err
		}
		if (runID == "" || key.RunID == runID) && (status == "" || key.Status == status) {
			selected = append(selected, record)
		}
	}
	return json.Marshal(selected)
}

func auditReadHelp(name string) string {
	const fieldsHelp = `--fields prints a JSON object with the selected top-level keys. Use a
comma-separated list of exact keys from the full JSON. Absent keys are omitted.
Present null, empty and unknown values are preserved. Omit it for full output.
`
	switch name {
	case "issue show":
		return `Usage: tend issue show <issue-id> [--json] [--revision <issue-revision>] [--fields <keys>]

Read one Issue. Use --revision for a historical Issue revision.
` + fieldsHelp + `
Common audit fields: issue_id, revision, assignment_epoch, effective_work_status,
manager, responsibility_runs, tasks, progress, depends_on, delivery_duties.

  tend issue show "$ISSUE_ID" --fields issue_id,effective_work_status,manager
`
	case "task show":
		return `Usage: tend task show <task-id> [--json] [--fields <keys>]

Read one Task.
` + fieldsHelp + `
Common audit fields: task_id, lifecycle, control_revision, block_reason,
execution, runs, delivery_authorization, artifacts, decisions. A missing legacy
delivery_authorization provides no evidence of permission.

  tend task show "$TASK_ID" --fields task_id,lifecycle,execution
`
	case "task waits":
		return `Usage: tend task waits <task-id> [--run <run-id>] [--status <status>] [--json]

Read recorded external waits, including the condition, stopped flag and
recovery action. Filter by exact original Run or status, or combine both.
Status values: stopping, waiting, continued, cancelled. Omit the filters
for all waits on the Task. Results are JSON and have no pagination.
The CLI also applies the selection when a server returns the full list.

  tend task waits "$TASK_ID" --run "$RUN_ID"
`
	case "task manager-reconciliation":
		return `Usage: tend task manager-reconciliation <task-id> --run <run-id> [--json]

Read the original Issue-manager Run's recorded stop and reconciliation facts.
Use its native close and stop receipts, unapplied events, unresolved requests
and blockers to assess the original Run. This read has no pagination.
`
	case "run maintenance-reconciliation":
		return `Usage: tend run maintenance-reconciliation <run-id> [--json]

Read ledger evidence for the original retained ordinary Orca Run. The response
includes its execution identity, maintenance protections and outstanding
receipts. can_continue is a ledger assessment. This read has no pagination.
`
	case "task ci-continuation":
		return `Usage: tend task ci-continuation <owner-task-id> <run-id|latest> [--json]

Read the original CI wait, observed candidate and recorded source/wake state.
Use the original Run ID when comparing a wait, candidate and source message.
This read has no pagination.
`
	case "run logs":
		return `Usage: tend run logs <run-id> [--after <seq>] [--wait <seconds>] [--json]

Read recorded Run logs in pages of up to 500. Pass the returned next sequence
to --after for later logs. --wait waits for new records on a live Run with a
server limit of 25 seconds.
`
	case "issue context":
		return `Usage: tend issue context <issue-id> [--after-source <seq>] [--json]
       tend issue context <issue-id> --view incremental [--cursor <token>] [--limit <1..100>] [--json]

Read retained Issue context. The default legacy view pages source messages
with --after-source, using next while more is true. --after is an alias and
must agree when both are passed. Find omitted tool bodies using original_message_id
with issue materials --kind messages --id <message-id>.

For a current snapshot and later event deltas use --view incremental. Pass
next_cursor to --cursor until more is false, and keep it to read later deltas.
The default page size is 50. through fixes the event interval and values are
current at read time. Cursor scope or history errors require a fresh snapshot.
This read leaves source acknowledgement and Task dispatch to their own commands.
`

	case "issue materials":
		return `Usage: tend issue materials <issue-id> [--kind <kind>] [--id <id>] [--duty-revision <n>] [--after <cursor>] [--limit <1..100>] [--json]

Read retained Issue context and original materials. --kind selects overview, issues, sources, facts, messages, tasks, artifacts, acceptances, questions, files, deliveries, reviews, deployment, delivery_authorization, capabilities, agent_capabilities, agent_capability_history or task_capabilities. Default overview returns the selected Issue context and separately identified legacy_contexts for retained historical work items. kind=issues discovers related Issues with --after and --limit; more=true and next identify the following page. Select a discovered Issue with the positional issue_id for a focused material read; no separate share grant is needed within the same named project. Other material kinds retain their own pages. --kind reviews uses --id <delivery_id> to select a delivery and --after/--limit to page its shared review list. shared_reviews.existing_evidence is a bounded contributor evidence index with candidate identities and original artifact/file references, not complete reports or check logs. Respect contributions_more and other truncation markers; read needed originals with --kind artifacts --id <artifact_id>, then download the original file IDs. Missing fields remain evidence not provided. --id otherwise selects one exact object. Use --after with the preceding next cursor and --limit 1..100; more=true means further pages remain. sources preserves linked message content, queued/withdrawn state and association versions; facts preserves original and superseded facts. Confirmed uploads from earlier Runs of the same Task can be discovered with kind=files and reused by file_id without reuploading. File sources with kind=upload retain the original task_id and locator run:<uploading_run_id>; status=uploaded records a confirmed original, not acceptance or a passed check for the current candidate. File sources_more marks incomplete provenance metadata. The full JSON preserves source, historical state, versions, timestamps and original file size/SHA256.
For capabilities, omit --id to list current active and retired definitions, or supply a capability ID for its revision history. agent_capabilities lists current public declarations, optionally selecting --id environment/agent. agent_capability_history requires --id environment/agent. task_capabilities requires a related Task ID and returns its requirements and matching explanation; the worker's read_context may omit this ID to inspect its own Task and role. Nested capability pages retain their original pagination and unknown states. delivery_authorization accepts --id <task_id> for current execution authority and its bounded historical Run index, or --id <original-run-id> --duty-revision <history.duty_revision> for one exact archived execution. history.execution_run_id and duty_revision are the immutable selector; run_id remains a compatibility alias. A Run without --duty-revision is accepted only when its history is unique, otherwise an explicit selector is required. For a superseded live binding without a Run, use --id <task_id> --duty-revision <history.duty_revision>; missing Run/artifact means none existed, not a completed result. If a Task/revision has multiple historical matches, select its real Run and revision from the history index. Archived valid=false grants no new execution; its settlement.eligible is an independent historical fact. A later candidate or installation can invalidate new execution while the original completed deployment remains eligible for settlement.
Reading does not admit a new instruction, grant a deployment or schedule a Run. No request_id or control revision is needed; an interrupted read may be repeated. Task spec.inputs records historical inputs and is not an Issue reading whitelist. Related Issues in the same non-empty project share discoverable materials by default; other projects retain explicit sharing, and an explicit object-level share revocation applies to that target and object. Original frozen Task inputs remain historical sources across handoff. Use file/attachment download with a returned original file_id for the actual bytes.
`
	case "message original":
		return `Usage: tend message original <message-id> [--json]

Read the retained original message by ID, including its recorded source facts.
This read has no pagination.
`
	case "artifact show":
		return `Usage: tend artifact show <artifact-id> [--json]

Read one immutable artifact, its report, file manifest and acceptance evidence.
Use file download with a returned file_id for the verified original bytes.
This read has no pagination.
`
	case "file download":
		return `Usage: tend file download <file-id> --out <path> [--json]

Read and verify an original file by ID, then save its bytes to --out.
--json prints the local path in a JSON object. This read has no pagination.
`

	case "issue activity":
		return `Usage: tend issue activity <issue-id> [--before <position>] [--json]
       tend issue activity <issue-id> --notification <position> [--after <cursor>] [--json]

Read recorded Issue activity as JSON. For older activity use the returned
next_before as --before. To read notification evidence for a specific event,
pass its position to --notification. If next is present, use it as --after.
`
	case "issue deliveries":
		return `Usage: tend issue deliveries <issue-id> [--json]

Read the Issue's recorded delivery observations as JSON. Use the delivery_id
and the recorded head, base/target and CI state to identify the candidate.
This read uses no pagination and queues no provider refresh. The write command
issue refresh-delivery queues a new observation.
`
	}
	return ""
}
