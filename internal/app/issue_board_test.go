package app

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func moveBoardIssue(t *testing.T, h *harness, issue protocol.Issue, status protocol.IssueWorkStatus) protocol.Issue {
	t.Helper()
	if status == protocol.IssueTodo && (issue.State == protocol.IssueClosed || issue.State == protocol.IssueCancelled) {
		issue = moveBoardIssue(t, h, issue, protocol.IssueBacklog)
	}
	payload := protocol.SetIssueStatusPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Status: status}
	if status == protocol.IssueTodo && issue.OwnerSelection == nil {
		const category = "board-execution"
		var exists bool
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			return tx.Q().QueryRowContext(t.Context(), `SELECT EXISTS(SELECT 1 FROM agent_category_versions WHERE category_id=?)`, category).Scan(&exists)
		}); err != nil {
			t.Fatal(err)
		}
		if !exists {
			if op := h.exec(owner, h.env("categories.put", protocol.Target{Type: "category", ID: category}, protocol.PutAgentCategoryPayload{Title: "Board execution", State: "active"}, rev(0)), PutAgentCategory{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
		}
		payload.Owner = &protocol.IssueOwnerRequest{CategoryID: category, Reason: "Explicitly continue the board goal"}
	}
	return canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "set_status", payload))
}
func statusNotice(t *testing.T, h *harness, id string) (issueStatusNotice, bool) {
	t.Helper()
	var n issueStatusNotice
	err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		n, err = readIssueStatusNotice(t.Context(), tx.Q(), id)
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return n, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return n, true
}
func sweepStatus(t *testing.T, h *harness) {
	t.Helper()
	c := SweepIssueStatusNotifications{}
	if op := h.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, h.env(c.Operation(), protocol.Target{Type: "issue_settings"}, struct{}{}, nil), c); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}
