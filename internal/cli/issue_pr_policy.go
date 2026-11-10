package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

// runIssuePRPolicy freezes a complete Issue update before sending it. Recovery
// uses issue replay with that Envelope, without rebuilding the current plan.
func runIssuePRPolicy(ctx context.Context, e *Env, args []string) error {
	if len(args) == 0 || args[0] != "set" && args[0] != "clear" && args[0] != "show" {
		return usagef("issue pr-policy expects show|set|clear")
	}
	action := args[0]
	fs := newFlags(e, "issue pr-policy "+action)
	fs.Usage = func() { fmt.Fprint(e.Stderr, commandUsage([]string{"issue", "pr-policy", action})); fs.PrintDefaults() }
	mode := fs.String("mode", "", "auto or manual delivery; deployment requires explicit target scope")
	deployServer := fs.String("deploy-server-instance", "", "explicit configured server instance covered by automatic deployment authority")
	deployHosts := fs.String("deploy-hosts", "", "comma-separated runner identities covered by deployment authority; empty means server only")
	repository := fs.String("repository", "", "exact repository covered by the integration policy")
	reason := fs.String("reason", "", "reason for setting or revoking this integration policy")
	revision := fs.Int64("revision", -1, "Issue version to read and compare; defaults to the current Issue")
	epoch := fs.Int64("epoch", -1, "original assignment epoch; defaults to the selected Issue version")
	source := fs.String("source-message", "", "actual owner source message, when applicable")
	sourceRevision := fs.Int64("source-revision", -1, "original source content version")
	dry := fs.Bool("dry-run", false, "read the selected Issue and print the frozen update Envelope without writing")
	fs.Bool("json", true, "print the complete structured result")
	pos, err := parse(fs, args[1:])
	if err != nil {
		return err
	}
	if len(pos) != 1 || *revision == 0 || *revision < -1 || *epoch == 0 || *epoch < -1 {
		return usagef("issue pr-policy takes one Issue ID and positive explicit revision/epoch")
	}
	if action == "set" && (*mode != "auto" && *mode != "manual" || strings.TrimSpace(*repository) == "" || strings.TrimSpace(*reason) == "") {
		return usagef("set requires --mode auto|manual, --repository and --reason")
	}
	if action == "clear" && strings.TrimSpace(*reason) == "" {
		return usagef("clear requires --reason")
	}
	if (*deployServer != "" || *deployHosts != "") && (action != "set" || *mode != "auto" || strings.TrimSpace(*deployServer) == "") {
		return usagef("deployment scope requires set --mode auto --deploy-server-instance; --deploy-hosts alone grants nothing")
	}
	var deployment *protocol.IssueDeploymentScope
	if *deployServer != "" {
		if *deployServer != strings.TrimSpace(*deployServer) {
			return usagef("--deploy-server-instance requires an exact configured identity")
		}
		hosts := []string{}
		if *deployHosts != "" {
			for _, host := range strings.Split(*deployHosts, ",") {
				if strings.TrimSpace(host) == "" {
					return usagef("--deploy-hosts must contain nonempty runner identities")
				}
				hosts = append(hosts, strings.TrimSpace(host))
			}
		}
		slices.Sort(hosts)
		hosts = slices.Compact(hosts)
		deployment = &protocol.IssueDeploymentScope{ServerInstance: *deployServer, HostIDs: hosts}
	}
	if *sourceRevision != -1 && (*source == "" || *sourceRevision < 1) || *source != "" && *sourceRevision < 1 {
		return usagef("--source-message and a positive --source-revision must be supplied together")
	}
	c, err := e.client(protocol.ScopeOwner)
	if err != nil {
		return err
	}
	path := "/api/v1/issues/" + url.PathEscape(pos[0])
	if *revision > 0 {
		path += "?revision=" + strconv.FormatInt(*revision, 10)
	}
	var issue protocol.IssueView
	if err := c.do(ctx, "GET", path, nil, &issue); err != nil {
		return err
	}
	if action == "show" {
		return (owner{e: e, c: c, json: true}).printJSON(struct {
			IssueID  string                  `json:"issue_id"`
			Revision int64                   `json:"revision"`
			Policy   *protocol.IssuePRPolicy `json:"pr_policy"`
		}{issue.IssueID, issue.Revision, issue.Plan.PRPolicy})
	}
	if *epoch < 0 {
		*epoch = issue.AssignmentEpoch
	}
	if *revision < 0 {
		*revision = issue.Revision
	}
	plan := issue.Plan
	plan.PRPolicy = nil
	if action == "set" {
		plan.PRPolicy = &protocol.IssuePRPolicy{Mode: *mode, Repository: *repository, Reason: *reason, Deployment: deployment}
		if previous := issue.Plan.PRPolicy; previous != nil && previous.Mode == *mode && previous.Repository == *repository && previous.Reason == *reason && reflect.DeepEqual(previous.Deployment, plan.PRPolicy.Deployment) {
			plan.PRPolicy.Authority = previous.Authority
		}
	}
	payload := protocol.UpdateIssuePayload{Plan: plan, ExpectedAssignmentEpoch: *epoch}
	if *source != "" {
		payload.SourceMessageID = *source
		payload.ExpectedSourceRevision = sourceRevision
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	forward := []string{"update", pos[0], "--revision", strconv.FormatInt(*revision, 10), "--epoch", strconv.FormatInt(*epoch, 10), "--reason", *reason, "--data", string(body), "--json"}
	if *dry {
		forward = append(forward, "--dry-run")
	}
	return runIssue(ctx, e, forward)
}
