package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
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
execution, runs, delivery_authorization, artifacts, decisions.

  tend task show "$TASK_ID" --fields task_id,lifecycle,execution
`
	case "task waits":
		return `Usage: tend task waits <task-id> [--run <run-id>] [--status <status>] [--json]

Read recorded external waits, including the condition, stopped flag and
recovery action. Filter by exact original Run or status, or combine both.
Status values: stopping, waiting, continued, cancelled. Omit the filters
for all waits on the Task. Results are JSON and have no pagination.

  tend task waits "$TASK_ID" --run "$RUN_ID"
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