func statusSourceCount(t *testing.T, h *harness, active bool) int {
	t.Helper()
	var n int
	query := `SELECT COUNT(*) FROM background_sources WHERE kind='issue.status_changed'`
	if active {
		query += ` AND cancelled_at=0`
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error { return tx.Q().QueryRowContext(t.Context(), query).Scan(&n) }); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestIssueBoardStateNoopReplayEpochAndEndpoints(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	initial := issue
	p := protocol.SetIssueStatusPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Status: protocol.IssueInProgress}
	env := h.env("issues.set_status", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(issue.Revision))
	op := h.exec(owner, env, CanonicalOwnerIssueCommand{Action: "set_status"})
	issue = canonicalLifecycleIssue(t, op)
	if issue.State != protocol.IssueActive || issue.BoardStatus() != protocol.IssueInProgress || issue.Revision != initial.Revision+1 {
		t.Fatal(issue)
	}
	n, _ := statusNotice(t, h, issue.IssueID)
	h.clock.Advance(time.Minute)
	noop := moveBoardIssue(t, h, issue, protocol.IssueInProgress)
	after, _ := statusNotice(t, h, issue.IssueID)
	if noop.Revision != issue.Revision || n != after {
		t.Fatal("no-op reset window")
	}
	replay := h.exec(owner, env, CanonicalOwnerIssueCommand{Action: "set_status"})
	expectWorkCode(t, replay, protocol.CodeStaleRevision)
	if current := canonicalTaskIssue(t, h, issue.IssueID); current.Revision != issue.Revision || current.BoardStatus() != protocol.IssueInProgress {
		t.Fatal("stale board write changed current business state", current)
	}
	expectWorkCode(t, canonicalLifecycleControl(t, h, issue, "set_status", protocol.SetIssueStatusPayload{ExpectedAssignmentEpoch: 99, Status: protocol.IssueInReview}), protocol.CodeStaleRevision)
	issue = moveBoardIssue(t, h, issue, protocol.IssueDone)
	if issue.State != protocol.IssueClosed || issue.Closure == nil || issue.Closure.Evidence[0].Reference == "current_request" {
		t.Fatal(issue)
	}
	issue = moveBoardIssue(t, h, issue, protocol.IssueTodo)
	if issue.State != protocol.IssueActive || issue.Closure != nil {
		t.Fatal(issue)
	}
	issue = moveBoardIssue(t, h, issue, protocol.IssueWorkCancelled)
	if issue.State != protocol.IssueCancelled {
		t.Fatal(issue)
	}
	issue = moveBoardIssue(t, h, issue, protocol.IssueDone)
	issue = moveBoardIssue(t, h, issue, protocol.IssueWorkCancelled)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		saved, err := store.GetCanonicalIssue(t.Context(), tx.Q(), issue.IssueID)
		if err != nil {
			return err
		}
		actual, _ := json.Marshal(saved)
		want, _ := json.Marshal(issue)
		if !bytes.Equal(actual, want) {
			t.Fatal("current disagrees with receipt")
		}
		history, err := store.GetCanonicalIssueRevision(t.Context(), tx.Q(), initial.IssueID, initial.Revision)
		if err == nil && history.WorkStatus != initial.WorkStatus {
			t.Fatal("historical progress was rewritten")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIssueBoardAgentStatusPreservesEarlierExternalWake(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	issue = moveBoardIssue(t, h, issue, protocol.IssueInProgress)
	turn, principal := startHostedIssueReview(t, h, issue)
	p := protocol.SetIssueStatusPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Status: protocol.IssueInReview}
	issue = canonicalLifecycleIssue(t, hostedEffect(t, h, principal, turn, 1, protocol.Target{Type: "issue", ID: issue.IssueID}, rev(issue.Revision), p, CanonicalOwnerIssueCommand{Action: "set_status"}))
	if _, ok := statusNotice(t, h, issue.IssueID); !ok {
		t.Fatal("agent change discarded the earlier external notification")
	}
	h.clock.Advance(5 * time.Minute)
	sweepStatus(t, h)
	if statusSourceCount(t, h, false) != 1 {
		t.Fatal("the earlier external status must be delivered exactly once")
	}
	p.Status = protocol.IssueDone
	command := HostedCoordinationCommand{Command: CanonicalOwnerIssueCommand{Action: "set_status"}, ExecutionID: turn.RequestID, EffectSeq: 2}
	_, err := h.app.Execute(t.Context(), principal, h.env(command.Operation(), protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(issue.Revision)), command)
	if protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("manager shortcut bypassed evidence-bearing endpoints", err)
	}
}

func TestIssueBoardOwnerEndpointPreservesActiveNativeTurn(t *testing.T) {
	for _, status := range []protocol.IssueWorkStatus{protocol.IssueDone, protocol.IssueWorkCancelled} {
		t.Run(string(status), func(t *testing.T) {
			h := hostedManagerHarness(t)
			issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
			turn, _ := startHostedIssueReview(t, h, issue)
			issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "set_status", protocol.SetIssueStatusPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Status: status}))
			if issue.BoardStatus() != status {
				t.Fatal("explicit owner endpoint required a separate source review", issue)
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				actual, err := store.GetMainTurn(t.Context(), tx.Q(), turn.RequestID)
				if err == nil && (actual.Status != store.TurnStarted || actual.ThreadID != turn.ThreadID || actual.TurnID != turn.TurnID) {
					t.Fatal("Issue endpoint rewrote the actual native execution", actual)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			logical, err := h.app.LogicalTurn(t.Context(), turn.RequestID)
			if err != nil || logical.Status == "completed" {
				t.Fatal("Issue endpoint invented native completion", logical, err)
			}
		})
	}
}

func TestIssueBoardOwnerEndpointKeepsRunnerResourcesUntilStopped(t *testing.T) {
	for _, status := range []protocol.IssueWorkStatus{protocol.IssueDone, protocol.IssueWorkCancelled} {
		t.Run(string(status), func(t *testing.T) {
			h, _, issue, as, _ := reviewManagerAssignment(t)
			before := h.run(as.RunID)
			issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "set_status", protocol.SetIssueStatusPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Status: status}))
			if actual := h.run(as.RunID); actual.ObservedState != before.ObservedState || actual.ObservedState == protocol.RunFinished {
				t.Fatal("Issue endpoint released a running owner", actual)
			}
			if h.countWhere(`SELECT COUNT(*) FROM leases WHERE run_id=? AND status='active'`, as.RunID) != 1 {
				t.Fatal("Issue endpoint released the physical execution lease")
			}
			stopOwnerClaim(t, h, as)
			if h.countWhere(`SELECT COUNT(*) FROM leases WHERE run_id=? AND status='released'`, as.RunID) != 1 || h.run(as.RunID).ObservedState != protocol.RunFailed {
				t.Fatal("original stopped receipt did not release the owner execution")
			}
		})
	}
}

