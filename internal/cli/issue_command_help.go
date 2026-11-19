package cli

import (
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
)

var commandHelpSummaries = map[string]string{
	"host maintenance":       "Write (alias for host set-maintenance)",
	"host set-maintenance":   "Write runner admission",
	"host show":              "Read host state",
	"issue deliveries":       "Read recorded delivery observations",
	"issue dependencies":     "Write (alias for issue set-dependencies)",
	"issue refresh-delivery": "Queue a new provider observation",
	"issue reparent":         "Write the parent Issue",
	"issue set-dependencies": "Write the complete dependency list",
	"issue show":             "Read Issue details, including dependencies",
	"issue update-progress":  "Write the progress assessment",
}

const issueShowExample = `tend issue show "$ISSUE_ID" --json`

const issueWriteControls = `Use --data for inline JSON or --file - to read JSON from stdin.
Use --reason to record a reason. The response is JSON with or without --json.
For a first write, missing control values are read from the current Issue. Supply
--revision and --epoch (or JSON expected_assignment_epoch) to pin the version
you inspected. --dry-run prints the complete Envelope without submitting the
write. It may read current control values if they were omitted.
If the outcome is unknown, inspect the corresponding read command first. Retain
the original --request-id, --revision, --epoch and payload. Only if reconciliation
calls for resubmission, use the same values or submit the original saved Envelope
with tend operation submit --file original-envelope.json.
`

func issueCommandHelp(action string) string {
	switch action {
	case "show":
		return `Usage: tend issue show <issue-id> [--json] [--revision <issue-revision>]

Read one Issue. --json includes depends_on, the complete list of explicit
Issue IDs this Issue depends on for completion. It also includes revision,
assignment_epoch, parent_issue_id when set, and recorded progress.
Use --revision to inspect a historical Issue revision.

  ` + issueShowExample + ` | jq '.depends_on // []'
`
	case "set-dependencies", "dependencies":
		alias := ""
		if action == "dependencies" {
			alias = "\nCompatibility alias for tend issue set-dependencies.\n"
		}
		return fmt.Sprintf(`Usage: tend issue %s <issue-id> (--data <json> | --file <path|->) [flags]

Write: replace the entire explicit completion dependency list on this Issue.
Provide a JSON object with depends_on, an array of Issue IDs. Include every ID
you want to retain. Use {"depends_on":[]} to clear the list. An omitted or
null depends_on is rejected.
%s
  %s | jq '.depends_on // []'
  tend issue set-dependencies "$ISSUE_ID" --data "$(jq -cn --arg id "$DEPENDENCY_ID" '{depends_on:[$id]}')" --dry-run

`, action, alias, issueShowExample) + issueWriteControls
	case "reparent":
		return `Usage: tend issue reparent <issue-id> (--data <json> | --file <path|->) [flags]

Write: change the parent of this Issue. Provide parent_issue_id with the new
parent's Issue ID. Optionally pin that parent's revision with
expected_parent_revision. To move the Issue to the root, explicitly use
{"parent_issue_id":""} and omit expected_parent_revision. The server also
treats an omitted or null parent_issue_id as empty.

  ` + issueShowExample + ` | jq '.parent_issue_id'
  tend issue reparent "$ISSUE_ID" --data '{"parent_issue_id":""}' --dry-run

` + issueWriteControls
	case "update-progress":
		return fmt.Sprintf(`Usage: tend issue update-progress <issue-id> (--data <json> | --file <path|->) [flags]

Write: replace this Issue's progress assessment. Provide both summary and
next_step as strings (up to %d and %d Unicode code points respectively).
Use {"summary":"","next_step":""} to clear both. Omit delivery for an
Issue-wide assessment. For a specific candidate, delivery includes delivery_id,
provider, repository, and number from the linked delivery shown by
tend issue deliveries "$ISSUE_ID" --json. Copy head_sha and all baseline fields
from that same observation. Copy both base_sha and target_sha when both are
present. Omit a baseline field only when the observation omits it.
The assignee can also record conflict_action (seen or working) with
conflict_revision from last_conflict.revision.

  %s | jq '.progress'
  tend issue update-progress "$ISSUE_ID" --data '{"summary":"Checks passed","next_step":"Review the PR"}' --dry-run

`, protocol.MaxIssueProgressSummaryCharacters, protocol.MaxIssueProgressNextStepCharacters, issueShowExample) + issueWriteControls
	case "deliveries":
		return `Usage: tend issue deliveries <issue-id> [--json]

Read recorded delivery observations for one Issue. Output is JSON. Use the
delivery_id to identify a delivery. The read does not queue a provider refresh.

  tend issue deliveries "$ISSUE_ID" --json
`
	case "refresh-delivery":
		return `Usage: tend issue refresh-delivery <issue-id> (--data <json> | --file <path|->) [flags]

Write: queue a new provider observation for one linked delivery. Provide its
nonempty delivery_id from issue deliveries. Empty, null and omitted IDs cannot
select a delivery. The response is the recorded observation with a refresh
pending. Read issue deliveries again for the resulting provider state.

  tend issue deliveries "$ISSUE_ID" --json
  tend issue refresh-delivery "$ISSUE_ID" --data "$(jq -cn --arg id "$DELIVERY_ID" '{delivery_id:$id}')" --dry-run

` + issueWriteControls
	}
	return ""
}

func hostMaintenanceHelp(name string) string {
	alias := ""
	if name == "host maintenance" {
		alias = "\nCompatibility alias for tend host set-maintenance.\n"
	}
	return fmt.Sprintf(`Usage: tend %s <host-id> (--data <json> | --file <path|->) [flags]

Write: set admission for new runner allocations on one host. Provide mode as
"maintenance" to stop new allocations or "normal" to allow them again. This
does not stop running processes. Empty, null and omitted modes are rejected.
%s
  tend host show "$HOST_ID" --json
  tend host set-maintenance "$HOST_ID" --data '{"mode":"maintenance"}' --dry-run

Use --data for inline JSON or --file - to read JSON from stdin.
Use --reason to record a reason. The response is JSON with or without --json.
--dry-run prints the complete Envelope without submitting the write. For a
first write, omitting --revision reads current control_revision, including
in dry-run. Supply --revision to pin the control_revision from host show.
An omitted control_revision represents the initial revision 0.
If the outcome is unknown, read control_revision and, when present,
maintenance.mode and maintenance.phase in host show --json. In normal
operation the maintenance object can be absent. The view combines server
control and the runner's last report. It can still display maintenance after
the server has returned to normal. If the readback is inconclusive, keep the
outcome unknown.
Keep the original --request-id, --revision and payload. Resubmit only when
reconciliation calls for it, using those original values or the saved Envelope
with tend operation submit --file original-envelope.json.
`, name, alias)
}
