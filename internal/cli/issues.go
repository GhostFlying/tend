package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

var issueOperations = map[string]string{
	"refresh-delivery": "issues.refresh_delivery", "followups-create": "issues.followups_create", "followups-update": "issues.followups_update", "followups-pause": "issues.followups_pause", "followups-resume": "issues.followups_resume", "followups-cancel": "issues.followups_cancel", "followups-trigger": "issues.followups_trigger",

	"migrate-manager": "issues.manager_migration", "request-manager": "issues.request_manager", "assign": "issues.assign", "finish-assignment": "issues.finish_assignment",
	"update-progress": "issues.update_progress", "create": "issues.create", "update": "issues.update", "pause": "issues.pause", "resume": "issues.resume", "cancel": "issues.cancel", "finish-cancel": "issues.finish_cancel", "close": "issues.close", "reopen": "issues.reopen", "reparent": "issues.reparent", "dependencies": "issues.dependencies", "review-sources": "issues.review_sources", "review-completions": "issues.review_completions", "create-task": "issues.create_task", "update-task": "issues.update_task",
}

func runIssue(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "context", "followups", "followup-history", "deliveries", "refresh-delivery", "followup":
			return runIssueCaller(ctx, e, args)
		}
	}

	if len(args) > 0 && args[0] == "deadline" {
		return runPlanningRecord(ctx, e, args[1:], true)
	}
	if len(args) == 0 {
		return usagef("issue expects list|show|context|followups|followup-history|followup|deliveries|refresh-delivery|create|update|pause|resume|cancel|finish-cancel|close|reopen|reparent|dependencies|migrate-manager|request-manager|assign|finish-assignment|review-sources|review-completions|create-task|update-task|request|replay")
	}
	action := args[0]
	fs := newFlags(e, "issue "+action)
	data := fs.String("data", "", "inline JSON payload (or original Envelope for replay)")
	file := fs.String("file", "", "JSON payload file; - reads stdin")
	request := fs.String("request-id", "", "reuse the original request identity with its original revision, epoch and payload")
	revision := fs.Int64("revision", -1, "expected control revision; first requests may read the current value")
	epoch := fs.Int64("epoch", -1, "expected assignment epoch; first requests may read the current value")
	dry := fs.Bool("dry-run", false, "print the complete original Envelope without sending it")
	project := fs.String("project", "", "issue project filter")
	parent := fs.String("parent", "", "list direct children of this issue")
	roots := fs.Bool("roots", false, "list root issues")
	state := fs.String("state", "", "issue state filter")
	all := fs.Bool("all", false, "include closed and cancelled issues")
	after := fs.String("after", "", "list cursor from the preceding page")
	limit := fs.Int("limit", 50, "page size, up to 100")
	pos, err := parse(fs, args[1:])
	if err != nil {
		return err
	}
	if *revision == 0 || *revision < -1 || *epoch == 0 || *epoch < -1 {
		return usagef("revision and epoch must be positive when provided")
	}
	c, err := e.client(protocol.ScopeOwner)
	if err != nil {
		return err
	}
	o := owner{e: e, c: c, json: true}
	switch action {
	case "list":
		if len(pos) != 0 || (*roots && *parent != "") {
			return usagef("issue list takes no ID and at most one of --roots/--parent")
		}
		query := url.Values{"limit": {strconv.Itoa(*limit)}}
		if *project != "" {
			query.Set("project", *project)
		}
		if *state != "" {
			query.Set("state", *state)
		}
		if *all {
			query.Set("include_closed", "true")
		}
		if *after != "" {
			query.Set("after", *after)
		}
		if *roots || *parent != "" {
			query.Set("parent_id", *parent)
		}
		var page protocol.IssuePage
		if err := c.do(ctx, "GET", "/api/v1/issues?"+query.Encode(), nil, &page); err != nil {
			return err
		}
		return o.printJSON(page)
	case "show":
		if len(pos) != 1 {
			return usagef("issue show takes one issue ID")
		}
		path := "/api/v1/issues/" + url.PathEscape(pos[0])
		if *revision >= 0 {
			path += "?revision=" + strconv.FormatInt(*revision, 10)
		}
		var view protocol.IssueView
		if err := c.do(ctx, "GET", path, nil, &view); err != nil {
			return err
		}
		return o.printJSON(view)
	case "request":
		if len(pos) != 1 {
			return usagef("issue request takes one original request ID")
		}
		var view protocol.RequestView
		if err := c.do(ctx, "GET", "/api/v1/requests/"+url.PathEscape(pos[0]), nil, &view); err != nil {
			return err
		}
		return o.printJSON(view)
	case "replay":
		if len(pos) != 0 || *request != "" || *revision != -1 || *epoch != -1 {
			return usagef("issue replay uses the complete original Envelope via --file or --data; do not override its identity or revisions")
		}
		body, err := issueInput(e, *data, *file)
		if err != nil {
			return err
		}
		var env protocol.Envelope
		if err := json.Unmarshal(body, &env); err != nil {
			return usagef("invalid original Envelope: %v", err)
		}
		if !protocol.ValidID(protocol.PrefixRequest, env.RequestID) || !strings.HasPrefix(env.Operation, "issues.") || env.ParentRequestID != "" {
			return usagef("replay requires an independent original Issue owner request")
		}
		known := false
		for _, op := range issueOperations {
			known = known || op == env.Operation
		}
		if !known {
			return usagef("issue operation %q is not published", env.Operation)
		}
		if *dry {
			return o.printJSON(env)
		}
		return sendIssueEnvelope(ctx, o, env)
	}
	operation, ok := issueOperations[action]
	if !ok {
		return usagef("unknown issue command %q", action)
	}
	target := protocol.Target{Type: "issue"}
	if action == "create" {
		if len(pos) != 0 || *revision != -1 || *epoch != -1 {
			return usagef("issue create has no current ID/revision/epoch")
		}
	} else {
		if len(pos) != 1 {
			return usagef("issue %s takes one ID", action)
		}
		target.ID = pos[0]
	}
	if action == "update-task" {
		target.Type = "task"
	}
	body, err := issueInput(e, *data, *file)
	if err != nil {
		return err
	}
	if len(body) == 0 {
		body = []byte(`{}`)
	}
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return usagef("issue payload must be a JSON object")
	}
	if action != "create" {
		var suppliedEpoch int64
		if raw, ok := fields["expected_assignment_epoch"]; ok {
			if err := json.Unmarshal(raw, &suppliedEpoch); err != nil || suppliedEpoch < 1 {
				return usagef("expected_assignment_epoch must be a positive integer")
			}
		}
		if *epoch >= 0 {
			if suppliedEpoch > 0 && suppliedEpoch != *epoch {
				return usagef("--epoch differs from the payload")
			}
			suppliedEpoch = *epoch
		}
		if *request != "" && (*revision < 1 || suppliedEpoch < 1) {
			return usagef("request recovery requires the original --revision and --epoch (or expected_assignment_epoch payload)")
		}
		if *revision < 1 || suppliedEpoch < 1 {
			var issueID string
			if target.Type == "task" {
				var task protocol.TaskDetail
				if err := c.do(ctx, "GET", "/api/v1/tasks/"+url.PathEscape(target.ID), nil, &task); err != nil {
					return err
				}
				issueID = task.PrimaryIssueID
				if issueID == "" {
					return usagef("task has no canonical primary Issue")
				}
				if *revision < 1 {
					*revision = task.ControlRevision
				}
			} else {
				issueID = target.ID
			}
			var issue protocol.IssueView
			if err := c.do(ctx, "GET", "/api/v1/issues/"+url.PathEscape(issueID), nil, &issue); err != nil {
				return err
			}
			if target.Type == "issue" && *revision < 1 {
				*revision = issue.Revision
			}
			if suppliedEpoch < 1 {
				suppliedEpoch = issue.AssignmentEpoch
			}
		}
		fields["expected_assignment_epoch"], err = json.Marshal(suppliedEpoch)
		if err != nil {
			return err
		}
	}
	body, err = json.Marshal(fields)
	if err != nil {
		return err
	}
	if *request == "" {
		*request = e.IDs.New(protocol.PrefixRequest)
	} else if !protocol.ValidID(protocol.PrefixRequest, *request) {
		return usagef("request-id must be a valid request identity")
	}
	env := protocol.Envelope{RequestID: *request, Operation: operation, Target: target, Payload: body}
	if action != "create" {
		value := *revision
		env.ExpectedControlRevision = &value
	}
	if *dry {
		return o.printJSON(env)
	}
	return sendIssueEnvelope(ctx, o, env)
}

