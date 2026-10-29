package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
)

func createOwnerIssueHTTP(t *testing.T, e *env, token, parent string) protocol.Issue {
	t.Helper()
	payload := protocol.CreateIssuePayload{Plan: protocol.IssuePlan{Title: "Owner intent", Project: "default", Intent: "Investigate a vague goal", Stages: []protocol.IssueStage{}}, ParentIssueID: parent}
	return issueHTTP(t, e, token, e.envelope("issues.create", protocol.Target{Type: "issue"}, nil, payload))
}
func issueHTTP(t *testing.T, e *env, token string, envelope protocol.Envelope) protocol.Issue {
	t.Helper()
	status, response := e.op(token, envelope)
	if status != http.StatusOK || response.Operation == nil || response.Operation.Status != protocol.OpSucceeded {
		t.Fatalf("%s HTTP%d %+v", envelope.Operation, status, response)
	}
	var issue protocol.Issue
	if err := json.Unmarshal(response.Operation.Result, &issue); err != nil {
		t.Fatal(err)
	}
	return issue
}
func issueDetailHTTP(t *testing.T, e *env, token, id string) protocol.IssueView {
	t.Helper()
	var view protocol.IssueView
	if status := e.do("GET", "/api/v1/issues/"+id, token, nil, &view); status != http.StatusOK {
		t.Fatal(status)
	}
	return view
}

func TestIssueOwnerHTTPRecursiveIdentityHistoryAndReplay(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	// Legacy setup is intentionally not adopted by the new issue list/read API.
	legacy := e.task("Existing legacy task")
	_ = legacy
	parent := createOwnerIssueHTTP(t, e, token, "")
	child := createOwnerIssueHTTP(t, e, token, parent.IssueID)
	var roots protocol.IssuePage
	if status := e.do("GET", "/api/v1/issues?parent_id=", token, nil, &roots); status != http.StatusOK || len(roots.Items) != 1 || roots.Items[0].IssueID != parent.IssueID {
		t.Fatalf("roots=%d %+v", status, roots)
	}
	var children protocol.IssuePage
	if status := e.do("GET", "/api/v1/issues?parent_id="+parent.IssueID, token, nil, &children); status != http.StatusOK || len(children.Items) != 1 || children.Items[0].IssueID != child.IssueID {
		t.Fatalf("children=%d %+v", status, children)
	}
	if status := e.do("GET", "/api/v1/issues/"+child.IssueID, "", nil, nil); status != http.StatusUnauthorized {
		t.Fatal(status)
	}
	plan := child.Plan
	plan.Title = "Owner clarified intent"
	update := e.envelope("issues.update", protocol.Target{Type: "issue", ID: child.IssueID}, &child.Revision, protocol.UpdateIssuePayload{Plan: plan, ExpectedAssignmentEpoch: child.AssignmentEpoch})
	current := issueHTTP(t, e, token, update)
	status, stale := e.op(token, e.envelope(update.Operation, update.Target, &child.Revision, protocol.UpdateIssuePayload{Plan: plan, ExpectedAssignmentEpoch: child.AssignmentEpoch}))
	if status != http.StatusOK || stale.Operation == nil || stale.Operation.Error == nil || stale.Operation.Error.Code != protocol.CodeStaleRevision || stale.CurrentIssue == nil || stale.CurrentIssue.Revision != current.Revision {
		t.Fatalf("canonical stale projection=%d %+v", status, stale)
	}
	later := protocol.IssueControlPayload{ExpectedAssignmentEpoch: current.AssignmentEpoch}
	current = issueHTTP(t, e, token, e.envelope("issues.pause", update.Target, &current.Revision, later))
	replay := issueHTTP(t, e, token, update)
	if replay.Revision != child.Revision+1 || replay.Plan.Title != plan.Title {
		t.Fatal("same request returned later aggregate", replay)
	}
	before := e.count(`SELECT count(*) FROM request_records`)
	var historical protocol.IssueView
	if status := e.do("GET", "/api/v1/issues/"+child.IssueID+"?revision=1", token, nil, &historical); status != http.StatusOK || !historical.Historical || historical.Plan.Title != child.Plan.Title || len(historical.Tasks) != 0 || historical.ContextRevision != 0 {
		t.Fatalf("history=%d %+v", status, historical)
	}
	if e.count(`SELECT count(*) FROM request_records`) != before {
		t.Fatal("history read wrote a request")
	}
	// P4 publishes assignment. Incomplete requests reach domain validation;
	// retaining the actual authenticated owner is an idempotent responsibility
	// choice, not a new epoch or an implicit resume of this paused Issue.
	status, invalidAssignment := e.op(token, e.envelope("issues.assign", update.Target, &current.Revision, map[string]any{"expected_assignment_epoch": current.AssignmentEpoch}))
	if status != http.StatusOK || invalidAssignment.Operation == nil || invalidAssignment.Operation.Status != protocol.OpFailed || invalidAssignment.Operation.Error == nil || invalidAssignment.Operation.Error.Code != protocol.CodeInvalidRequest {
		t.Fatalf("published assignment domain rejection=%d %+v", status, invalidAssignment)
	}
	payload := protocol.IssueAssignmentPayload{ExpectedAssignmentEpoch: current.AssignmentEpoch, Assignee: current.Assignee, Reason: "Retain concrete responsibility with the authenticated owner"}
	assigned := issueHTTP(t, e, token, e.envelope("issues.assign", update.Target, &current.Revision, payload))
	if assigned.Revision != current.Revision || assigned.AssignmentEpoch != current.AssignmentEpoch || assigned.Assignee != current.Assignee || assigned.State != current.State {
		t.Fatalf("same-owner assignment changed responsibility or lifecycle: %+v", assigned)
	}
	var raw map[string]any
	e.do("GET", "/api/v1/issues/"+child.IssueID, token, nil, &raw)
	if _, found := raw["work_item_id"]; found {
		t.Fatal("new canonical detail leaked a legacy facade identity")
	}
}

