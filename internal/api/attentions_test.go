package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/cli"
	"github.com/GhostFlying/tend/internal/netx"
	"github.com/GhostFlying/tend/internal/protocol"
)

func newAttentionCLI(t *testing.T, e *env, token string) (*cli.Env, *bytes.Buffer, func(...string) protocol.APIResponse) {
	t.Helper()
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
	return env, &stderr, run
}

func TestAttentionCLIRealHTTPOriginalDecisionAndInboxIdentity(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	issue := createOwnerIssueHTTP(t, e, token, "")
	env, stderr, run := newAttentionCLI(t, e, token)
	due := e.clock.Now().Add(30 * time.Hour).UnixMilli()
	payload, _ := json.Marshal(protocol.CreateAttentionPayload{IssueID: issue.IssueID, Kind: "decision", Title: "Which result is needed?", Options: []string{"report", "changes"}, BlocksClose: true, LatestActionAt: &due})
	created := run("create", "--data", string(payload))
	var a protocol.Attention
	if err := json.Unmarshal(created.Operation.Result, &a); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), created.Operation.RequestID) {
		t.Fatal("request identity was not visible")
	}
	if a.LatestActionAt == nil || *a.LatestActionAt != due || a.LatestActionDueRuleID == "" {
		t.Fatal("CLI discarded action time", a)
	}
	near, dueRule := a.LatestActionNearRuleID, a.LatestActionDueRuleID
	updated := run("update", a.AttentionID, "--revision", strconv.FormatInt(a.Revision, 10), "--data", `{"title":"Which result is needed?","blocks_close":true,"latest_action_timezone":"Asia/Shanghai"}`)
	if err := json.Unmarshal(updated.Operation.Result, &a); err != nil {
		t.Fatal(err)
	}
	if a.LatestActionAt == nil || *a.LatestActionAt != due || a.LatestActionNearRuleID != near || a.LatestActionDueRuleID != dueRule || a.QuestionVersion != 1 {
		t.Fatal("display change changed clock or question", a)
	}
	var inbox protocol.AttentionPage
	if status := e.do("GET", "/api/v1/inbox?issue_id="+issue.IssueID, token, nil, &inbox); status != http.StatusOK || len(inbox.Items) != 1 || inbox.Items[0].AttentionID != a.AttentionID || inbox.Items[0].Decision.DecisionID != a.DecisionID || inbox.Items[0].IssueTitle != issue.Plan.Title {
		t.Fatal(status, inbox)
	}
	presented := run("archive", a.AttentionID, "--revision", strconv.FormatInt(a.Revision, 10))
	if err := json.Unmarshal(presented.Operation.Result, &a); err != nil {
		t.Fatal(err)
	}
	run("create", "--data", `{"kind":"information","title":"An informational update"}`)
	if status := e.do("GET", "/api/v1/inbox?status=open&include_archived=true&actionable_only=true", token, nil, &inbox); status != http.StatusOK || len(inbox.Items) != 1 || inbox.Items[0].AttentionID != a.AttentionID {
		t.Fatal("actionable summary hid an archived obligation or counted a notification", status, inbox)
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

func TestAttentionContextTitlesRetainOriginalQuestionAndScope(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	issue := createOwnerIssueHTTP(t, e, token, "")
	payload := protocol.CreateIssueTaskPayload{
		ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Mobile repair execution",
		Spec:   protocol.OwnerTaskSpec{Goal: "Repair mobile layout", Acceptance: []string{"Mobile layout is readable"}, Delivery: "commit", Workspace: protocol.OwnerTaskWorkspace{Mounts: []protocol.OwnerTaskMount{{Repo: "tend"}}}},
		Rating: protocol.RatingP1,
	}
	status, response := e.op(token, e.envelope("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, &issue.Revision, payload))
	if status != http.StatusOK || response.Operation == nil || response.Operation.Status != protocol.OpSucceeded {
		t.Fatal(status, response)
	}
	var task struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(response.Operation.Result, &task); err != nil {
		t.Fatal(err)
	}
	for _, scoped := range []bool{true, false} {
		p := protocol.CreateAttentionPayload{Kind: "decision", Title: "How should this proceed?", Options: []string{"Continue"}}
		if scoped {
			p.IssueID, p.TaskID = issue.IssueID, task.TaskID
		}
		status, created := e.op(token, e.envelope("attentions.create", protocol.Target{Type: "attention"}, nil, p))
		if status != http.StatusOK || created.Operation == nil || created.Operation.Status != protocol.OpSucceeded {
			t.Fatal(status, created)
		}
		var original protocol.Attention
		if err := json.Unmarshal(created.Operation.Result, &original); err != nil {
			t.Fatal(err)
		}
		var detail protocol.AttentionView
		if status := e.do("GET", "/api/v1/attentions/"+original.AttentionID, token, nil, &detail); status != http.StatusOK {
			t.Fatal(status, detail)
		}
		if detail.Title != p.Title || detail.Decision.Question != p.Title || detail.DecisionID != original.DecisionID || detail.Revision != original.Revision {
			t.Fatal("context projection changed the original question", detail)
		}
		if scoped {
			if detail.IssueTitle != issue.Plan.Title || detail.IssueIntent != issue.Plan.Intent || detail.TaskTitle != payload.Title || detail.TaskContext == nil || detail.TaskContext.Goal != payload.Spec.Goal || detail.TaskContext.HasCurrentArtifact || detail.TaskContext.CurrentArtifactAccepted {
				t.Fatal("missing business context", detail)
			}
		} else if detail.IssueTitle != "" || detail.IssueIntent != "" || detail.TaskTitle != "" || detail.TaskContext != nil {
			t.Fatal("global attention inherited another item's context", detail)
		}
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
	// The opt-in owner list uses the real CLI and HTTP dispatch. The original
	// general list above retains its existing query and response.
	stdout.Reset()
	stderr.Reset()
	if code := cli.Main(t.Context(), env, []string{"next-action", "list", "--owner-only", "--issue", issue.IssueID}); code != 0 {
		t.Fatal(code, stdout.String(), stderr.String())
	}
	var personal protocol.NextActionPage
	if err := json.Unmarshal(stdout.Bytes(), &personal); err != nil || len(personal.Items) != 1 || personal.Items[0].EntryID != next.Items[0].EntryID {
		t.Fatal(err, personal)
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

func TestAttentionCLIDismissOrdinaryAction(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	issue := createOwnerIssueHTTP(t, e, token, "")
	_, _, run := newAttentionCLI(t, e, token)
	var action protocol.Attention
	created := run("create", "--data", `{"kind":"action","title":"Optional follow-up","action_class":"ordinary","issue_id":"`+issue.IssueID+`"}`)
	if err := json.Unmarshal(created.Operation.Result, &action); err != nil {
		t.Fatal(err)
	}
	var inbox protocol.AttentionPage
	if st := e.do("GET", "/api/v1/inbox?status=open", token, nil, &inbox); st != http.StatusOK || len(inbox.Items) != 1 || !inbox.Items[0].CanDismiss {
		t.Fatal(st, inbox)
	}
	entry := protocol.PlannerEntry{Kind: "user_action", IssueID: issue.IssueID, AttentionID: action.AttentionID, Title: action.Title, Responsible: "owner", Status: "open", Reason: "Track action"}
	_, plan := e.op(token, e.envelope("planner.entries_put", protocol.Target{Type: "planner_entry"}, nil, protocol.PutPlannerEntryPayload{Entry: entry}))
	if err := json.Unmarshal(plan.Operation.Result, &entry); err != nil {
		t.Fatal(err)
	}

	withdrawal := run("withdraw", action.AttentionID, "--not-needed")
	var withdrawn protocol.Attention
	if err := json.Unmarshal(withdrawal.Operation.Result, &withdrawn); err != nil {
		t.Fatal(err)
	}
	if withdrawn.AttentionID != action.AttentionID || withdrawn.Revision != action.Revision+1 ||
		withdrawn.Status != "withdrawn" || withdrawn.BlocksClose || withdrawn.WithdrawalMode != protocol.AttentionWithdrawalNotNeeded ||
		withdrawn.WithdrawnAt != e.clock.Now().UnixMilli() || withdrawn.RaisedBy != action.RaisedBy ||
		withdrawn.ResolutionActor == nil || withdrawn.ResolutionActor.Kind != protocol.PrincipalOwner || withdrawn.ResolutionActor.Subject != ownerLogin {
		t.Fatalf("unexpected withdrawal %+v", withdrawn)
	}
	if st := e.do("GET", "/api/v1/inbox?status=open", token, nil, &inbox); st != http.StatusOK || len(inbox.Items) != 0 {
		t.Fatal(st, inbox)
	}
	if st := e.do("GET", "/api/v1/inbox?status=withdrawn", token, nil, &inbox); st != http.StatusOK ||
		len(inbox.Items) != 1 || inbox.Items[0].CanDismiss || inbox.Items[0].Revision != withdrawn.Revision {
		t.Fatal(st, inbox)
	}
	if st := e.do("GET", "/api/v1/planner-entries/"+entry.EntryID, token, nil, &entry); st != http.StatusOK || entry.Status != "cancelled" {
		t.Fatal(st, entry)
	}
}

func TestAttentionDismissRejectsIneligibleAndOtherOwner(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	create := func(payload protocol.CreateAttentionPayload) protocol.Attention {
		t.Helper()
		_, res := e.op(token, e.envelope("attentions.create", protocol.Target{Type: "attention"}, nil, payload))
		var a protocol.Attention
		if err := json.Unmarshal(res.Operation.Result, &a); err != nil {
			t.Fatal(err)
		}
		return a
	}
	ordinary := create(protocol.CreateAttentionPayload{Kind: "action", Title: "Optional", ActionClass: protocol.AttentionActionOrdinary})
	check := func(token string, a protocol.Attention) {
		t.Helper()
		var view protocol.AttentionView
		path := "/api/v1/attentions/" + a.AttentionID
		if st := e.do("GET", path, token, nil, &view); st != http.StatusOK || view.CanDismiss {
			t.Fatal(st, view)
		}
		_, res := e.op(token, e.envelope("attentions.withdraw", protocol.Target{Type: "attention", ID: a.AttentionID}, &a.Revision,
			protocol.WithdrawAttentionPayload{Mode: protocol.AttentionWithdrawalNotNeeded}))
		if res.Operation.Status != protocol.OpFailed {
			t.Fatal(res)
		}
		if st := e.do("GET", path, token, nil, &view); st != http.StatusOK || view.Status != "open" || view.Revision != a.Revision {
			t.Fatal(st, view)
		}
	}
	check(token, create(protocol.CreateAttentionPayload{Kind: "action", Title: "Unclassified historical action"}))
	check(token, create(protocol.CreateAttentionPayload{Kind: "action", Title: "Real blocker", BlocksClose: true}))
	check(token, create(protocol.CreateAttentionPayload{Kind: "decision", Title: "Approve delivery?", Options: []string{"Approve"}, BlocksClose: true}))

	e.peer.set(netx.Identity{User: "other-owner@example.com"})
	var other protocol.SessionView
	if st := e.do("POST", "/api/v1/session", "", protocol.SessionRequest{ClientID: clientID, Scope: protocol.ScopeOwner}, &other); st != http.StatusOK {
		t.Fatal(st)
	}
	check(other.Token, ordinary)
}

func TestAttentionDismissAndConfirmHaveOneWinner(t *testing.T) {
	e := newEnv(t)
	token := e.login(protocol.ScopeOwner)
	_, created := e.op(token, e.envelope("attentions.create", protocol.Target{Type: "attention"}, nil,
		protocol.CreateAttentionPayload{Kind: "action", Title: "Optional follow-up", ActionClass: protocol.AttentionActionOrdinary}))
	var a protocol.Attention
	if err := json.Unmarshal(created.Operation.Result, &a); err != nil {
		t.Fatal(err)
	}
	target := protocol.Target{Type: "attention", ID: a.AttentionID}
	dismiss := e.envelope("attentions.withdraw", target, &a.Revision, protocol.WithdrawAttentionPayload{Mode: protocol.AttentionWithdrawalNotNeeded})
	confirm := e.envelope("attentions.confirm", target, &a.Revision, protocol.ConfirmAttentionPayload{Summary: "Completed"})
	var wg sync.WaitGroup
	var results [2]protocol.APIResponse
	for i, op := range []protocol.Envelope{dismiss, confirm} {
		wg.Go(func() {
			_, results[i] = e.op(token, op)
		})
	}
	wg.Wait()
	winner := results[0].Operation.Status == protocol.OpSucceeded
	if winner == (results[1].Operation.Status == protocol.OpSucceeded) {
		t.Fatal(results)
	}
	var detail protocol.AttentionView
	want := "resolved"
	if winner {
		want = "withdrawn"
	}
	path := "/api/v1/attentions/" + a.AttentionID
	if st := e.do("GET", path, token, nil, &detail); st != http.StatusOK || detail.Status != want || detail.Revision != a.Revision+1 {
		t.Fatal(st, detail)
	}
	dismiss.RequestID = e.ids.New(protocol.PrefixRequest)
	e.op(token, dismiss)
	if st := e.do("GET", path, token, nil, &detail); st != http.StatusOK || detail.Status != want || detail.Revision != a.Revision+1 {
		t.Fatal(st, detail)
	}
}

func TestAttentionCLIDismissTimeoutPrintsOriginalResponsibilityRevision(t *testing.T) {
	e := newEnv(t)
	token := e.login(protocol.ScopeOwner)
	e.configure()
	issue := createOwnerIssueHTTP(t, e, token, "")
	cliEnv, stderr, run := newAttentionCLI(t, e, token)
	var action protocol.Attention
	created := run("create", "--data", `{"kind":"action","title":"Optional follow-up","action_class":"ordinary","issue_id":"`+issue.IssueID+`"}`)
	if err := json.Unmarshal(created.Operation.Result, &action); err != nil || action.Responsibility == nil {
		t.Fatal(err, action)
	}

	var submitted protocol.Envelope
	cliEnv.HTTP = &http.Client{Transport: issueTransport(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodPost && req.URL.Path == "/api/v1/ops" {
			if err := json.NewDecoder(req.Body).Decode(&submitted); err != nil {
				return nil, err
			}
			return nil, context.DeadlineExceeded
		}
		return http.DefaultTransport.RoundTrip(req)
	})}
	stderr.Reset()
	if code := cli.Main(t.Context(), cliEnv, []string{"attention", "withdraw", action.AttentionID, "--not-needed"}); code == 0 {
		t.Fatal("expected transport timeout")
	}
	var payload protocol.WithdrawAttentionPayload
	if err := json.Unmarshal(submitted.Payload, &payload); err != nil || payload.ExpectedResponsibilityRevision == nil ||
		*payload.ExpectedResponsibilityRevision != action.Responsibility.Revision {
		t.Fatal(err, submitted)
	}
	for _, original := range []string{submitted.RequestID, "--not-needed", "--revision " + strconv.FormatInt(action.Revision, 10),
		"--responsibility-revision " + strconv.FormatInt(action.Responsibility.Revision, 10)} {
		if !strings.Contains(stderr.String(), original) {
			t.Fatalf("missing original input %q in %s", original, stderr)
		}
	}
}