func issueInput(e *Env, data, file string) ([]byte, error) {
	if data != "" && file != "" {
		return nil, usagef("use at most one of --data and --file")
	}
	if data != "" {
		return []byte(data), nil
	}
	if file == "-" {
		return io.ReadAll(io.LimitReader(e.Stdin, 1<<20))
	}
	if file != "" {
		return os.ReadFile(file)
	}
	return nil, nil
}

func sendIssueEnvelope(ctx context.Context, o owner, env protocol.Envelope) error {
	// Visible before any HTTP; recovery preserves this ID and the original input.
	fmt.Fprintf(o.e.Stderr, "request %s (%s)", env.RequestID, env.Operation)
	if env.ExpectedControlRevision != nil {
		fmt.Fprintf(o.e.Stderr, " --revision %d", *env.ExpectedControlRevision)
	}
	var control struct {
		Epoch int64 `json:"expected_assignment_epoch"`
	}
	_ = json.Unmarshal(env.Payload, &control)
	if control.Epoch > 0 {
		fmt.Fprintf(o.e.Stderr, " --epoch %d", control.Epoch)
	}
	fmt.Fprintf(o.e.Stderr, "; inspect with tend issue request %s; retry only the original request\n", env.RequestID)
	response, err := o.sendEnvelope(ctx, env)
	if err != nil {
		return err
	}
	if err := o.printJSON(response); err != nil {
		return err
	}
	if response.Operation == nil {
		if response.Error != nil {
			return response.Error
		}
		return errors.New("the server sent no outcome")
	}
	switch response.Operation.Status {
	case protocol.OpSucceeded:
		return nil
	case protocol.OpFailed:
		if response.Operation.Error != nil {
			return response.Operation.Error
		}
		return errors.New("the operation failed without a reason")
	default:
		return protocol.Errorf(protocol.CodeUnknownOutcome, "request %s is %s; inspect/reconcile this request before any new action", env.RequestID, response.Operation.Status)
	}
}