func issueWorkerCommand(t *testing.T, e *env, p protocol.Principal, target protocol.Target, body any, command app.Command) protocol.Operation {
	t.Helper()
	env := e.envelope(command.Operation(), target, nil, body)
	out, err := e.app.Execute(t.Context(), p, &env, command)
	if err != nil || out.Status != protocol.OpSucceeded {
		t.Fatalf("%s %+v %v", command.Operation(), out, err)
	}
	return out
}
func issueClaimWorker(t *testing.T, e *env) protocol.Assignment {
	t.Helper()
	out := issueWorkerCommand(t, e, protocol.Principal{Kind: protocol.PrincipalRunner, Subject: "host-a"}, protocol.Target{Type: "host", ID: "host-a"}, protocol.ClaimPayload{WorkspaceProtocol: 1, Worker: protocol.WorkerAppServer}, app.Claim{})
	var result struct {
		Assignment *protocol.Assignment `json:"assignment"`
	}
	if err := json.Unmarshal(out.Result, &result); err != nil || result.Assignment == nil {
		t.Fatalf("claim result %s %v", out.Result, err)
	}
	return *result.Assignment
}
func issueArtifact(run protocol.Assignment) protocol.ArtifactPayload {
	return protocol.ArtifactPayload{Mounts: []protocol.ArtifactMount{{Repo: "tend", Path: "tend", BaseSHA: strings.Repeat("1", 40), CommitSHA: strings.Repeat("2", 40), Branch: "tend/" + run.Workspace.Name, Changed: true}}}
}

