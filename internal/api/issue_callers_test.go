package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/cli"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestCanonicalIssueCallerHTTPAndCLILifecycle(t *testing.T) {
	e := newEnv(t)
	e.configure()
	app.WithFollowups(true)(e.app)
	token := e.login(protocol.ScopeOwner)
	issue := createOwnerIssueHTTP(t, e, token, "")
	plan := issue.Plan
	plan.Deliveries = []protocol.WorkItemDelivery{{DeliveryID: "delivery-1", Provider: "github", Repository: "example/project", Number: 7}}
	code, op := e.op(token, e.envelope("issues.update", protocol.Target{Type: "issue", ID: issue.IssueID}, &issue.Revision, protocol.UpdateIssuePayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Plan: plan}))
	if code != http.StatusOK || op.Operation.Status != protocol.OpSucceeded {
		t.Fatal(code, op)
	}
	if err := json.Unmarshal(op.Operation.Result, &issue); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"context", "followups", "deliveries"} {
		if code := e.do("GET", "/api/v1/issues/"+issue.IssueID+"/"+suffix, "", nil, nil); code != http.StatusUnauthorized {
			t.Fatal("non-owner Issue resource", suffix, code)
		}
	}
	dir := t.TempDir()
	deployment, err := e.app.DeploymentID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for n, v := range map[string]any{"config.json": map[string]string{"server": e.url}, "credentials.json": map[string]string{"client_id": "issue-callers", "owner_token": token, "deployment_id": deployment}} {
		raw, _ := json.Marshal(v)
		if err := os.WriteFile(filepath.Join(dir, n), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	var stdout, stderr bytes.Buffer
	env := &cli.Env{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr, ConfigDir: dir, HTTP: http.DefaultClient, IDs: e.ids}
	run := func(args ...string) []byte {
		t.Helper()
		stdout.Reset()
		stderr.Reset()
		if code := cli.Main(t.Context(), env, append([]string{"issue"}, args...)); code != 0 {
			t.Fatal(code, stdout.String(), stderr.String())
		}
		return append([]byte(nil), stdout.Bytes()...)
	}
	var context protocol.IssueContextView
	if json.Unmarshal(run("context", issue.IssueID), &context) != nil || context.IssueID != issue.IssueID || context.Revision < 1 {
		t.Fatal(context)
	}
	at := e.clock.Now().Add(time.Hour).UnixMilli()
	payload, _ := json.Marshal(map[string]any{"spec": protocol.FollowupSpec{Purpose: "Check current publication", Instruction: "Review existing delivery facts", Kind: "at", At: at, Timezone: "UTC"}})
	var result protocol.APIResponse
	if json.Unmarshal(run("followup", "create", issue.IssueID, "--data", string(payload)), &result) != nil || result.Operation == nil {
		t.Fatal(result)
	}
	var rule protocol.IssueFollowupRule
	if json.Unmarshal(result.Operation.Result, &rule) != nil || rule.IssueID != issue.IssueID {
		t.Fatal(rule)
	}
	request := result.Operation.RequestID
	var replay protocol.APIResponse
	if json.Unmarshal(run("followup", "create", issue.IssueID, "--data", string(payload), "--request-id", request, "--revision", strconv.FormatInt(issue.Revision, 10), "--epoch", strconv.FormatInt(issue.AssignmentEpoch, 10)), &replay) != nil || string(replay.Operation.Result) != string(result.Operation.Result) {
		t.Fatal("CLI replay changed rule", replay)
	}
	if code := cli.Main(t.Context(), env, []string{"issue", "followup", "create", issue.IssueID, "--request-id", request, "--data", string(payload)}); code != 2 {
		t.Fatal("retry silently refreshed current controls", code)
	}
	for _, action := range []string{"pause", "resume", "update", "trigger", "cancel"} {
		args := []string{"followup", action, issue.IssueID, rule.RuleID, "--reason", "Owner controls the agreed check"}
		if action == "update" {
			args = append(args, "--data", string(payload))
		}
		if json.Unmarshal(run(args...), &result) != nil || json.Unmarshal(result.Operation.Result, &rule) != nil {
			t.Fatal(result)
		}
	}
	var history protocol.IssueFollowupHistory
	if json.Unmarshal(run("followup-history", issue.IssueID, rule.RuleID), &history) != nil || history.Rule.Status != "cancelled" || len(history.Revisions) < 6 {
		t.Fatal(history)
	}
	var page protocol.IssueFollowupPage
	if json.Unmarshal(run("followups", issue.IssueID), &page) != nil || len(page.Items) != 1 || page.Items[0].RuleID != rule.RuleID {
		t.Fatal(page)
	}
	if json.Unmarshal(run("refresh-delivery", issue.IssueID, "--data", `{"delivery_id":"delivery-1"}`), &result) != nil || result.Operation.Status != protocol.OpSucceeded {
		t.Fatal(result)
	}
	var deliveries protocol.IssueDeliveryPage
	if json.Unmarshal(run("deliveries", issue.IssueID), &deliveries) != nil || len(deliveries.Items) != 1 || !deliveries.Items[0].Observation.RefreshPending {
		t.Fatal(deliveries)
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(result.Operation.Result, &raw) != nil || raw["work_item_id"] != nil || raw["issue_id"] == nil {
		t.Fatal("canonical response retained old top-level field", raw)
	}
	other := createOwnerIssueHTTP(t, e, token, "")
	if status := e.do("GET", "/api/v1/issues/"+other.IssueID+"/followups/"+rule.RuleID+"/history", token, nil, nil); status != http.StatusNotFound {
		t.Fatal("mismatched Issue history", status)
	}
}
