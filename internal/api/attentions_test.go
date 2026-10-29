package api

import (
	"bytes"
	"encoding/json"
	"github.com/GhostFlying/tend/internal/cli"
	"github.com/GhostFlying/tend/internal/protocol"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestAttentionCLIRealHTTPOriginalDecisionAndInboxIdentity(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	issue := createOwnerIssueHTTP(t, e, token, "")
	dir := t.TempDir()
	deployment, err := e.app.DeploymentID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]any{"config.json": map[string]string{"server": e.url}, "credentials.json": map[string]string{"client_id": "attention-cli", "owner_token": token, "deployment_id": deployment}} {
		raw, _ := json.Marshal(data)
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	var stdout, stderr bytes.Buffer
	env := &cli.Env{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr, ConfigDir: dir, HTTP: http.DefaultClient, IDs: e.ids}
	run := func(args ...string) protocol.APIResponse {
		t.Helper()
		stdout.Reset()
		stderr.Reset()
		if code := cli.Main(t.Context(), env, append([]string{"attention"}, args...)); code != 0 {
			t.Fatal(code, stdout.String(), stderr.String())
		}
		var v protocol.APIResponse
		if err := json.Unmarshal(stdout.Bytes(), &v); err != nil {
			t.Fatal(err, stdout.String())
		}
		return v
	}
	payload, _ := json.Marshal(protocol.CreateAttentionPayload{IssueID: issue.IssueID, Kind: "decision", Title: "Which result is needed?", Options: []string{"report", "changes"}, BlocksClose: true})
	created := run("create", "--data", string(payload))
	var a protocol.Attention
	if err := json.Unmarshal(created.Operation.Result, &a); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), created.Operation.RequestID) {
		t.Fatal("request identity was not visible")
	}
	var inbox protocol.AttentionPage
	if status := e.do("GET", "/api/v1/inbox?issue_id="+issue.IssueID, token, nil, &inbox); status != http.StatusOK || len(inbox.Items) != 1 || inbox.Items[0].AttentionID != a.AttentionID || inbox.Items[0].Decision.DecisionID != a.DecisionID {
		t.Fatal(status, inbox)
	}
	presented := run("archive", a.AttentionID, "--revision", strconv.FormatInt(a.Revision, 10))
	if err := json.Unmarshal(presented.Operation.Result, &a); err != nil {
		t.Fatal(err)
	}
	response := run("respond", a.AttentionID, "--revision", strconv.FormatInt(a.Revision, 10), "--data", `{"version":1,"option":"report"}`)
	var answered protocol.Attention
	if err := json.Unmarshal(response.Operation.Result, &answered); err != nil {
		t.Fatal(err)
	}
	if answered.AttentionID != a.AttentionID || answered.Status != "resolved" {
		t.Fatal(answered)
	}
	var current protocol.AttentionView
	if status := e.do("GET", "/api/v1/attentions/"+a.AttentionID, token, nil, &current); status != http.StatusOK || current.Decision.Status != "answered" || !strings.Contains(string(current.Decision.Answer), "report") {
		t.Fatal(status, current)
	}
	// Original control values never refresh on request recovery, and stale returns the same object.
	if code := cli.Main(t.Context(), env, []string{"attention", "respond", a.AttentionID, "--request-id", e.ids.New(protocol.PrefixRequest), "--data", `{"version":1,"option":"report"}`}); code != 2 {
		t.Fatal("retry admitted without original revision", code)
	}
	status, stale := e.op(token, e.envelope("attentions.present", protocol.Target{Type: "attention", ID: a.AttentionID}, &a.Revision, protocol.PresentAttentionPayload{}))
	if status != http.StatusOK || stale.CurrentAttention == nil || stale.CurrentAttention.Revision != answered.Revision {
		t.Fatal(status, stale)
	}
}

func TestPlannerDeadlineCLIRealHTTPAndGeneratedViews(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	issue := createOwnerIssueHTTP(t, e, token, "")
	dir := t.TempDir()
	deployment, err := e.app.DeploymentID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]any{"config.json": map[string]string{"server": e.url}, "credentials.json": map[string]string{"client_id": "planner-cli", "owner_token": token, "deployment_id": deployment}} {
		raw, _ := json.Marshal(data)
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	var stdout, stderr bytes.Buffer
	env := &cli.Env{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr, ConfigDir: dir, HTTP: http.DefaultClient, IDs: e.ids}
	run := func(args ...string) protocol.APIResponse {
		t.Helper()
		stdout.Reset()
		stderr.Reset()
		if code := cli.Main(t.Context(), env, args); code != 0 {
			t.Fatal(code, stdout.String(), stderr.String())
		}
		var res protocol.APIResponse
		if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		return res
	}
	p := protocol.PutIssueDeadlinePayload{IssueID: issue.IssueID, Title: "Actual external target", Nature: "external", Strength: "soft", Timezone: "UTC", Reason: "Owner records unknown time honestly"}
	raw, _ := json.Marshal(p)
	res := run("issue", "deadline", "put", "--data", string(raw))
	var d protocol.IssueDeadline
	if err := json.Unmarshal(res.Operation.Result, &d); err != nil {
		t.Fatal(err)
	}
	if d.DueAt != nil || d.EstimateSeconds != nil || d.NearRuleID != "" {
		t.Fatal("unknown date invented a reminder", d)
	}
	raw, _ = json.Marshal(protocol.PutPlannerEntryPayload{Entry: protocol.PlannerEntry{Kind: "user_action", IssueID: issue.IssueID, Title: "Clarify the external date", Responsible: "owner", Status: "open", Rank: 10, Reason: "The external target lacks a time"}})
	res = run("next-action", "put", "--data", string(raw))
	if res.Operation.Status != protocol.OpSucceeded {
		t.Fatal(res)
	}
	var next protocol.NextActionPage
	if status := e.do("GET", "/api/v1/next-actions?issue_id="+issue.IssueID, token, nil, &next); status != http.StatusOK || len(next.Items) != 1 || len(next.Deadlines) != 1 || next.Deadlines[0].DeadlineID != d.DeadlineID {
		t.Fatal(status, next)
	}
	detail := issueDetailHTTP(t, e, token, issue.IssueID)
	if len(detail.Deadlines) != 1 || detail.Attentions == nil {
		t.Fatal("detail lost shared identities", detail)
	}
	run("issue", "deadline", "cancel", d.DeadlineID, "--revision", "1", "--reason", "Owner withdraws the target")
}

