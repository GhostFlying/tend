package app

import (
	"encoding/json"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"sync"
	"testing"
)

func issueAdmissionStatus(t *testing.T, h *harness, i protocol.Issue, status protocol.IssueWorkStatus, choice *protocol.IssueOwnerRequest) protocol.Issue {
	t.Helper()
	op := h.exec(owner, h.env("issues.set_status", protocol.Target{Type: "issue", ID: i.IssueID}, protocol.SetIssueStatusPayload{ExpectedAssignmentEpoch: i.AssignmentEpoch, Status: status, Owner: choice}, rev(i.Revision)), CanonicalOwnerIssueCommand{Action: "set_status"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var next protocol.Issue
	if err := json.Unmarshal(op.Result, &next); err != nil {
		t.Fatal(err)
	}
	return next
}
func admissionClaim(t *testing.T, h *harness, r *protocol.RunnerRuntime) *protocol.Assignment {
	t.Helper()
	op := h.exec(runner("linux-1"), h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{Runtime: r, WorkspaceProtocol: 1}, nil), Claim{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var result struct {
		Assignment *protocol.Assignment `json:"assignment"`
	}
	if err := json.Unmarshal(op.Result, &result); err != nil {
		t.Fatal(err)
	}
	return result.Assignment
}
func admissionCurrent(t *testing.T, h *harness, id string) protocol.Issue {
	t.Helper()
	var i protocol.Issue
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		i, err = store.GetCanonicalIssue(t.Context(), tx.Q(), id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return i
}

func TestIssueAdmissionCreationIsPureBacklogAndTodoIsClaimSet(t *testing.T) {
	h, r := managerClaimHarness(t)
	i := createBacklogTestIssue(t, h)
	if i.WorkStatus != protocol.IssueBacklog {
		t.Fatal(i)
	}
	for _, table := range []string{"tasks", "plan_actions", "runs", "leases"} {
		if h.count(table) != 0 {
			t.Fatalf("creation allocated %s", table)
		}
	}
	if got := admissionClaim(t, h, r); got != nil {
		t.Fatalf("backlog claimed: %+v", got)
	}
	for _, status := range []protocol.IssueWorkStatus{protocol.IssueInProgress, protocol.IssueInReview, protocol.IssueDone} {
		op := h.exec(owner, h.env("issues.set_status", protocol.Target{Type: "issue", ID: i.IssueID}, protocol.SetIssueStatusPayload{ExpectedAssignmentEpoch: i.AssignmentEpoch, Status: status}, rev(i.Revision)), CanonicalOwnerIssueCommand{Action: "set_status"})
		if op.Error == nil || op.Error.Code != protocol.CodeInvalidTransition {
			t.Fatalf("backlog skip %s: %+v", status, op)
		}
	}
	i = issueAdmissionStatus(t, h, i, protocol.IssueTodo, &protocol.IssueOwnerRequest{CategoryID: protocol.DefaultIssueManagerCategory})
	if h.count("tasks") != 0 || h.count("plan_actions") != 0 {
		t.Fatal("todo preallocated a carrier")
	}
	// Empty ownership_policy selects ordinary owner execution, so this manager-only
	// inventory cannot run it. Select the manager category contract explicitly.
	op := h.exec(owner, h.env(protocol.OpIssueRequestManager, protocol.Target{Type: "issue", ID: i.IssueID}, protocol.RequestIssueManagerPayload{CategoryID: protocol.DefaultIssueManagerCategory, ExpectedAssignmentEpoch: i.AssignmentEpoch, Reason: "Select continuing coordinator capability"}, rev(i.Revision)), RequestIssueManager{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	as := admissionClaim(t, h, r)
	if as == nil || as.Manager == nil || h.count("tasks") != 1 || h.count("runs") != 1 || h.count("leases") != 1 {
		t.Fatalf("todo not atomically claimed: %+v", as)
	}
	if again := admissionClaim(t, h, r); again != nil {
		t.Fatalf("duplicate owner: %+v", again)
	}
}

func TestIssueAdmissionConcurrentClaimsMaterializeOneResponsibility(t *testing.T) {
	h, r := pendingIssueOwnerHarness(t)
	i := createBacklogTestIssue(t, h)
	i = issueAdmissionStatus(t, h, i, protocol.IssueTodo, &protocol.IssueOwnerRequest{CategoryID: "implementer"})
	requests := []*protocol.Envelope{h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{Runtime: r, WorkspaceProtocol: 1}, nil), h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{Runtime: r, WorkspaceProtocol: 1}, nil)}
	var wg sync.WaitGroup
	for _, env := range requests {
		wg.Go(func() {
			op, err := h.app.Execute(t.Context(), runner("linux-1"), env, Claim{})
			if err != nil || op.Status != protocol.OpSucceeded {
				t.Errorf("claim: %+v %v", op, err)
			}
		})
	}
	wg.Wait()
	if h.count("tasks") != 1 || h.count("runs") != 1 || h.count("issue_manager_tasks") != 1 {
		t.Fatalf("concurrent materialization: tasks=%d runs=%d", h.count("tasks"), h.count("runs"))
	}
}

func TestIssuePauseHandoffEndsRunRetainsTaskAndCreatesOneSuccessor(t *testing.T) {
	h, r := pendingIssueOwnerHarness(t)
	r.Capabilities = append(r.Capabilities, protocol.CapabilityPauseHandoffs)
	i := issueAdmissionStatus(t, h, createBacklogTestIssue(t, h), protocol.IssueTodo, &protocol.IssueOwnerRequest{CategoryID: "implementer"})
	as := admissionClaim(t, h, r)
	if as == nil || !as.PauseHandoffs {
		t.Fatal(as)
	}
	i = admissionCurrent(t, h, i.IssueID)
	original := h.task(as.TaskID)
	i = issueAdmissionStatus(t, h, i, protocol.IssueBacklog, nil)
	paused := h.run(as.RunID)
	var action store.ActionRow
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		action, err = store.GetAction(t.Context(), tx.Q(), as.ActionID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if desiredFor(paused, action, h.task(as.TaskID)) != protocol.DesiredPaused || paused.PauseHandoff == nil {
		t.Fatal(paused)
	}
	if h.task(as.TaskID).ControlMode != original.ControlMode || h.task(as.TaskID).ControlRevision != original.ControlRevision {
		t.Fatal("Issue pause overwrote independent Task control")
	}
	if h.lease(as.RunID).Status != store.LeaseActive {
		t.Fatal("released without stop proof")
	}
	// A quick todo must finish the original physical handoff before resuming.
	i = issueAdmissionStatus(t, h, i, protocol.IssueTodo, nil)
	if next := admissionClaim(t, h, r); next != nil {
		t.Fatal("resumed before confirmed stop", next)
	}
	env := h.env(protocol.OpRunsYieldPause, runTarget(as.RunID), protocol.YieldPausePayload{PauseHandoff: *paused.PauseHandoff, ThreadID: "retained-native-thread"}, nil)
	op := h.exec(runPrincipal(as.RunID, as.Role), env, YieldPause{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.run(as.RunID).ObservedState != protocol.RunFinished || h.lease(as.RunID).Status != store.LeaseReleased {
		t.Fatal("physical handoff did not release slot")
	}
	if replay := h.exec(runPrincipal(as.RunID, as.Role), env, YieldPause{}); replay.Status != protocol.OpSucceeded {
		t.Fatal(replay)
	}
	next := admissionClaim(t, h, r)
	if next == nil || next.TaskID != as.TaskID || next.SpecVersion != as.SpecVersion || next.Generation != as.Generation || next.PauseContinuation == nil || next.PauseContinuation.ThreadID != "retained-native-thread" {
		t.Fatalf("incorrect successor: %+v", next)
	}
	if again := admissionClaim(t, h, r); again != nil {
		t.Fatal("duplicate successor", again)
	}
}

func TestIssueAdmissionAncestorBacklogGatesChildAndPauseRetainsIndependentControl(t *testing.T) {
	h, r := pendingIssueOwnerHarness(t)
	r.Capabilities = append(r.Capabilities, protocol.CapabilityPauseHandoffs)
	inventoryCategory(t, h, "planning", 0, "active")
	parent := createBacklogTestIssue(t, h)
	op := h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, protocol.CreateIssuePayload{ParentIssueID: parent.IssueID, ExpectedParentRevision: rev(parent.Revision), Plan: protocol.IssuePlan{Title: "Child", Project: "default", Intent: "Continue the selected part"}}, nil), CanonicalOwnerIssueCommand{Action: "create"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var child protocol.Issue
	if err := json.Unmarshal(op.Result, &child); err != nil {
		t.Fatal(err)
	}
	child = issueAdmissionStatus(t, h, child, protocol.IssueTodo, &protocol.IssueOwnerRequest{CategoryID: "implementer"})
	if as := admissionClaim(t, h, r); as != nil {
		t.Fatal("ancestor backlog admitted child", as)
	}
	parent = issueAdmissionStatus(t, h, parent, protocol.IssueTodo, &protocol.IssueOwnerRequest{CategoryID: "planning"})
	as := admissionClaim(t, h, r)
	if as == nil || as.Manager.IssueID != child.IssueID {
		t.Fatal(as)
	}
	task := h.task(as.TaskID)
	if op := h.exec(owner, h.env("control.set_mode", taskTarget(task.TaskID), protocol.SetControlModePayload{Mode: protocol.ControlPaused}, rev(task.ControlRevision)), SetControlMode{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	parent = issueAdmissionStatus(t, h, admissionCurrent(t, h, parent.IssueID), protocol.IssueBacklog, nil)
	childNow := admissionCurrent(t, h, child.IssueID)
	if childNow.BoardStatus() != protocol.IssueTodo {
		t.Fatal("parent rewrote child work status", childNow)
	}
	paused := h.run(as.RunID)
	if paused.PauseHandoff == nil || paused.PauseHandoff.IssueID != parent.IssueID {
		t.Fatal("child run not covered", paused)
	}
	op = h.runAs(*as, protocol.OpRunsYieldPause, protocol.YieldPausePayload{PauseHandoff: *paused.PauseHandoff}, YieldPause{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	parent = issueAdmissionStatus(t, h, parent, protocol.IssueTodo, nil)
	if next := admissionClaim(t, h, r); next != nil {
		t.Fatal("resume overrode independent Task pause", next)
	}
	current := h.task(task.TaskID)
	if current.ControlMode != protocol.ControlPaused {
		t.Fatal(current)
	}
	if op := h.exec(owner, h.env("control.set_mode", taskTarget(task.TaskID), protocol.SetControlModePayload{Mode: protocol.ControlManaged}, rev(current.ControlRevision)), SetControlMode{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if next := admissionClaim(t, h, r); next == nil || next.TaskID != as.TaskID {
		t.Fatal("explicit control resume did not continue original task", next)
	}
}

func TestIssuePauseUnsupportedRunnerKeepsSlotAndCancellationWins(t *testing.T) {
	for _, supported := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "negotiated"}[supported], func(t *testing.T) {
			h, r := pendingIssueOwnerHarness(t)
			if supported {
				r.Capabilities = append(r.Capabilities, protocol.CapabilityPauseHandoffs)
			}
			issue := issueAdmissionStatus(t, h, createBacklogTestIssue(t, h), protocol.IssueTodo, &protocol.IssueOwnerRequest{CategoryID: "implementer"})
			as := admissionClaim(t, h, r)
			if as == nil {
				t.Fatal("missing owner")
			}
			issue = issueAdmissionStatus(t, h, admissionCurrent(t, h, issue.IssueID), protocol.IssueBacklog, nil)
			paused := h.run(as.RunID)
			payload := protocol.YieldPausePayload{PauseHandoff: *paused.PauseHandoff}
			if !supported {
				op := h.runAs(*as, protocol.OpRunsYieldPause, payload, YieldPause{})
				if op.Status != protocol.OpFailed || h.lease(as.RunID).Status != store.LeaseActive {
					t.Fatal("unnegotiated stop released slot", op)
				}
				return
			}
			op := h.exec(owner, h.env("issues.cancel", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Cancel the paused responsibility"}, rev(issue.Revision)), CanonicalOwnerIssueCommand{Action: "cancel"})
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			op = h.runAs(*as, protocol.OpRunsYieldPause, payload, YieldPause{})
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			var successor bool
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				return tx.Q().QueryRowContext(t.Context(), `SELECT continuation_action_id IS NOT NULL FROM run_pause_handoffs WHERE run_id=?`, as.RunID).Scan(&successor)
			}); err != nil {
				t.Fatal(err)
			}
			if successor {
				t.Fatal("cancellation created a pause continuation")
			}
			if next := admissionClaim(t, h, r); next != nil && next.PauseContinuation != nil {
				t.Fatal("cancel resumed paused work", next)
			}
		})
	}
}

func TestIssueAdmissionInitializerAppendsWithoutRewritingPureRecords(t *testing.T) {
	h := newHarness(t)
	id := h.ids.New(protocol.PrefixWorkItem)
	old := protocol.Issue{IssueControl: protocol.IssueControl{IssueID: id, State: protocol.IssueActive, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: owner.Subject}, AssignmentEpoch: 1, AssignmentState: "active"}, Revision: 1, Plan: protocol.IssuePlan{Title: "Retained intent", Project: "default", Intent: "Record only", Stages: []protocol.IssueStage{}}, DependsOn: []string{}, CreatedAt: h.clock.Now().UnixMilli(), UpdatedAt: h.clock.Now().UnixMilli()}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		return tx.SaveCanonicalIssue(t.Context(), old, 0, store.IssueRevisionProvenance{Actor: owner, RequestID: h.ids.New(protocol.PrefixRequest)})
	}); err != nil {
		t.Fatal(err)
	}
	outbox := h.count("outbox")
	if err := h.app.InitializeIssueAdmission(t.Context()); err != nil {
		t.Fatal(err)
	}
	current := admissionCurrent(t, h, id)
	if current.Revision != 2 || current.WorkStatus != protocol.IssueBacklog || h.count("tasks") != 0 || h.count("outbox") != outbox {
		t.Fatal("initializer executed recorded intent", current)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		snapshot, err := store.GetCanonicalIssueRevision(t.Context(), tx.Q(), id, 1)
		if err == nil && snapshot.WorkStatus != "" {
			t.Fatal("historical snapshot changed", snapshot)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	requests := h.count("request_records")
	if err := h.app.InitializeIssueAdmission(t.Context()); err != nil {
		t.Fatal(err)
	}
	if h.count("request_records") != requests || admissionCurrent(t, h, id).Revision != 2 {
		t.Fatal("initializer repeated a settled conversion")
	}
}

func TestIssuePausePhysicalStopDoesNotReplayUnknownNativeWork(t *testing.T) {
	h, r := managerClaimHarness(t)
	r.Capabilities = append(r.Capabilities, protocol.CapabilityPauseHandoffs)
	i := createBacklogTestIssue(t, h)
	op := h.exec(owner, h.env(protocol.OpIssueRequestManager, protocol.Target{Type: "issue", ID: i.IssueID}, protocol.RequestIssueManagerPayload{ExpectedAssignmentEpoch: i.AssignmentEpoch, Reason: "Select continuing coordination"}, rev(i.Revision)), RequestIssueManager{})
	i = canonicalLifecycleIssue(t, op)
	i = issueAdmissionStatus(t, h, i, protocol.IssueTodo, nil)
	as := admissionClaim(t, h, r)
	if as == nil {
		t.Fatal("no claim")
	}
	request, err := h.app.QueueManagerNative(t.Context(), as.RunID, "turn", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	claimed := h.runAs(*as, protocol.OpManagerNativeClaim, map[string]any{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim})
	if claimed.Status != protocol.OpSucceeded {
		t.Fatal(claimed)
	}
	i = canonicalTaskIssue(t, h, i.IssueID)
	i = issueAdmissionStatus(t, h, i, protocol.IssueBacklog, nil)
	paused := h.run(as.RunID)
	yielded := h.runAs(*as, protocol.OpRunsYieldPause, protocol.YieldPausePayload{PauseHandoff: *paused.PauseHandoff}, YieldPause{})
	if yielded.Status != protocol.OpSucceeded {
		t.Fatal(yielded)
	}
	i = issueAdmissionStatus(t, h, i, protocol.IssueTodo, nil)
	if next := admissionClaim(t, h, r); next != nil {
		t.Fatal("unknown native request replayed", request, next)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var status string
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT status FROM manager_native_requests WHERE request_id=?`, request).Scan(&status); err != nil {
			return err
		}
		lease, err := store.GetLeaseByRun(t.Context(), tx.Q(), as.RunID)
		if err == nil && (status != "unknown" || lease.Status != "released") {
			t.Fatal(status, lease)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