func TestIssueBoardBlockedMoveIsAtomicAndReturnsFacts(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	plan := issue.Plan
	plan.Stages = []protocol.IssueStage{{StageID: "investigate", Title: "Investigation", Status: protocol.WorkItemStageActive}}
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "update", protocol.UpdateIssuePayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Plan: plan}))
	before := canonicalGuardBusinessSnapshot(t, h)
	op := canonicalLifecycleControl(t, h, issue, "set_status", protocol.SetIssueStatusPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Status: protocol.IssueDone})
	expectWorkCode(t, op, protocol.CodeInvalidTransition)
	if !bytes.Equal(before, canonicalGuardBusinessSnapshot(t, h)) {
		t.Fatal("blocked move leaked writes")
	}
	var detail struct {
		Blockers []protocol.IssueBlocker `json:"blockers"`
	}
	if err := json.Unmarshal(op.Error.Details, &detail); err != nil || len(detail.Blockers) != 1 || detail.Blockers[0].ObjectID != "investigate" {
		t.Fatal(detail, err)
	}
}

func TestIssueBoardDebounceResetsCancelsAndIsolates(t *testing.T) {
	h := hostedManagerHarness(t)
	a := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	b := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	a = moveBoardIssue(t, h, a, protocol.IssueInProgress)
	b = moveBoardIssue(t, h, b, protocol.IssueInProgress)
	first, _ := statusNotice(t, h, a.IssueID)
	h.clock.Advance(2 * time.Minute)
	a = moveBoardIssue(t, h, a, protocol.IssueInReview)
	latest, _ := statusNotice(t, h, a.IssueID)
	if latest.Due-first.Due != int64((2*time.Minute)/time.Millisecond) {
		t.Fatal(first, latest)
	}
	h.clock.Advance(time.Minute)
	sweepStatus(t, h)
	if statusSourceCount(t, h, true) != 1 {
		t.Fatal("other Issue did not become due independently")
	}
	a = moveBoardIssue(t, h, a, protocol.IssueTodo)
	if _, ok := statusNotice(t, h, a.IssueID); ok {
		t.Fatal("return to baseline retained notification")
	}
	_ = b
}

func TestIssueBoardNotificationRecoveryQueuedReplacementAndFrozenBoundary(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	issue = moveBoardIssue(t, h, issue, protocol.IssueInProgress)
	// Recreating the app loses all process-local state; the window remains due.
	h.app = New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(h.app.hostedConfiguration))
	h.clock.Advance(3 * time.Minute)
	sweepStatus(t, h)
	sweepStatus(t, h)
	if statusSourceCount(t, h, true) != 1 {
		t.Fatal("recovery duplicated notification")
	}
	issue = moveBoardIssue(t, h, issue, protocol.IssueInReview)
	if statusSourceCount(t, h, true) != 0 {
		t.Fatal("unfrozen notification survived newer drag")
	}
	h.clock.Advance(3 * time.Minute)
	sweepStatus(t, h)
	if statusSourceCount(t, h, true) != 1 {
		t.Fatal("replacement missing")
	}
	// Claiming the existing batch freezes its input before native dispatch.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE background_batches SET status='frozen' WHERE work_item_id=? AND status='pending'`, issue.IssueID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	issue = moveBoardIssue(t, h, issue, protocol.IssueTodo)
	if statusSourceCount(t, h, true) != 1 {
		t.Fatal("frozen input was modified")
	}
	h.clock.Advance(3 * time.Minute)
	sweepStatus(t, h)
	if statusSourceCount(t, h, true) != 2 {
		t.Fatal("later generation lost")
	}
}

