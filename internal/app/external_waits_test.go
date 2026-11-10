package app

import (
	"encoding/json"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"strings"
	"testing"
	"time"
)

func externalWaitFixture(t *testing.T) (*harness, *protocol.RunnerRuntime, protocol.Assignment, string) {
	t.Helper()
	h := newHarness(t)
	runtime := testRuntime("wait-runner")
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityExternalWaits)
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	issue := createCanonicalOwnerTestIssue(t, h)
	first := createCanonicalTaskForTest(t, h, issue)
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	second := createCanonicalTaskForTest(t, h, issue)
	as, ok := h.claimInputRun(runtime)
	if !ok {
		t.Fatal("no work")
	}
	other := second
	if as.TaskID == second {
		other = first
	}
	if op := h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	return h, runtime, as, other
}
func waitForTask(t *testing.T, h *harness, as protocol.Assignment, target string, deadline int64) protocol.ExternalWait {
	t.Helper()
	op := h.runAs(as, protocol.OpRunsWaitExternal, protocol.WaitExternalPayload{WaitKey: "existing-work", Kind: "task", TargetID: target, Reason: "Wait for the existing investigation", Recovery: "Read the original result and continue this Task", DeadlineAtMillis: deadline}, WaitExternal{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var v protocol.ExternalWait
	if err := json.Unmarshal(op.Result, &v); err != nil {
		t.Fatal(err)
	}
	return v
}
func TestExternalWaitReleasesOnlyPhysicalStopAndContinuesOriginalTask(t *testing.T) {
	h, runtime, as, other := externalWaitFixture(t)
	before := h.task(as.TaskID)
	v := waitForTask(t, h, as, other, 0)
	if h.lease(as.RunID).Status != store.LeaseActive || h.run(as.RunID).ObservedState != protocol.RunRunning {
		t.Fatal("wait intent released live process")
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE tasks SET lifecycle='completed' WHERE task_id=?`, other)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.app.SweepExternalWaits(t.Context()); err != nil {
		t.Fatal(err)
	}
	if h.count("run_external_waits WHERE continuation_action_id IS NOT NULL") != 0 {
		t.Fatal("ready condition bypassed physical exit")
	}
	env := h.env(protocol.OpRunsYieldExternalWait, runTarget(as.RunID), protocol.YieldExternalWaitPayload{WaitID: v.WaitID, ThreadID: "previous-session"}, nil)
	op := h.exec(runPrincipal(as.RunID, as.Role), env, YieldExternalWait{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if replay := h.exec(runPrincipal(as.RunID, as.Role), env, YieldExternalWait{}); string(replay.Result) != string(op.Result) {
		t.Fatal("yield replay changed", replay)
	}
	if h.lease(as.RunID).Status != store.LeaseReleased {
		t.Fatal("lease retained after exit")
	}
	next, ok := h.claimInputRun(runtime)
	if !ok || next.TaskID != as.TaskID || next.Generation != as.Generation || next.SpecVersion != as.SpecVersion || next.ExternalWaitContinuation == nil || next.ExternalWaitContinuation.WakeReason != "task_completed" {
		t.Fatal("wait did not resume original responsibility", next)
	}
	if !strings.Contains(string(next.Spec), v.WaitID) || !strings.Contains(string(next.Spec), v.Recovery) {
		t.Fatal("reconstructed context depended on the new native session or protocol field")
	}
	after := h.task(as.TaskID)
	if before.SpecVersion != after.SpecVersion || before.ImplementGeneration != after.ImplementGeneration {
		t.Fatal("machine wait consumed attempt or changed agreement")
	}
	if h.count("decisions") != 0 {
		t.Fatal("machine wait created human decision")
	}
}
func TestExternalWaitReminderIsCoalescedWithoutBusinessReplay(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "reminder", true: "deadline"}[deadline], func(t *testing.T) {
			h, _, as, other := externalWaitFixture(t)
			at := int64(0)
			if deadline {
				at = h.clock.Now().Add(time.Minute).UnixMilli()
			}
			v := waitForTask(t, h, as, other, at)
			if op := h.runAs(as, protocol.OpRunsYieldExternalWait, protocol.YieldExternalWaitPayload{WaitID: v.WaitID}, YieldExternalWait{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if err := h.app.SweepExternalWaits(t.Context()); err != nil {
				t.Fatal(err)
			}
			if h.count("run_external_waits WHERE continuation_action_id IS NOT NULL") != 0 {
				t.Fatal("woke before deadline")
			}
			h.clock.Advance(externalWaitReminder)
			for range 3 {
				if err := h.app.SweepExternalWaits(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			waits, err := h.app.ExternalWaits(t.Context(), as.TaskID)
			if err != nil || len(waits) != 1 || waits[0].WakeReason != map[bool]string{false: "reminder", true: "deadline"}[deadline] || h.count("run_external_waits WHERE continuation_action_id IS NOT NULL") != 1 {
				t.Fatal(waits, err)
			}
		})
	}
}
func TestExternalWaitScopeAndIdentityArePreserved(t *testing.T) {
	h, _, as, other := externalWaitFixture(t)
	v := waitForTask(t, h, as, other, 0)
	changed := v.WaitExternalPayload
	changed.Recovery = "different recovery"
	env := h.env(protocol.OpRunsWaitExternal, runTarget(as.RunID), changed, nil)
	op, err := h.app.Execute(t.Context(), runPrincipal(as.RunID, as.Role), env, WaitExternal{})
	if err == nil && op.Status != protocol.OpFailed {
		t.Fatal("changed wait accepted", op)
	}
	if op := h.runAs(as, protocol.OpRunsYieldExternalWait, protocol.YieldExternalWaitPayload{WaitID: "other-wait"}, YieldExternalWait{}); op.Status != protocol.OpFailed {
		t.Fatal("wrong stop identity accepted", op)
	}
	if h.lease(as.RunID).Status != store.LeaseActive {
		t.Fatal("invalid handoff freed slot")
	}
}

func TestExternalWaitPhysicalStopLetsOtherTaskUseSlot(t *testing.T) {
	h, runtime, as, other := externalWaitFixture(t)
	config := testConfig()
	config.Hosts[0].Slots = 1
	if op := h.syncConfig(config); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	v := waitForTask(t, h, as, other, 0)
	if next, ok := h.claimInputRun(runtime); ok {
		t.Fatal("intent released the running process's slot", next)
	}
	if op := h.runAs(as, protocol.OpRunsYieldExternalWait, protocol.YieldExternalWaitPayload{WaitID: v.WaitID}, YieldExternalWait{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if next, ok := h.claimInputRun(runtime); !ok || next.TaskID != other {
		t.Fatal("waiting Task did not release real capacity to other work", next, ok)
	}
	waits, err := h.app.ExternalWaits(t.Context(), as.TaskID)
	if err != nil || len(waits) != 1 || waits[0].Status != "waiting" {
		t.Fatal(waits, err)
	}
}

func TestExternalWaitUnchangedObservationDoesNotWakeSchedulers(t *testing.T) {
	h, _, as, other := externalWaitFixture(t)
	v := waitForTask(t, h, as, other, 0)
	if op := h.runAs(as, protocol.OpRunsYieldExternalWait, protocol.YieldExternalWaitPayload{WaitID: v.WaitID}, YieldExternalWait{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	wake, visible := h.app.SchedulingChanges().Wait(), h.app.Changes().Wait()
	h.clock.Advance(5 * time.Second)
	if err := h.app.SweepExternalWaits(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-wake:
		t.Fatal("unchanged wait caused a full scheduling recomputation")
	default:
	}
	select {
	case <-visible:
		t.Fatal("unchanged wait produced a visible business change")
	default:
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE tasks SET lifecycle='completed' WHERE task_id=?`, other)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(5 * time.Second)
	if err := h.app.SweepExternalWaits(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-wake:
	default:
		t.Fatal("actual completion did not wake scheduling")
	}
}

func TestExternalWaitEarlyResumePreservesPhysicalStopAndOriginalOperation(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		t.Run(map[bool]string{false: "stopping", true: "stopped"}[stopped], func(t *testing.T) {
			h, runtime, as, other := externalWaitFixture(t)
			before := h.task(as.TaskID)
			v := waitForTask(t, h, as, other, 0)
			if stopped {
				if op := h.runAs(as, protocol.OpRunsYieldExternalWait, protocol.YieldExternalWaitPayload{WaitID: v.WaitID, ThreadID: "original-session"}, YieldExternalWait{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			payload := protocol.ResumeExternalWaitPayload{WaitID: v.WaitID, Reason: "Read the available evidence and choose whether continued waiting is useful"}
			env := h.env(protocol.OpTasksResumeWait, taskTarget(as.TaskID), payload, rev(before.ControlRevision))
			op := h.exec(owner, env, ResumeExternalWait{})
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if replay := h.exec(owner, env, ResumeExternalWait{}); string(replay.Result) != string(op.Result) {
				t.Fatal("resumption replay changed", replay)
			}
			if !stopped {
				if h.lease(as.RunID).Status != store.LeaseActive || h.count("run_external_waits WHERE continuation_action_id IS NOT NULL") != 0 {
					t.Fatal("manual resumption bypassed a live execution")
				}
				if op := h.runAs(as, protocol.OpRunsYieldExternalWait, protocol.YieldExternalWaitPayload{WaitID: v.WaitID, ThreadID: "original-session"}, YieldExternalWait{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			next, ok := h.claimInputRun(runtime)
			// The independent dependency was already queued first; either assignment
			// may claim the newly free slots, while the continuation must exist once.
			if ok && next.TaskID == other {
				next, ok = h.claimInputRun(runtime)
			}
			if !ok || next.TaskID != as.TaskID || next.ExternalWaitContinuation == nil || next.ExternalWaitContinuation.WakeReason != "requested" || next.ExternalWaitContinuation.ResumeReason != payload.Reason {
				t.Fatal(next, ok)
			}
			if h.task(other).Lifecycle.Terminal() || h.count("decisions") != 0 || h.count("run_external_waits WHERE continuation_action_id IS NOT NULL") != 1 {
				t.Fatal("resumption changed or duplicated the original external operation")
			}
			after := h.task(as.TaskID)
			if before.SpecVersion != after.SpecVersion || before.ImplementGeneration != after.ImplementGeneration {
				t.Fatal("resumption replaced the existing goal")
			}
		})
	}
}

func TestExternalWaitEarlyResumeRequiresOriginalWaitAndTaskRevision(t *testing.T) {
	h, _, as, other := externalWaitFixture(t)
	v := waitForTask(t, h, as, other, 0)
	for _, candidate := range []struct {
		name, task, wait string
		revision         int64
		code             protocol.Code
	}{
		{"stale Task", as.TaskID, v.WaitID, h.task(as.TaskID).ControlRevision + 1, protocol.CodeStaleRevision},
		{"another Task", other, v.WaitID, h.task(other).ControlRevision, protocol.CodeNotFound},
	} {
		t.Run(candidate.name, func(t *testing.T) {
			op := h.exec(owner, h.env(protocol.OpTasksResumeWait, taskTarget(candidate.task), protocol.ResumeExternalWaitPayload{WaitID: candidate.wait, Reason: "Reconsider next step"}, rev(candidate.revision)), ResumeExternalWait{})
			if op.Status != protocol.OpFailed || op.Error.Code != candidate.code {
				t.Fatal(op)
			}
		})
	}
	if h.lease(as.RunID).Status != store.LeaseActive {
		t.Fatal("invalid resumption released the process")
	}
}

func TestExternalWaitManagerNativeClosePreservesMachineWaitAndWakesOriginalTask(t *testing.T) {
	for _, natural := range []bool{false, true} {
		t.Run(map[bool]string{false: "physical-stop", true: "natural-close"}[natural], func(t *testing.T) {
			h, runtime := managerClaimHarness(t)
			issue := createCanonicalOwnerTestIssue(t, h)
			if op := h.exec(owner, h.env(protocol.OpIssueRequestManager, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.RequestIssueManagerPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Own this work"}, rev(issue.Revision)), RequestIssueManager{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			as := ownerClaim(t, h, runtime)
			if as.Role != protocol.RoleIssueManager {
				t.Fatal(as)
			}
			issue = canonicalTaskIssue(t, h, issue.IssueID)
			dependency := createCanonicalTaskForTest(t, h, issue)
			v := waitForTask(t, h, as, dependency, 0)
			if h.lease(as.RunID).Status != store.LeaseActive {
				t.Fatal("intent released live owner")
			}
			runtime.Sequence++
			state, err := h.app.RuntimeHeartbeat(t.Context(), runner(as.Execution.Identity.ExecutionEnvironmentID), protocol.HeartbeatPayload{Runtime: runtime, Runs: []protocol.LeaseRef{{RunID: as.RunID, LeaseID: as.LeaseID}}})
			if err != nil || len(state.Runs) != 1 || state.Runs[0].Desired != protocol.DesiredPaused || state.Runs[0].ExternalWait == nil {
				t.Fatal("wait did not stop actual native owner", state, err)
			}
			if natural {
				closeID, err := h.app.QueueManagerNative(t.Context(), as.RunID, "close", json.RawMessage(`{}`))
				if err != nil {
					t.Fatal(err)
				}
				h.runAs(as, protocol.OpManagerNativeClaim, map[string]any{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim})
				if op := h.runAs(as, protocol.OpManagerNativeReply, protocol.ManagerNativeReply{ID: closeID, Result: json.RawMessage(`null`)}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			} else {
				stopOwnerClaim(t, h, as)
			}
			waits, err := h.app.ExternalWaits(t.Context(), as.TaskID)
			if err != nil || len(waits) != 1 || !waits[0].Stopped || waits[0].Status != "waiting" || waits[0].WaitID != v.WaitID {
				t.Fatal(waits, err)
			}
			if h.lease(as.RunID).Status != store.LeaseReleased {
				t.Fatal("stopped owner retained slot")
			}
			h.clock.Advance(externalWaitReminder)
			if err := h.app.SweepExternalWaits(t.Context()); err != nil {
				t.Fatal(err)
			}
			waits, err = h.app.ExternalWaits(t.Context(), as.TaskID)
			if err != nil || waits[0].Status != "continued" || waits[0].WakeReason != "reminder" {
				t.Fatal(waits, err)
			}
			if h.count("conversation_messages WHERE body LIKE 'External wait returned to this Issue owner:%'") != 1 {
				t.Fatal("wake did not deliver original recovery facts")
			}
			var count int
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				return tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM plan_actions WHERE task_id=? AND status='pending'`, as.TaskID).Scan(&count)
			}); err != nil || count != 1 {
				t.Fatal("owner continuation duplicated", count, err)
			}
		})
	}
}

func TestExternalWaitOwnerInputResumesStoppedTaskAndIgnoresSystemActivity(t *testing.T) {
	h, runtime := managerClaimHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	if op := h.exec(owner, h.env(protocol.OpIssueRequestManager, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.RequestIssueManagerPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Own this work"}, rev(issue.Revision)), RequestIssueManager{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	as := ownerClaim(t, h, runtime)
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	dependency := createCanonicalTaskForTest(t, h, issue)
	v := waitForTask(t, h, as, dependency, 0)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		message := h.ids.New(protocol.PrefixMessage)
		if _, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: message, ConversationID: as.Manager.ConversationID, Author: "system", Body: "Ordinary progress"}); err != nil {
			return err
		}
		_, err := tx.InsertEvent(t.Context(), store.EventRow{EventID: h.ids.New(protocol.PrefixEvent), Source: "app", ObjectType: "message", ObjectID: message, Kind: "conversation.message", BodyJSON: `{}`})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	stopOwnerClaim(t, h, as)
	h.clock.Advance(5 * time.Second)
	if err := h.app.SweepExternalWaits(t.Context()); err != nil {
		t.Fatal(err)
	}
	waits, err := h.app.ExternalWaits(t.Context(), as.TaskID)
	if err != nil || len(waits) != 1 || waits[0].Status != "waiting" {
		t.Fatal("ordinary activity woke the waiting owner", waits, err)
	}
	op := h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{ConversationID: as.Manager.ConversationID, Body: "Please reconsider the approach using the available evidence"}, nil), SubmitMessage{})
	if op.Status != protocol.OpAccepted && op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	h.clock.Advance(5 * time.Second)
	for range 2 {
		if err := h.app.SweepExternalWaits(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	waits, err = h.app.ExternalWaits(t.Context(), as.TaskID)
	if err != nil || len(waits) != 1 || waits[0].WaitID != v.WaitID || waits[0].WakeReason != "owner_input" || waits[0].Status != "continued" {
		t.Fatal("new owner input did not resume the original Task", waits, err)
	}
	if h.count("run_external_waits WHERE continuation_action_id IS NOT NULL") != 1 || h.task(dependency).Lifecycle.Terminal() {
		t.Fatal("new input duplicated or completed the external operation")
	}
}

func TestTaskContinuationOffersSameReviewerSessionAndFreshContext(t *testing.T) {
	h, runtime, issue, source, _, _ := sharedReviewFixture(t)
	sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: source}))
	task := createSharedReviewerTaskForTest(t, h, issue.IssueID)
	review := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: task}))
	executeSharedReviewForTest(t, h, runtime, review, task, "passed")
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		wait := protocol.ExternalWait{WaitExternalPayload: protocol.WaitExternalPayload{Kind: "review", TargetID: review.ReviewID}}
		ready, reason, err := observeExternalWait(t.Context(), tx.Q(), &wait)
		if err == nil && (!ready || reason != "review_task_completed") {
			t.Fatal("review completion required the waiting owner to record it first", ready, reason)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	review = sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
	oldRun := review.Rounds[0].RunID
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO run_session_stops(run_id,thread_id,stopped_at) VALUES(?,?,?)`, oldRun, "stopped-review-session", h.clock.Now().UnixMilli())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	resource := h.resourceForTask(task)
	if !h.releaseResource(task, resource.WorkspaceID) {
		t.Fatal("release")
	}
	if op := continueTaskForTest(t, h, task, "Check the rebase delta using retained evidence"); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	next, ok := h.claimInputRun(runtime)
	if !ok || next.TaskID != task || next.SessionContinuation == nil || next.SessionContinuation.RunID != oldRun || next.SessionContinuation.ThreadID != "stopped-review-session" {
		t.Fatal(next)
	}
	if !strings.Contains(string(next.Spec), "Check the rebase delta") {
		t.Fatal("reused session lacks current ledger context")
	}
	parent, err := h.app.NativeQuestionPredecessor(t.Context(), next.RunID)
	if err != nil || parent != oldRun {
		t.Fatal("native broker cannot validate the continued session", parent, err)
	}
	// Native history can be discarded: the current spec still contains the next
	// step and the original report index, independent of the optional locator.
	next.SessionContinuation = nil
	if !strings.Contains(string(next.Spec), review.Rounds[0].ArtifactID) {
		t.Fatal("fresh session lacks original report index")
	}
}