func TestP5ReviewPlannerDetailMatchesLinkedAttention(t *testing.T) {
	for _, action := range []string{"confirm", "withdraw"} {
		t.Run(action, func(t *testing.T) {
			e := newEnv(t)
			e.configure()
			token := e.login(protocol.ScopeOwner)
			issue := createOwnerIssueHTTP(t, e, token, "")
			_, res := e.op(token, e.envelope("attentions.create", protocol.Target{Type: "attention"}, nil, protocol.CreateAttentionPayload{Kind: "action", IssueID: issue.IssueID, Title: "Owner review"}))
			var a protocol.Attention
			if err := json.Unmarshal(res.Operation.Result, &a); err != nil {
				t.Fatal(err)
			}
			payload := protocol.PutPlannerEntryPayload{Entry: protocol.PlannerEntry{Kind: "user_action", IssueID: issue.IssueID, AttentionID: a.AttentionID, Title: a.Title, Responsible: "owner", Status: "open", Reason: "Track original action"}}
			_, res = e.op(token, e.envelope("planner.entries_put", protocol.Target{Type: "planner_entry"}, nil, payload))
			var entry protocol.PlannerEntry
			if err := json.Unmarshal(res.Operation.Result, &entry); err != nil {
				t.Fatal(err)
			}
			var body any = protocol.ConfirmAttentionPayload{Summary: "Actual owner completed it"}
			expected := "done"
			if action == "withdraw" {
				body = struct{}{}
				expected = "cancelled"
			}
			env := e.envelope("attentions."+action, protocol.Target{Type: "attention", ID: a.AttentionID}, &a.Revision, body)
			env.Reason = "Owner withdraws this obligation"
			_, res = e.op(token, env)
			if res.Operation.Status != protocol.OpSucceeded {
				t.Fatal(res)
			}
			var page protocol.NextActionPage
			var detail protocol.PlannerEntry
			if status := e.do("GET", "/api/v1/next-actions?issue_id="+issue.IssueID, token, nil, &page); status != 200 {
				t.Fatal(status, page)
			}
			if status := e.do("GET", "/api/v1/planner-entries/"+entry.EntryID, token, nil, &detail); status != 200 {
				t.Fatal(status, detail)
			}
			if len(page.Items) != 1 || detail.EntryID != entry.EntryID || detail.Status != expected || detail.Status != page.Items[0].Status {
				t.Fatal("list/detail disagree", page, detail)
			}
		})
	}
}

func TestP5ReviewAttentionCLISecondPage(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	for i := 0; i < 51; i++ {
		_, res := e.op(token, e.envelope("attentions.create", protocol.Target{Type: "attention"}, nil, protocol.CreateAttentionPayload{Kind: "information", Title: "Read all obligations"}))
		if res.Operation.Status != protocol.OpSucceeded {
			t.Fatal(res)
		}
	}
	dir := t.TempDir()
	deployment, err := e.app.DeploymentID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]any{"config.json": map[string]string{"server": e.url}, "credentials.json": map[string]string{"client_id": "page-cli", "owner_token": token, "deployment_id": deployment}} {
		raw, _ := json.Marshal(data)
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	var stdout, stderr bytes.Buffer
	env := &cli.Env{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr, ConfigDir: dir, HTTP: http.DefaultClient, IDs: e.ids}
	run := func(args ...string) protocol.AttentionPage {
		t.Helper()
		stdout.Reset()
		stderr.Reset()
		if code := cli.Main(t.Context(), env, append([]string{"attention", "list"}, args...)); code != 0 {
			t.Fatal(code, stderr.String())
		}
		var page protocol.AttentionPage
		if err := json.Unmarshal(stdout.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	first := run()
	if len(first.Items) != 50 || !first.More || first.Next == "" {
		t.Fatal(first)
	}
	second := run("--after", first.Next)
	if len(second.Items) != 1 || second.More || second.Items[0].AttentionID <= first.Next {
		t.Fatal("later obligation unreachable", second)
	}
}