func TestIssueBoardSettingsRecalculatePersistedWindow(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	issue = moveBoardIssue(t, h, issue, protocol.IssueInProgress)
	h.clock.Advance(2 * time.Minute)
	c := SetIssueNotificationSettings{}
	env := h.env(c.Operation(), protocol.Target{Type: "issue_settings"}, protocol.SetIssueNotificationSettingsPayload{DelayMinutes: 1}, rev(0))
	op := h.exec(owner, env, c)
	if op.Status != protocol.OpSucceeded || statusSourceCount(t, h, true) != 1 {
		t.Fatal(op)
	}
	expectWorkCode(t, h.exec(owner, env, c), protocol.CodeStaleRevision)
	if statusSourceCount(t, h, true) != 1 {
		t.Fatal("stale settings write repeated notification")
	}
	expectWorkCode(t, h.exec(owner, h.env(c.Operation(), env.Target, protocol.SetIssueNotificationSettingsPayload{DelayMinutes: 0}, rev(0)), c), protocol.CodeStaleRevision)
	if result := h.exec(owner, h.env(c.Operation(), env.Target, protocol.SetIssueNotificationSettingsPayload{DelayMinutes: 0}, rev(1)), c); result.Status != protocol.OpSucceeded {
		t.Fatal(result)
	}
	moveBoardIssue(t, h, issue, protocol.IssueInReview)
	if statusSourceCount(t, h, true) != 1 {
		t.Fatal("immediate mode failed to replace queued notification")
	}
}

func TestIssueBoardCountsPaginationRootScopeAndStatusProjection(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 3; i++ {
		moveBoardIssue(t, h, createCanonicalOwnerTestIssue(t, h), protocol.IssueInProgress)
	}
	parent := createCanonicalOwnerTestIssue(t, h)
	child := protocol.CreateIssuePayload{ParentIssueID: parent.IssueID, ExpectedParentRevision: &parent.Revision, Plan: protocol.IssuePlan{Title: "Child", Project: "default", Intent: "Independent child"}}
	canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, child, nil), CanonicalOwnerIssueCommand{Action: "create"}))
	closed := moveBoardIssue(t, h, createCanonicalOwnerTestIssue(t, h), protocol.IssueDone)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		filter := views.CanonicalIssueFilter{Board: true, Project: "default", BoardStatus: protocol.IssueInProgress, Limit: 2, IncludeClosed: true}
		page, err := views.CanonicalIssues(t.Context(), tx.Q(), filter)
		if err != nil {
			return err
		}
		if len(page.Items) != 2 || !page.More || page.Counts[protocol.IssueInProgress] != 3 || page.Counts[protocol.IssueTodo] != 1 || page.Counts[protocol.IssueDone] != 1 {
			t.Fatalf("page=%+v", page)
		}
		filter.After = page.Next
		page, err = views.CanonicalIssues(t.Context(), tx.Q(), filter)
		if err != nil {
			return err
		}
		if len(page.Items) != 1 || page.More || page.Counts[protocol.IssueInProgress] != 3 {
			t.Fatal(page)
		}
		filter.After = ""
		filter.BoardStatus = protocol.IssueDone
		page, err = views.CanonicalIssues(t.Context(), tx.Q(), filter)
		if err == nil && (len(page.Items) != 1 || page.Items[0].IssueID != closed.IssueID || page.Items[0].Board.Status != protocol.IssueDone) {
			t.Fatal(page)
		}
		if err != nil {
			return err
		}
		filter.IncludeClosed = false
		filter.State = protocol.IssueClosed
		page, err = views.CanonicalIssues(t.Context(), tx.Q(), filter)
		if err == nil && (len(page.Items) != 1 || page.Counts[protocol.IssueDone] != 1) {
			t.Fatal("explicit endpoint filter was hidden by the default", page)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIssueBoardConcurrentSweepAndAssignmentHandoff(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	issue = moveBoardIssue(t, h, issue, protocol.IssueInProgress)
	h.clock.Advance(3 * time.Minute)
	c := SweepIssueStatusNotifications{}
	first := h.env(c.Operation(), protocol.Target{Type: "issue_settings"}, struct{}{}, nil)
	second := h.env(c.Operation(), protocol.Target{Type: "issue_settings"}, struct{}{}, nil)
	errors := make(chan error, 2)
	for _, env := range []*protocol.Envelope{first, second} {
		go func() {
			op, err := h.app.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalSystem}, env, c)
			if err == nil && op.Error != nil {
				err = op.Error
			}
			errors <- err
		}()
	}
	for range 2 {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	if statusSourceCount(t, h, true) != 1 {
		t.Fatal("concurrent sweep duplicated input")
	}
	issue = moveBoardIssue(t, h, issue, protocol.IssueInReview)
	p := protocol.IssueAssignmentPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: owner.Subject}, Reason: "Return responsibility to owner"}
	issue = canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.assign", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(issue.Revision)), HostedIssueAssignment{}))
	h.clock.Advance(3 * time.Minute)
	sweepStatus(t, h)
	if statusSourceCount(t, h, true) != 0 {
		t.Fatal("old manager received notification during handoff")
	}
	finish := protocol.FinishIssueAssignmentPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, HandoffID: issue.PendingHandoffID, Reason: "Previous manager has no execution"}
	canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.finish_assignment", protocol.Target{Type: "issue", ID: issue.IssueID}, finish, rev(issue.Revision)), HostedIssueAssignment{Finish: true}))
	h.clock.Advance(time.Minute)
	sweepStatus(t, h)
	if _, ok := statusNotice(t, h, issue.IssueID); ok {
		t.Fatal("owner assignment retained a stale manager notification")
	}
}