func TestIssueOwnerHTTPNoStageWorkerAcceptanceReviewAndContractRecovery(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	issue := createOwnerIssueHTTP(t, e, token, "")
	spec := protocol.OwnerTaskSpec{Goal: "Investigate and fix the build", Acceptance: []string{"Build passes"}, Delivery: "commit", ImplementationContext: "PRIVATE_IMPLEMENTATION_PLAN", Workspace: protocol.OwnerTaskWorkspace{Mounts: []protocol.OwnerTaskMount{{Repo: "tend"}}}}
	// Copying a stored spec must not turn server-frozen context into owner input.
	wrongSpec, _ := json.Marshal(spec)
	var fields map[string]any
	_ = json.Unmarshal(wrongSpec, &fields)
	fields["shared_context"] = []any{map[string]any{"work_item_id": issue.IssueID, "revision": 1}}
	rejected := e.envelope("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, &issue.Revision, map[string]any{"expected_assignment_epoch": issue.AssignmentEpoch, "title": "Copied stored spec", "spec": fields})
	status, rejectedResponse := e.op(token, rejected)
	if status != http.StatusOK || rejectedResponse.Operation == nil || rejectedResponse.Operation.Error == nil || rejectedResponse.Operation.Error.Code != protocol.CodeInvalidRequest || e.count(`SELECT count(*) FROM tasks`) != 0 {
		t.Fatalf("stored spec input was accepted: %d %+v", status, rejectedResponse)
	}
	create := e.envelope("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, &issue.Revision, protocol.CreateIssueTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Owner worker task", Spec: spec, Rating: protocol.RatingP1})
	status, created := e.op(token, create)
	if status != http.StatusOK || created.Operation == nil || created.Operation.Status != protocol.OpSucceeded {
		t.Fatalf("create task %d %+v", status, created)
	}
	var task struct {
		TaskID string `json:"task_id"`
	}
	_ = json.Unmarshal(created.Operation.Result, &task)
	view := issueDetailHTTP(t, e, token, issue.IssueID)
	if len(view.Plan.Stages) != 0 || len(view.Tasks) != 1 || view.Tasks[0].TaskID != task.TaskID {
		t.Fatalf("primary=%+v", view)
	}
	// Explicit owner contract refresh is available, rather than leaving a stale
	// agreement with no public recovery path. It shares UpdateIntent's rules.
	spec.Goal = "Clarified owner investigation"
	staleUpdate := e.envelope("issues.update_task", protocol.Target{Type: "task", ID: task.TaskID}, &view.Tasks[0].ControlRevision, protocol.UpdateIssueTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch + 1, Spec: &spec})
	status, staleContract := e.op(token, staleUpdate)
	if status != http.StatusOK || staleContract.Operation == nil || staleContract.Operation.Error == nil || staleContract.Operation.Error.Code != protocol.CodeStaleRevision || staleContract.Current == nil || staleContract.Current.SpecVersion != 1 {
		t.Fatalf("assignment epoch silently changed task: %d %+v", status, staleContract)
	}
	updated := e.envelope("issues.update_task", protocol.Target{Type: "task", ID: task.TaskID}, &view.Tasks[0].ControlRevision, protocol.UpdateIssueTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Spec: &spec})
	status, res := e.op(token, updated)
	if status != http.StatusOK || res.Operation == nil || res.Operation.Status != protocol.OpSucceeded {
		t.Fatalf("update contract %d %+v", status, res)
	}
	implement := issueClaimWorker(t, e)
	if implement.TaskID != task.TaskID || implement.SpecVersion != 2 || !strings.Contains(string(implement.Spec), "PRIVATE_IMPLEMENTATION_PLAN") {
		t.Fatal(implement)
	}
	principal := protocol.Principal{Kind: protocol.PrincipalRun, Subject: implement.RunID, Role: implement.Role}
	target := protocol.Target{Type: "run", ID: implement.RunID}
	issueWorkerCommand(t, e, principal, target, nil, app.ReportRunStarted{})
	issueWorkerCommand(t, e, principal, target, issueArtifact(implement), app.SubmitArtifact{})
	accept := issueClaimWorker(t, e)
	if accept.Role != protocol.RoleAccept || accept.RunID == implement.RunID || accept.SpecVersion != 2 || strings.Contains(string(accept.Spec), "PRIVATE_IMPLEMENTATION_PLAN") {
		t.Fatal("independent acceptance", accept)
	}
	issueWorkerCommand(t, e, protocol.Principal{Kind: protocol.PrincipalRun, Subject: accept.RunID, Role: accept.Role}, protocol.Target{Type: "run", ID: accept.RunID}, protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, app.SubmitAcceptance{})
	issueWorkerCommand(t, e, protocol.Principal{Kind: protocol.PrincipalRunner, Subject: "host-a"}, protocol.Target{Type: "workspace", ID: accept.Workspace.ID}, protocol.WorkspaceRelease{WorkspaceID: accept.Workspace.ID, TaskID: task.TaskID}, app.ReleaseWorkspace{})
	view = issueDetailHTTP(t, e, token, issue.IssueID)
	if len(view.PendingReviews) != 1 || view.Tasks[0].Lifecycle != protocol.TaskCompleted {
		t.Fatalf("review=%+v", view)
	}
	review := e.envelope("issues.review_completions", protocol.Target{Type: "issue", ID: issue.IssueID}, &view.Revision, protocol.ReviewIssueCompletionsPayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: view.AssignmentEpoch}, Reviews: []protocol.CompletionReviewAcknowledgement{{EventID: view.PendingReviews[0].EventID, Reason: "Owner checked accepted delivery"}}})
	issue = issueHTTP(t, e, token, review)
	if issue.Revision != view.Revision+1 {
		t.Fatal("completion review lost revision")
	}
	closed := issueHTTP(t, e, token, e.envelope("issues.close", review.Target, &issue.Revision, protocol.IssueEndpointPayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch}, Closure: &protocol.IssueClosure{Kind: protocol.WorkItemClosureCompleted, Reason: "Scope achieved", Evidence: []protocol.IssueEvidence{{Kind: protocol.WorkItemEvidenceOwnerDecision, Reference: "current_request", Summary: "Owner confirms accepted result and scope"}}}}))
	if closed.State != protocol.IssueClosed {
		t.Fatal(closed)
	}
	taskView := issueDetailHTTP(t, e, token, issue.IssueID).Tasks[0]
	status, res = e.op(token, e.envelope("issues.update_task", updated.Target, &taskView.ControlRevision, protocol.UpdateIssueTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Spec: &spec}))
	if status != http.StatusOK || res.Operation == nil || res.Operation.Error == nil || res.Operation.Error.Code != protocol.CodeInvalidTransition {
		t.Fatal("closed issue silently reactivated task", res)
	}
}

