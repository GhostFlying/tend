package app

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestManagerPublicationAndClaimFreezeResponsibilityWithoutWorkspace(t *testing.T) {
	h, r := managerClaimHarness(t)
	var op protocol.Operation
	var err error
	i := createCanonicalOwnerTestIssue(t, h)
	request := h.env(protocol.OpIssueRequestManager, protocol.Target{Type: "issue", ID: i.IssueID}, protocol.RequestIssueManagerPayload{ExpectedAssignmentEpoch: i.AssignmentEpoch, Reason: "Delegate the continuing responsibility"}, rev(i.Revision))
	op = h.exec(owner, request, RequestIssueManager{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var manager store.IssueManagerTask
	if err = json.Unmarshal(op.Result, &manager); err != nil {
		t.Fatal(err)
	}
	if manager.HostID != "" || manager.Pin != nil || manager.AssignmentEpoch != 0 {
		t.Fatal("publication selected an executor", manager)
	}
	if h.count("workspaces") != 0 {
		t.Fatal("manager created a code workspace")
	}
	claim := h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{Runtime: r, WorkspaceProtocol: 1}, nil)
	op = h.exec(runner("linux-1"), claim, Claim{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var result claimResult
	if err = json.Unmarshal(op.Result, &result); err != nil {
		t.Fatal(err)
	}
	as := result.Assignment
	var wire struct {
		Assignment map[string]json.RawMessage `json:"assignment"`
	}
	if err := json.Unmarshal(op.Result, &wire); err != nil {
		t.Fatal(err)
	}
	if _, exists := wire.Assignment["workspace"]; exists {
		t.Fatal("manager wire contract contains a code workspace", string(op.Result))
	}
	if as.Manager == nil || as.Role != protocol.RoleIssueManager || as.Execution == nil || as.TaskID != manager.TaskID || as.Workspace.ID != "" {
		t.Fatalf("assignment=%+v receipt=%s", as, op.Result)
	}
	if err = h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		current, err := store.GetCanonicalIssue(t.Context(), tx.Q(), i.IssueID)
		if err != nil {
			return err
		}
		if current.Assignee.Ref != "linux-1/alpha" || current.AssignmentEpoch != i.AssignmentEpoch+1 || current.Revision != i.Revision+1 {
			t.Fatal("claim did not atomically update assignee", current)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if h.count("workspaces") != 0 || h.count("workspace_runs") != 0 || h.count("acceptances") != 0 {
		t.Fatal("manager acquired execution artifacts")
	}
	before := h.count("runs")
	replay := h.exec(runner("linux-1"), claim, Claim{})
	if string(replay.Result) != string(op.Result) || h.count("runs") != before {
		t.Fatal("replayed claim allocated another execution", replay)
	}
}

func managerClaimHarness(t *testing.T) (*harness, *protocol.RunnerRuntime) {
	t.Helper()
	h, r, p, _ := inventorySetup(t)
	inventoryCategory(t, h, protocol.DefaultIssueManagerCategory, 0, "active")
	p.FormatVersion = 2
	p.Runtimes[0].DeclaredPurposes = []string{"worker_run", "issue_manager"}
	p.Runtimes[0].CLI = "codex"
	p.Agents[0].Purposes = []string{"issue_manager"}
	p.Agents[0].Categories = []string{protocol.DefaultIssueManagerCategory}
	_, hash, err := protocol.CanonicalAgentInventory(p)
	if err != nil {
		t.Fatal(err)
	}
	op := h.exec(runner("linux-1"), inventoryRequest(h, r, p, hash, 1, 0, "manager-snapshot"), RegisterAgentInventory{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	r.Capabilities = append(r.Capabilities, protocol.CapabilityAgentExecution, protocol.CapabilityIssueManager)
	r.JournalVersion = 13
	r.AgentExecutors = []protocol.AgentExecutor{{Available: true, Pin: protocol.RunExecutionPin{Purpose: "issue_manager", Identity: p.Agents[0].Identity, AgentRevision: 1, Runtime: p.Agents[0].Binding, InventoryRevision: 1, SnapshotID: "manager-snapshot", PublicSHA256: hash, Profile: protocol.ExecutionProfile{Model: p.Agents[0].RequestedModel}, Worker: protocol.WorkerAppServer}}}
	r.Sequence++
	if op = h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}

	return h, r
}

func TestManagerHandoffWaitsForOldProcessAndRetainsConversation(t *testing.T) {
	h, r := managerClaimHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	publish := func(replace bool) store.IssueManagerTask {
		t.Helper()
		var current protocol.Issue
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			var err error
			current, err = store.GetCanonicalIssue(t.Context(), tx.Q(), issue.IssueID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		op := h.exec(owner, h.env(protocol.OpIssueRequestManager, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.RequestIssueManagerPayload{ExpectedAssignmentEpoch: current.AssignmentEpoch, Replace: replace, Reason: "Explicit responsibility transfer"}, rev(current.Revision)), RequestIssueManager{})
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		var m store.IssueManagerTask
		if err := json.Unmarshal(op.Result, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	first := publish(false)
	claim := func() *protocol.Assignment {
		op := h.exec(runner("linux-1"), h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{Runtime: r, WorkspaceProtocol: 1}, nil), Claim{})
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		var res struct {
			Assignment *protocol.Assignment `json:"assignment"`
		}
		_ = json.Unmarshal(op.Result, &res)
		return res.Assignment
	}
	old := claim()
	if old == nil {
		t.Fatal("no first manager")
	}
	next := publish(true)
	if next.TaskID == first.TaskID || next.HandoffID == "" {
		t.Fatal("replacement did not record a new fenced responsibility", next)
	}
	if got := claim(); got != nil {
		t.Fatal("replacement started before old process exit", got)
	}
	stop := h.runAs(*old, protocol.OpManagerNativeStopped, protocol.ManagerStoppedPayload{Reason: "Confirmed local process exit", LastSequence: 0}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeStopped})
	if stop.Status != protocol.OpSucceeded {
		t.Fatal(stop)
	}
	successor := claim()
	if successor == nil || successor.TaskID != next.TaskID || successor.Manager.ConversationID != old.Manager.ConversationID {
		t.Fatal("handoff did not preserve issue conversation", successor)
	}
	if h.task(first.TaskID).Lifecycle != protocol.TaskCompleted {
		t.Fatal("old responsibility did not finish after real stop")
	}
}

func TestManagerNativeReceiptsAreOrderedAndContentBound(t *testing.T) {
	h, r := managerClaimHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	h.exec(owner, h.env(protocol.OpIssueRequestManager, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.RequestIssueManagerPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Delegate"}, rev(issue.Revision)), RequestIssueManager{})
	op := h.exec(runner("linux-1"), h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{Runtime: r, WorkspaceProtocol: 1}, nil), Claim{})
	var res struct {
		Assignment *protocol.Assignment `json:"assignment"`
	}
	_ = json.Unmarshal(op.Result, &res)
	as := res.Assignment
	if as == nil {
		t.Fatal(op)
	}
	event := func(seq int64, body string) protocol.Operation {
		env := h.env(protocol.OpManagerNativeEvent, runTarget(as.RunID), protocol.ManagerNativeEvent{Sequence: seq, Body: json.RawMessage(body)}, nil)
		result, err := h.app.Execute(t.Context(), runPrincipal(as.RunID, as.Role), env, ManagerNativeCommand{OperationName: protocol.OpManagerNativeEvent})
		if err != nil {
			return protocol.Operation{Status: protocol.OpFailed, Error: protocol.Errorf(protocol.CodeOf(err), "receipt rejected")}
		}
		return result
	}
	if got := event(2, `{}`); got.Status != protocol.OpFailed {
		t.Fatal("accepted event gap", got)
	}
	if got := event(1, `{"thread_id":"native"}`); got.Status != protocol.OpSucceeded {
		t.Fatal(got)
	}
	if got := event(1, `{"thread_id":"other"}`); got.Status != protocol.OpFailed || got.Error.Code != protocol.CodeConflictPayload {
		t.Fatal("accepted conflicting event", got)
	}
	env := h.env(protocol.OpManagerNativeStopped, runTarget(as.RunID), protocol.ManagerStoppedPayload{Reason: "Exit", LastSequence: 0}, nil)
	_, err := h.app.Execute(t.Context(), runPrincipal(as.RunID, as.Role), env, ManagerNativeCommand{OperationName: protocol.OpManagerNativeStopped})
	if protocol.CodeOf(err) != protocol.CodeUnknownOutcome {
		t.Fatal("stop overtook events", err)
	}
	if got := event(2, `{"thread_id":"native"}`); got.Status != protocol.OpSucceeded {
		t.Fatal(got)
	}
	if err := h.app.AcknowledgeManagerEvent(t.Context(), as.RunID, 2, "native", "", "", false); err != nil {
		t.Fatal(err)
	}
	// A replacement proxy must still replay event 1 even though event 2 was
	// already applied. The maximum uploaded sequence is not an applied cursor.
	if cursor, err := h.app.ManagerAppliedCursor(t.Context(), as.RunID); err != nil || cursor != 0 {
		t.Fatal("unapplied event was skipped", cursor, err)
	}
	closeID, err := h.app.QueueManagerNative(t.Context(), as.RunID, "close", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	h.runAs(*as, protocol.OpManagerNativeClaim, map[string]any{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim})
	closeReply := protocol.ManagerNativeReply{ID: closeID, LastSequence: 2, Result: json.RawMessage(`null`)}
	_, err = h.app.Execute(t.Context(), runPrincipal(as.RunID, as.Role), h.env(protocol.OpManagerNativeReply, runTarget(as.RunID), closeReply, nil), ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply})
	if protocol.CodeOf(err) != protocol.CodeUnknownOutcome {
		t.Fatal("close overtook unapplied output", err)
	}
	if err = h.app.AcknowledgeManagerEvent(t.Context(), as.RunID, 1, "native", "", "", false); err != nil {
		t.Fatal(err)
	}
	if cursor, err := h.app.ManagerAppliedCursor(t.Context(), as.RunID); err != nil || cursor != 2 {
		t.Fatal(cursor, err)
	}
	if op := h.runAs(*as, protocol.OpManagerNativeReply, closeReply, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		run, err := store.GetRun(t.Context(), tx.Q(), as.RunID)
		if err != nil {
			return err
		}
		lease, err := store.GetLeaseByRun(t.Context(), tx.Q(), as.RunID)
		if err == nil && (run.ObservedState != protocol.RunFinished || lease.Status != store.LeaseReleased) {
			t.Fatal("close receipt did not atomically release the run", run, lease)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestManagerTwoQualifiedRunnersRaceForOneResponsibility(t *testing.T) {
	h, one := managerClaimHarness(t)
	h.withHosts("linux-1", "linux-2")
	two := *one
	two.InstanceID = "second-manager-runner"
	two.AgentExecutors = append([]protocol.AgentExecutor(nil), one.AgentExecutors...)
	two.AgentExecutors[0].Pin.Identity.ExecutionEnvironmentID = "linux-2"
	projection, _ := inventoryProjection(t, "linux-2")
	projection.FormatVersion = 2
	projection.Runtimes[0].CLI = "codex"
	projection.Runtimes[0].DeclaredPurposes = []string{"worker_run", "issue_manager"}
	projection.Agents[0].Categories = []string{protocol.DefaultIssueManagerCategory}
	projection.Agents[0].Purposes = []string{"issue_manager"}
	_, hash, err := protocol.CanonicalAgentInventory(projection)
	if err != nil {
		t.Fatal(err)
	}
	two.AgentExecutors[0].Pin.PublicSHA256 = hash
	if op := h.versionedHello("linux-2", &two); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	registration := h.env(protocol.OpRunnerInventoryRegister, protocol.Target{Type: "host", ID: "linux-2"}, protocol.RegisterAgentInventoryPayload{Runtime: &two, LocalRevision: 1, SnapshotID: "manager-snapshot", PublicSHA256: hash, Projection: projection}, rev(0))
	if op := h.exec(runner("linux-2"), registration, RegisterAgentInventory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	issue := createCanonicalOwnerTestIssue(t, h)
	h.exec(owner, h.env(protocol.OpIssueRequestManager, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.RequestIssueManagerPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Publish one responsibility"}, rev(issue.Revision)), RequestIssueManager{})
	requests := []*protocol.Envelope{
		h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{Runtime: one, WorkspaceProtocol: 1}, nil),
		h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-2"}, protocol.ClaimPayload{Runtime: &two, WorkspaceProtocol: 1}, nil),
	}
	var wg sync.WaitGroup
	results := make([]protocol.Operation, 2)
	errors := make([]error, 2)
	start := make(chan struct{})
	for index := range requests {
		wg.Go(func() {
			<-start
			results[index], errors[index] = h.app.Execute(context.Background(), runner(requests[index].Target.ID), requests[index], Claim{})
		})
	}
	close(start)
	wg.Wait()
	winners := 0
	for index, result := range results {
		if errors[index] != nil || result.Status != protocol.OpSucceeded {
			t.Fatal(result, errors[index])
		}
		var claimed struct {
			Assignment *protocol.Assignment `json:"assignment"`
		}
		if err := json.Unmarshal(result.Result, &claimed); err != nil {
			t.Fatal(err)
		}
		if claimed.Assignment != nil {
			winners++
			replayed := h.exec(runner(requests[index].Target.ID), requests[index], Claim{})
			if string(replayed.Result) != string(result.Result) {
				t.Fatal("lost claim receipt changed the assignment")
			}
		}
	}
	if winners != 1 || h.count("runs") != 1 {
		t.Fatal("concurrent claims assigned multiple managers", winners, h.count("runs"))
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		current, err := store.GetCanonicalIssue(t.Context(), tx.Q(), issue.IssueID)
		if err == nil && current.AssignmentEpoch != issue.AssignmentEpoch+1 {
			t.Fatal("assignment epoch advanced more than once", current)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestManagerWithdrawnPublicationCannotClaimResponsibility(t *testing.T) {
	h, r := managerClaimHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	source := queueMessage(t, h, "Delegate this responsibility")
	published := h.exec(owner, h.env(protocol.OpIssueRequestManager, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.RequestIssueManagerPayload{SourceMessageID: source, ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Delegate by the exact source"}, rev(issue.Revision)), RequestIssueManager{})
	if published.Status != protocol.OpSucceeded {
		t.Fatal(published)
	}
	var manager store.IssueManagerTask
	if err := json.Unmarshal(published.Result, &manager); err != nil {
		t.Fatal(err)
	}
	withdrawOwnerSource(t, h, source)
	if h.task(manager.TaskID).Lifecycle != protocol.TaskCancelled {
		t.Fatal("withdrawn authorization left a runnable responsibility")
	}
	claimed := h.exec(runner("linux-1"), h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{Runtime: r, WorkspaceProtocol: 1}, nil), Claim{})
	var result struct {
		Assignment *protocol.Assignment `json:"assignment"`
	}
	if err := json.Unmarshal(claimed.Result, &result); err != nil || result.Assignment != nil || h.count("runs") != 0 {
		t.Fatal("withdrawn source acquired a manager", claimed, err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		current, err := store.GetCanonicalIssue(t.Context(), tx.Q(), issue.IssueID)
		if err == nil && (current.Assignee != issue.Assignee || current.AssignmentEpoch != issue.AssignmentEpoch) {
			t.Fatal("failed claim changed responsibility", current)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestManagerFileAccessUsesPersistedIssueAndRole(t *testing.T) {
	h, r := managerClaimHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	other := createCanonicalOwnerTestIssue(t, h)
	file := uploadChatFile(t, h, "evidence.bin", "application/octet-stream", []byte("original evidence\x00\n"))
	source := h.ids.New(protocol.PrefixMessage)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: source, Author: "owner", Body: "Evidence for a different issue", CreatedAt: h.clock.Now().UnixMilli()}); err != nil {
			return err
		}
		if err := tx.BindConversationAttachment(t.Context(), source, 0, file.FileID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO message_work_items(message_id,work_item_id,source,updated_at) VALUES(?,?,'owner',?)`, source, other.IssueID, h.clock.Now().UnixMilli())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	h.exec(owner, h.env(protocol.OpIssueRequestManager, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.RequestIssueManagerPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Delegate"}, rev(issue.Revision)), RequestIssueManager{})
	claimed := h.exec(runner("linux-1"), h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{Runtime: r, WorkspaceProtocol: 1}, nil), Claim{})
	var result claimResult
	if err := json.Unmarshal(claimed.Result, &result); err != nil {
		t.Fatal(err)
	}
	principal := runPrincipal(result.Assignment.RunID, protocol.RoleIssueManager)
	if _, err := h.app.ManagerFile(t.Context(), principal, file.FileID); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("cross-issue attachment admitted", err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO message_work_items(message_id,work_item_id,source,updated_at) VALUES(?,?,'owner',?)`, source, issue.IssueID, h.clock.Now().UnixMilli())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if metadata, err := h.app.ManagerFile(t.Context(), principal, file.FileID); err != nil || metadata.SHA256 != file.SHA256 {
		t.Fatal("explicit issue evidence unavailable", metadata, err)
	}
	principal.Role = protocol.RoleAccept
	if _, err := h.app.ManagerFile(t.Context(), principal, file.FileID); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("worker impersonated manager file access", err)
	}
}

func TestMainAgentCreatesCategoryManagerBeforeAnyRunnerIdentityIsSelected(t *testing.T) {
	h, r := managerClaimHarness(t)
	inventoryCategory(t, h, "planner", 0, "active")
	_, prepared := acceptHostedFixture(t, h, hostedConfigurationFixture(), 0)
	h.app = New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(prepared))
	source, delivery := h.say("Please coordinate this new intent")
	turn := startHostedReview(t, h, "main", delivery)
	create := protocol.CreateIssuePayload{IssueSourceReference: protocol.IssueSourceReference{SourceMessageID: source, ExpectedSourceRevision: rev(1)}, Plan: protocol.IssuePlan{Title: "Delegate an intent", Project: "default", Intent: "Coordinate through the runner"}, Manager: &protocol.RequestIssueManagerPayload{CategoryID: protocol.DefaultIssueManagerCategory, Reason: "Delegate ongoing responsibility"}}
	command := HostedCoordinationCommand{Command: CanonicalOwnerIssueCommand{Action: "create"}, ExecutionID: turn.RequestID, EffectSeq: 1}
	result := h.exec(mainAgent, h.env(command.Operation(), protocol.Target{Type: "issue"}, create, nil), command)
	if result.Status != protocol.OpSucceeded {
		t.Fatal(result)
	}
	var issue protocol.Issue
	if err := json.Unmarshal(result.Result, &issue); err != nil {
		t.Fatal(err)
	}
	if issue.Assignee.Kind != protocol.IssueAssigneeOwner || issue.Assignee.Ref != owner.Subject {
		t.Fatal("publication impersonated the owner or selected a runner early", issue)
	}
	var published store.IssueManagerTask
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		published, err = store.CurrentIssueManagerTask(t.Context(), tx.Q(), issue.IssueID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if published.Pin != nil || published.HostID != "" || published.SourceMessageID != source {
		t.Fatal("main publication did not retain source/category-only selection", published)
	}
	claimed := h.exec(runner("linux-1"), h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{Runtime: r, WorkspaceProtocol: 1}, nil), Claim{})
	var assignment claimResult
	if err := json.Unmarshal(claimed.Result, &assignment); err != nil || assignment.Assignment.Manager == nil || assignment.Assignment.TaskID != published.TaskID {
		t.Fatal("main's category task was not claimable", claimed, err)
	}
}