func TestIssueBoardUndoPreservesInitialAssignmentWake(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	assignmentPending := func() int {
		var count int
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			return tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM background_batches b JOIN outbox o ON json_extract(o.payload_json,'$.message_id')=b.message_id WHERE b.group_key=? AND b.status='pending' AND o.status='pending'`, "work_item:"+issue.IssueID).Scan(&count)
		}); err != nil {
			t.Fatal(err)
		}
		return count
	}
	if assignmentPending() != 1 {
		t.Fatal("initial assignment did not create a wake")
	}
	issue = moveBoardIssue(t, h, issue, protocol.IssueInProgress)
	h.clock.Advance(3 * time.Minute)
	sweepStatus(t, h)
	if assignmentPending() != 1 || statusSourceCount(t, h, true) != 1 {
		t.Fatal("status notification replaced the independent assignment input")
	}
	moveBoardIssue(t, h, issue, protocol.IssueTodo)
	if assignmentPending() != 1 || statusSourceCount(t, h, true) != 0 {
		t.Fatal("undo retired the independent initial assignment wake")
	}
}

func TestIssueBoardUndoAfterOwnerHandoffDoesNotNotifyPlanner(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	issue = moveBoardIssue(t, h, issue, protocol.IssueInProgress)
	h.clock.Advance(3 * time.Minute)
	sweepStatus(t, h)
	p := protocol.IssueAssignmentPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: owner.Subject}, Reason: "Owner takes responsibility"}
	issue = canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.assign", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(issue.Revision)), HostedIssueAssignment{}))
	finish := protocol.FinishIssueAssignmentPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, HandoffID: issue.PendingHandoffID, Reason: "Previous manager has no execution"}
	issue = canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.finish_assignment", protocol.Target{Type: "issue", ID: issue.IssueID}, finish, rev(issue.Revision)), HostedIssueAssignment{Finish: true}))
	moveBoardIssue(t, h, issue, protocol.IssueTodo)
	if _, ok := statusNotice(t, h, issue.IssueID); ok {
		t.Fatal("undo after handoff did not cancel its window")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var pending int
		err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM background_sources s JOIN background_batches b USING(batch_id) WHERE s.kind='issue.status_changed' AND s.cancelled_at=0 AND b.status='pending' AND json_extract(s.data_json,'$.issue_id')=?`, issue.IssueID).Scan(&pending)
		if err == nil && pending != 0 {
			t.Fatal("stale state was redirected to the personal planner")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