func TestIssueOwnerHTTPSourceShareReadAndTerminalSettlement(t *testing.T) {
	e := newEnv(t)
	e.configure()
	e.server.cfg.HostedMainAgent = true
	token := e.login(protocol.ScopeOwner)
	issue := createOwnerIssueHTTP(t, e, token, "")
	status, submitted := e.op(token, e.envelope("conversation.submit_message", protocol.Target{Type: "conversation"}, nil, protocol.SubmitMessagePayload{Body: "A vague source requiring owner review"}))
	if status != http.StatusOK || submitted.Operation == nil || submitted.Operation.Status != protocol.OpAccepted {
		t.Fatalf("source %d %+v op=%+v", status, submitted, submitted.Operation)
	}
	var source struct {
		MessageID string `json:"message_id"`
	}
	_ = json.Unmarshal(submitted.Operation.Result, &source)
	revision := int64(0)
	status, res := e.op(token, e.envelope("conversation.set_issue_associations", protocol.Target{Type: "message", ID: source.MessageID}, &revision, protocol.SetIssueMessageAssociationsPayload{Disposition: "linked", ContentRevision: 1, IssueIDs: []string{issue.IssueID}}))
	if status != http.StatusOK || res.Operation == nil || res.Operation.Status != protocol.OpSucceeded {
		t.Fatalf("source association %d %+v", status, res)
	}
	view := issueDetailHTTP(t, e, token, issue.IssueID)
	if len(view.SourceReviews) != 1 || view.SourceReviews[0].Author != "owner" || view.ContextRevision < 1 {
		t.Fatal(view)
	}
	issue = issueHTTP(t, e, token, e.envelope("issues.cancel", protocol.Target{Type: "issue", ID: issue.IssueID}, &issue.Revision, protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch}))
	pending := view.SourceReviews[0]
	view = issueDetailHTTP(t, e, token, issue.IssueID)
	issue = issueHTTP(t, e, token, e.envelope("issues.finish_cancel", protocol.Target{Type: "issue", ID: issue.IssueID}, &issue.Revision, protocol.IssueEndpointPayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Owner ends this scope"}, ExpectedContextRevision: &view.ContextRevision, SourceDispositions: []protocol.IssueSourceDisposition{{MessageID: pending.MessageID, ContentRevision: pending.ContentRevision, AssociationRevision: pending.AssociationRevision, Reason: "Owner reviewed source and ends scope"}}}))
	if issue.State != protocol.IssueCancelled || len(issueDetailHTTP(t, e, token, issue.IssueID).SourceReviews) != 0 {
		t.Fatal(issue)
	}
}
