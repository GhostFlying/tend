package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

func runManagerRetirement(ctx context.Context, e *Env, args []string) error {
	fs := newFlags(e, "task retire-manager")
	fs.Bool("json", true, "print the complete operation receipt as JSON")
	data := fs.String("data", "", "complete original JSON payload; do not combine with proof flags")
	file := fs.String("file", "", "complete original JSON payload file; - reads stdin")
	request := fs.String("request-id", "", "original request ID for an identical retry")
	revision := fs.Int64("revision", -1, "required original task control revision; never refreshed automatically")
	dry := fs.Bool("dry-run", false, "export the original Envelope without sending")
	var payload protocol.RetireManagerRunPayload
	fs.StringVar(&payload.IssueID, "issue", "", "exact cancelling Issue ID")
	fs.Int64Var(&payload.ExpectedIssueRevision, "issue-revision", -1, "required original Issue revision")
	fs.Int64Var(&payload.ExpectedAssignmentEpoch, "assignment-epoch", 0, "required original Issue assignment epoch")
	fs.StringVar(&payload.RunID, "run", "", "exact original manager Run ID")
	fs.StringVar(&payload.HostID, "host", "", "exact original execution host ID")
	fs.StringVar(&payload.LeaseID, "lease", "", "exact original lease ID")
	fs.StringVar(&payload.ExecutionSHA256, "pin-sha256", "", "SHA256 of the original execution pin from manager-retirement")
	fs.StringVar(&payload.SourceMessageID, "source-message", "", "optional original owner source message; requires --source-revision")
	sourceRevision := fs.Int64("source-revision", -1, "original source message revision when --source-message is supplied")
	reason := fs.String("reason", "", "payload and Envelope reason with proof flags; Envelope reason only with JSON input")
	fs.Usage = func() {
		fmt.Fprintln(e.Stderr, "usage: tend task retire-manager TASK --revision REVISION [proof flags | --data JSON | --file FILE]\nRead task manager-retirement with the exact original Run before selecting its proof.\nRetirement requests the original manager stop; admission does not prove physical exit or finish Issue cancellation.\nRetain the complete payload, both revisions, assignment epoch and request ID; use --dry-run to export the Envelope.\nOn an unknown outcome, use request show and replay only the original saved Envelope.")
		fs.PrintDefaults()
	}
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || strings.TrimSpace(pos[0]) == "" || *revision < 0 {
		return usagef("retire-manager requires the exact task and its original --revision")
	}
	body, err := issueInput(e, *data, *file)
	if err != nil {
		return err
	}
	if len(body) > 0 {
		mixed := ""
		fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "issue", "issue-revision", "assignment-epoch", "run", "host", "lease", "pin-sha256", "source-message", "source-revision":
				mixed = f.Name
			}
		})
		if mixed != "" {
			return usagef("complete JSON input cannot be combined with --%s", mixed)
		}
		if json.Unmarshal(body, &payload) != nil {
			return usagef("manager retirement payload must be a JSON object")
		}
	} else {
		payload.Reason = *reason
		if *sourceRevision >= 0 {
			payload.ExpectedSourceRevision = sourceRevision
		} else if *sourceRevision != -1 {
			return usagef("source-revision must be nonnegative")
		}
		body, err = json.Marshal(payload)
		if err != nil {
			return err
		}
	}
	if strings.TrimSpace(payload.IssueID) == "" || payload.ExpectedIssueRevision <= 0 || payload.ExpectedAssignmentEpoch <= 0 || strings.TrimSpace(payload.RunID) == "" || strings.TrimSpace(payload.HostID) == "" || strings.TrimSpace(payload.LeaseID) == "" || !managerProofSHA256(payload.ExecutionSHA256) || strings.TrimSpace(payload.Reason) == "" {
		return usagef("original issue, issue-revision, assignment-epoch, run, host, lease, pin-sha256 and reason are required")
	}
	if (payload.SourceMessageID == "") != (payload.ExpectedSourceRevision == nil) || payload.ExpectedSourceRevision != nil && *payload.ExpectedSourceRevision < 0 {
		return usagef("source-message and its nonnegative source-revision must be supplied together")
	}
	if *request == "" {
		*request = e.IDs.New(protocol.PrefixRequest)
	}
	if !protocol.ValidID(protocol.PrefixRequest, *request) {
		return usagef("malformed request identity")
	}
	env := protocol.Envelope{RequestID: *request, Operation: protocol.OpRetireManagerRun, Target: protocol.Target{Type: "task", ID: pos[0]}, ExpectedControlRevision: revision, Payload: body, Reason: *reason}
	if *dry {
		return (owner{e: e, json: true}).printJSON(env)
	}
	c, err := e.client(protocol.ScopeOwner)
	if err != nil {
		return err
	}
	return sendDirectEnvelope(ctx, owner{e: e, c: c, json: true}, env)
}

func runManagerRetirementStatus(ctx context.Context, e *Env, args []string) error {
	fs := newFlags(e, "task manager-retirement")
	asJSON := fs.Bool("json", true, "print complete retirement state and original native request page as JSON")
	run := fs.String("run", "", "exact original manager Run ID")
	after := fs.String("after", "", "original native request page cursor; copy next_after unchanged")
	limit := fs.Int("limit", 50, "maximum original native requests in this page (1-100)")
	fs.Usage = func() {
		fmt.Fprintln(e.Stderr, "usage: tend task manager-retirement TASK --run RUN [--after CURSOR] [--limit COUNT] [--json]\nReads original ledger facts only. Copy the returned CAS identity and execution pin SHA256 to retire-manager.\nAdmission, observed state and unresolved native receipts remain separate; no payloads or credentials are returned.")
		fs.PrintDefaults()
	}
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || strings.TrimSpace(pos[0]) == "" || strings.TrimSpace(*run) == "" {
		return usagef("manager-retirement requires the exact task and --run")
	}
	if *limit < 1 || *limit > 100 {
		return usagef("limit must be between 1 and 100")
	}
	query := url.Values{"run_id": {*run}, "limit": {strconv.Itoa(*limit)}}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "after" {
			query.Set("after", *after)
		}
	})
	c, err := e.client(protocol.ScopeOwner)
	if err != nil {
		return err
	}
	var value json.RawMessage
	if err := c.do(ctx, "GET", "/api/v1/tasks/"+url.PathEscape(pos[0])+"/manager-retirement?"+query.Encode(), nil, &value); err != nil {
		return err
	}
	return printCapabilityValue(e, value, *asJSON)
}
