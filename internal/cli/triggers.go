package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func runTriggers(ctx context.Context, e *Env, noun string, args []string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	action := args[0]
	fs := newFlags(e, noun+" "+action)
	issue := fs.String("issue", "", "Issue identity")
	source := fs.String("source", "", "followup, observation or webhook")
	schedule := fs.String("schedule", "", "exact schedule or subscription identity")
	status := fs.String("status", "", "exact processing or schedule state")
	search := fs.String("search", "", "title or associated ID")
	after := fs.String("after", "", "exclusive page cursor")
	afterRevision := fs.Int64("after-revision", 0, "history revision cursor")
	afterOccurrence := fs.String("after-occurrence", "", "history occurrence cursor")
	afterSource := fs.Int64("after-source", 0, "batch source cursor")
	since := fs.Int64("since", 0, "UTC milliseconds, inclusive")
	until := fs.Int64("until", 0, "UTC milliseconds, inclusive")
	limit := fs.Int("limit", 50, "page size, 1..100")
	asJSON := fs.Bool("json", false, "print full structured results")
	file := fs.String("file", "", "JSON input file; - reads stdin")
	data := fs.String("data", "", "JSON input")
	keyFile := fs.String("secret-file", "", "private key file; - reads stdin (never a command-line secret)")
	request := fs.String("request-id", "", "original request identity")
	revision := fs.Int64("revision", -1, "expected control revision")
	epoch := fs.Int64("epoch", -1, "expected assignment epoch")
	reason := fs.String("reason", "", "control reason")
	dry := fs.Bool("dry-run", false, "print exact request without side effects")
	delivery := fs.String("delivery", "", "delivery reference within the Issue")
	pos, err := parse(fs, args[1:])
	if err != nil {
		return err
	}
	if *limit < 1 || *limit > 100 || *since < 0 || *until < 0 {
		return usagef("invalid page size or time range")
	}
	c, err := e.client(protocol.ScopeOwner)
	if err != nil {
		return err
	}
	o := owner{e: e, c: c, json: *asJSON}
	collection := map[string]string{"schedule": "schedules", "event": "events", "webhook": "webhooks", "observation": "schedules"}[noun]
	base := "/api/v1/diagnostics/" + collection
	query := url.Values{"issue_id": {*issue}, "schedule_id": {*schedule}, "source": {*source}, "status": {*status}, "search": {*search}, "after": {*after}, "since": {strconv.FormatInt(*since, 10)}, "until": {strconv.FormatInt(*until, 10)}, "limit": {strconv.Itoa(*limit)}}
	if noun == "observation" {
		query.Set("source", "observation")
	}
	if noun == "event" && action == "sources" {
		if len(pos) != 1 || *afterSource < 0 {
			return usagef("event sources takes one event ID and a nonnegative source cursor")
		}
		return readOwnerJSON(ctx, owner{e: e, c: c, json: true}, base+"/"+url.PathEscape(pos[0])+"/sources?after="+strconv.FormatInt(*afterSource, 10))
	}
	if action == "list" || noun == "webhook" && action == "receipts" || action == "watch" {
		if len(pos) > 1 || len(pos) > 0 && !(noun == "webhook" && action == "receipts") {
			return usagef("list/watch takes no positional ID")
		}
		if action == "receipts" {
			base = "/api/v1/diagnostics/events"
			query.Set("source", "webhook")
			if len(pos) == 1 {
				query.Set("schedule_id", pos[0])
			}
		}
		if action == "watch" && noun != "event" {
			return usagef("watch is an event command")
		}
		seen := map[string]string{}
		for {
			var raw json.RawMessage
			if err = c.do(ctx, "GET", base+"?"+query.Encode(), nil, &raw); err != nil {
				return err
			}
			if action != "watch" {
				return printTriggerValue(o, raw)
			}
			var page protocol.TriggerEventPage
			if err = json.Unmarshal(raw, &page); err != nil {
				return err
			}
			for i := len(page.Items) - 1; i >= 0; i-- {
				v := page.Items[i]
				b, _ := json.Marshal(v)
				if seen[v.ID] != string(b) {
					seen[v.ID] = string(b)
					if err = printTriggerValue(o, b); err != nil {
						return err
					}
				}
			}
			if page.More {
				query.Set("after", page.Next)
				continue
			}
			query.Del("after")

			timer := time.NewTimer(2 * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil
			case <-timer.C:
			}
		}
	}
	if noun == "request" && action == "replay" {
		body, err := issueInput(e, *data, *file)
		if err != nil {
			return err
		}

		var env protocol.Envelope
		if json.Unmarshal(body, &env) != nil {
			return usagef("replay requires an original Envelope")
		}
		if !strings.HasPrefix(env.Operation, "issues.") && !strings.HasPrefix(env.Operation, "webhooks.") {
			return usagef("unsupported owner replay operation")
		}
		if env.Operation == "webhooks.receive" {
			return usagef("external events are replayed by their signed sender")
		}
		if *dry {
			return o.printJSON(env)
		}
		return sendIssueEnvelope(ctx, o, env)
	}
	if action == "show" || action == "trace" || noun == "request" {
		if len(pos) != 1 {
			return usagef("%s takes one ID", action)
		}
		if noun == "request" {
			base = "/api/v1/requests"
		}
		var raw json.RawMessage
		if err = c.do(ctx, "GET", base+"/"+url.PathEscape(pos[0]), nil, &raw); err != nil {
			return err
		}
		return printTriggerValue(o, raw)
	}
	if noun == "observation" && action == "refresh" {
		if *issue == "" || *delivery == "" {
			return usagef("observation refresh requires --issue and --delivery")
		}
		payload, _ := json.Marshal(map[string]string{"delivery_id": *delivery})
		return runIssueCaller(ctx, e, append([]string{"refresh-delivery", *issue, "--data", string(payload)}, triggerControlFlags(*request, *revision, *epoch, *reason, *dry)...))
	}
	if noun == "schedule" {
		if action == "create" {
			if *issue == "" || len(pos) != 0 {
				return usagef("schedule create requires --issue and a spec")
			}
		} else {
			if len(pos) != 1 {
				return usagef("schedule control requires a rule ID")
			}
			var schedule protocol.TriggerSchedule
			if err = c.do(ctx, "GET", base+"/"+url.PathEscape(pos[0]), nil, &schedule); err != nil {
				return err
			}
			if schedule.Source != "followup" {
				return usagef("only followup schedules accept rule controls")
			}
			if *issue != "" && *issue != schedule.IssueID {
				return usagef("rule belongs to another Issue")
			}
			*issue = schedule.IssueID
		}
		if action == "history" {
			if *after != "" || *afterRevision < 0 {
				return usagef("history uses --after-revision and --after-occurrence")
			}
			return runIssueCaller(ctx, e, []string{"followup-history", *issue, pos[0], "--after-revision", strconv.FormatInt(*afterRevision, 10), "--after-occurrence", *afterOccurrence})
		}
		body, err := issueInput(e, *data, *file)
		if err != nil {
			return err
		}
		var spec protocol.FollowupSpec
		payload := map[string]any{}
		if len(body) > 0 {
			if err = json.Unmarshal(body, &spec); err != nil {
				return usagef("invalid followup spec: %v", err)
			}
			payload["spec"] = &spec
		}
		raw, _ := json.Marshal(payload)
		tail := []string{"followup", action, *issue}
		if action != "create" {
			tail = append(tail, pos[0])
		}
		tail = append(tail, "--data", string(raw))
		return runIssueCaller(ctx, e, append(tail, triggerControlFlags(*request, *revision, *epoch, *reason, *dry)...))
	}
	if noun != "webhook" {
		return usagef("unknown %s command %q", noun, action)
	}
	if action != "create" && action != "update" && action != "pause" && action != "resume" && action != "cancel" && action != "rotate" {
		return usagef("unknown webhook operation")
	}
	target := protocol.Target{Type: "webhook"}
	var current protocol.WebhookSubscription
	if action == "create" {
		if *issue == "" || len(pos) != 0 {
			return usagef("webhook create requires --issue")
		}
		target = protocol.Target{Type: "issue", ID: *issue}
	} else {
		if len(pos) != 1 {
			return usagef("webhook control takes one subscription ID")
		}
		target.ID = pos[0]
		if err = c.do(ctx, "GET", base+"/"+url.PathEscape(pos[0]), nil, &current); err != nil {
			return err
		}
		if *issue != "" && *issue != current.IssueID {
			return usagef("subscription belongs to another Issue")
		}
		*issue = current.IssueID
	}
	body, err := issueInput(e, *data, *file)
	if err != nil {
		return err
	}
	p := protocol.WebhookSubscriptionPayload{}
	if len(body) > 0 {
		if err = json.Unmarshal(body, &p); err != nil {
			return usagef("invalid webhook metadata")
		}
	}
	if *epoch > 0 && p.ExpectedAssignmentEpoch > 0 && p.ExpectedAssignmentEpoch != *epoch {
		return usagef("epoch differs from payload")
	}
	if *epoch > 0 {
		p.ExpectedAssignmentEpoch = *epoch
	}
	if *request != "" && (*revision < 1 || p.ExpectedAssignmentEpoch < 1) {
		return usagef("retry needs original --revision and --epoch")
	}
	if *revision < 1 || p.ExpectedAssignmentEpoch < 1 {
		var v protocol.IssueView
		if err = c.do(ctx, "GET", "/api/v1/issues/"+url.PathEscape(*issue), nil, &v); err != nil {
			return err
		}
		if p.ExpectedAssignmentEpoch < 1 {
			p.ExpectedAssignmentEpoch = v.AssignmentEpoch
		}
		if *revision < 1 {
			*revision = v.Revision
			if action != "create" {
				*revision = current.Revision
			}
		}
	}
	if *request == "" {
		*request = e.IDs.New(protocol.PrefixRequest)
	}
	if !protocol.ValidID(protocol.PrefixRequest, *request) {
		return usagef("invalid request ID")
	}
	if *keyFile != "" {
		if action != "create" && action != "rotate" {
			return usagef("credentials change only with create or rotate")
		}
		p.KeyRef = *request
		if !*dry {
			var key []byte
			if *keyFile == "-" {
				if *file == "-" {
					return usagef("payload and key cannot both consume stdin")
				}
				key, err = io.ReadAll(io.LimitReader(e.Stdin, 4097))
			} else {
				key, err = os.ReadFile(*keyFile)
			}
			if err != nil {
				return err
			}
			if len(key) < 32 || len(key) > 4096 {
				return usagef("private key must contain 32..4096 bytes")
			}
			if err = stageCLIWebhookKey(ctx, c, p.KeyRef, key); err != nil {
				return err
			}
		}
	}
	raw, _ := json.Marshal(p)
	env := protocol.Envelope{RequestID: *request, Operation: "webhooks." + action, Target: target, ExpectedControlRevision: revision, Payload: raw, Reason: *reason}
	if *dry {
		return o.printJSON(env)
	}
	return sendIssueEnvelope(ctx, o, env)
}
func triggerControlFlags(request string, revision, epoch int64, reason string, dry bool) []string {
	args := []string{"--reason", reason}
	if request != "" {
		args = append(args, "--request-id", request)
	}
	if revision > 0 {
		args = append(args, "--revision", strconv.FormatInt(revision, 10))
	}
	if epoch > 0 {
		args = append(args, "--epoch", strconv.FormatInt(epoch, 10))
	}
	if dry {
		args = append(args, "--dry-run")
	}
	return args
}
func stageCLIWebhookKey(ctx context.Context, c *client, id string, key []byte) error {
	req, err := http.NewRequestWithContext(ctx, "PUT", strings.TrimSuffix(c.server, "/")+"/api/v1/webhook-keys/"+url.PathEscape(id), bytes.NewReader(key))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if c.deploymentID != "" {
		req.Header.Set("X-Tend-Deployment-ID", c.deploymentID)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return transportError{err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("stage private webhook key: HTTP %d", resp.StatusCode)
	}
	return nil
}
func printTriggerValue(o owner, raw json.RawMessage) error {
	if o.json {
		return o.printJSON(raw)
	}
	var page struct {
		Items []json.RawMessage `json:"items"`
		More  bool              `json:"more"`
		Next  string            `json:"next"`
	}
	if json.Unmarshal(raw, &page) == nil && page.Items != nil {
		for _, v := range page.Items {
			if err := printTriggerValue(o, v); err != nil {
				return err
			}
		}
		if len(page.Items) == 0 {
			fmt.Fprintln(o.e.Stdout, "No matching records.")
		}
		if page.More {
			fmt.Fprintf(o.e.Stdout, "Next page: --after %s\n", page.Next)
		}
		return nil
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	if _, ok := value["operation"]; ok {
		return o.printJSON(raw)
	}
	w := tabwriter.NewWriter(o.e.Stdout, 0, 4, 2, ' ', 0)
	for _, k := range []string{"id", "issue_id", "issue_title", "title", "source", "kind", "status", "reason", "next_at", "created_at", "request_id", "batch_id", "revision", "key_ref"} {
		if v, ok := value[k]; ok {
			fmt.Fprintf(w, "%s\t%v\n", k, v)
		}
	}
	if steps, ok := value["steps"].([]any); ok {
		for _, s := range steps {
			v := s.(map[string]any)
			fmt.Fprintf(w, "  %v\t%v\t%v\n", v["at"], v["kind"], v["detail"])
		}
	}
	if len(value) == 0 {
		fmt.Fprintln(w, "No record.")
	}
	return w.Flush()
}
