package app

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func TestIssueBoardProjectsCurrentOwnerExecutionAcrossPhysicalWait(t *testing.T) {
	h, runtime := managerClaimHarness(t)
	issue := createBacklogTestIssue(t, h)
	other := createCanonicalOwnerTestIssue(t, h)
	op := h.exec(owner, h.env(protocol.OpIssueRequestManager, protocol.Target{Type: "issue", ID: issue.IssueID},
		protocol.RequestIssueManagerPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Own this Issue"}, rev(issue.Revision)), RequestIssueManager{})
	issue = canonicalLifecycleIssue(t, op)
	issue = moveBoardIssue(t, h, issue, protocol.IssueTodo)

	var lastSeq int64
	check := func(phase string, want protocol.ExecutionCounts, event string, column protocol.IssueBoardColumn) {
		t.Helper()
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			page, err := views.CanonicalIssues(t.Context(), tx.Q(), views.CanonicalIssueFilter{Board: true, BoardColumn: "all"})
			if err != nil {
				return err
			}
			for _, card := range page.Items {
				switch card.IssueID {
				case issue.IssueID:
					if card.Board.Column != column || card.Board.WaitHint != "" {
						t.Errorf("%s: board column %s / %s, want %s without a hint", phase, card.Board.Column, card.Board.WaitHint, column)
					}
					if card.Board.Execution != want {
						t.Errorf("%s: board counts = %+v, want %+v", phase, card.Board.Execution, want)
					}
				case other.IssueID:
					if card.Board.Execution != (protocol.ExecutionCounts{}) {
						t.Errorf("%s: unrelated Issue counted this owner's Run: %+v", phase, card.Board.Execution)
					}
				}
			}
			if len(page.Items) != 2 || page.FactsSeq <= lastSeq {
				t.Errorf("%s: board result or watermark is stale: %+v, previous seq %d", phase, page, lastSeq)
			}
			if event != "" {
				var found bool
				if err := tx.Q().QueryRowContext(t.Context(), `
					SELECT EXISTS(SELECT 1 FROM events WHERE event_seq>? AND event_seq<=? AND kind=?)`,
					lastSeq, page.FactsSeq, event).Scan(&found); err != nil {
					return err
				}
				if !found {
					t.Errorf("%s: no %s event before board watermark %d", phase, event, page.FactsSeq)
				}
			}
			lastSeq = page.FactsSeq
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}

	check("before carrier creation", protocol.ExecutionCounts{Queued: 1}, "", protocol.IssueColumnWaiting)
	as := ownerClaim(t, h, runtime)
	if as.Role != protocol.RoleIssueManager || as.Manager == nil || as.Manager.IssueID != issue.IssueID {
		t.Fatal(as)
	}
	check("claimed", protocol.ExecutionCounts{Starting: 1}, "run.assigned", protocol.IssueColumnWaiting)
	startBoardManager(t, h, as)
	check("native command claimed", protocol.ExecutionCounts{Running: 1}, "run.manager_started", protocol.IssueColumnWIP)
	issue = moveBoardIssue(t, h, canonicalTaskIssue(t, h, issue.IssueID), protocol.IssueInReview)
	dependency := createCanonicalTaskForTest(t, h, issue)
	check("ordinary work queued while owner runs", protocol.ExecutionCounts{Running: 1, Queued: 1}, "", protocol.IssueColumnWIP)

	worker := ownerClaim(t, h, runtime)
	if worker.TaskID != dependency || worker.Role != protocol.RoleImplement {
		t.Fatal(worker)
	}
	if op := h.runAs(worker, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	check("owner and worker running", protocol.ExecutionCounts{Running: 2}, "", protocol.IssueColumnWIP)
	if _, err := h.app.RecordRunEvents(t.Context(), runPrincipal(worker.RunID, worker.Role), worker.RunID,
		[]protocol.RunEvent{{Seq: 1, Kind: protocol.EventRunWaiting}}); err != nil {
		t.Fatal(err)
	}
	check("worker waits for its own command", protocol.ExecutionCounts{Running: 2}, "run.observed", protocol.IssueColumnWIP)
	const waitDeadline = 6 * time.Second
	wait := waitForTask(t, h, as, dependency, h.clock.Now().Add(waitDeadline).UnixMilli())
	check("stop requested while both are still live", protocol.ExecutionCounts{Running: 2}, "run.external_wait_requested", protocol.IssueColumnWIP)
	stopOwnerClaim(t, h, as)
	if h.lease(as.RunID).Status != store.LeaseReleased {
		t.Fatal("owner did not release its slot")
	}
	check("owner physically stopped", protocol.ExecutionCounts{WaitingExternal: 1, Running: 1}, "run.external_wait_yielded", protocol.IssueColumnWIP)

	h.clock.Advance(waitDeadline)
	if err := h.app.SweepExternalWaits(t.Context()); err != nil {
		t.Fatal(err)
	}
	check("original wait requeued", protocol.ExecutionCounts{Queued: 1, Running: 1}, "task.external_wait_ready", protocol.IssueColumnWIP)
	setMode := func(mode protocol.ControlMode) {
		t.Helper()
		op := h.exec(owner, h.env("control.set_mode", taskTarget(as.TaskID), protocol.SetControlModePayload{Mode: mode}, rev(h.task(as.TaskID).ControlRevision)), SetControlMode{})
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	setMode(protocol.ControlPaused)
	check("paused owner action cannot be claimed", protocol.ExecutionCounts{Paused: 1, Running: 1}, "", protocol.IssueColumnWIP)
	setMode(protocol.ControlManaged)
	check("owner action can be claimed again", protocol.ExecutionCounts{Queued: 1, Running: 1}, "", protocol.IssueColumnWIP)
	next := ownerClaim(t, h, runtime)
	if next.RunID == as.RunID || next.TaskID != as.TaskID || next.ExternalWaitContinuation == nil || next.ExternalWaitContinuation.WaitID != wait.WaitID {
		t.Fatal("wrong owner successor", next)
	}
	check("successor claimed", protocol.ExecutionCounts{Starting: 1, Running: 1}, "run.assigned", protocol.IssueColumnWIP)
	startBoardManager(t, h, next)
	check("successor running", protocol.ExecutionCounts{Running: 2}, "run.manager_started", protocol.IssueColumnWIP)

	artifact := testArtifact(h.task(dependency).WorkspaceName)
	if op := h.runAs(worker, protocol.OpRunsSubmitArtifact, artifact, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.task(dependency).Lifecycle != protocol.TaskAwaitingAcceptance {
		t.Fatal("ordinary Task did not queue its independent acceptance")
	}
	check("independent acceptance queued while owner runs", protocol.ExecutionCounts{Running: 1, Queued: 1}, "", protocol.IssueColumnWIP)
}

func startBoardManager(t *testing.T, h *harness, as protocol.Assignment) {
	t.Helper()
	id, err := h.app.QueueManagerNative(t.Context(), as.RunID, "start", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	claim := h.runAs(as, protocol.OpManagerNativeClaim, map[string]any{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim})
	var request protocol.ManagerNativeRequest
	if claim.Status != protocol.OpSucceeded || json.Unmarshal(claim.Result, &request) != nil || request.ID != id {
		t.Fatal(claim)
	}
	if op := h.runAs(as, protocol.OpManagerNativeReply, protocol.ManagerNativeReply{ID: id, Method: "start", Result: json.RawMessage(`{"ID":"fixture-owner-thread"}`)}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}

func TestIssueBoardKeepsOwnerExecutionUntilTerminalStop(t *testing.T) {
	for _, endpoint := range []struct {
		name   string
		action string
		status protocol.IssueWorkStatus
		state  protocol.IssueState
	}{
		{"done", "set_status", protocol.IssueDone, protocol.IssueClosed},
		{"cancelled column", "set_status", protocol.IssueWorkCancelled, protocol.IssueCancelled},
		{"cancel", "cancel", protocol.IssueWorkCancelled, protocol.IssueCancelled},
	} {
		t.Run(endpoint.name, func(t *testing.T) {
			h, _, issue, assignment, _ := reviewManagerAssignment(t)
			check := func(phase string, want int) {
				t.Helper()
				if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
					for _, board := range []bool{false, true} {
						page, err := views.CanonicalIssues(t.Context(), tx.Q(), views.CanonicalIssueFilter{
							Board: board, IncludeClosed: true, ReadRoot: issue.IssueID,
							BoardColumn: map[bool]protocol.IssueBoardColumn{true: "all"}[board],
						})
						if err != nil {
							return err
						}
						if len(page.Items) != 1 || page.Items[0].IssueID != issue.IssueID {
							t.Fatalf("%s: board=%t did not return this Issue: %+v", phase, board, page)
						}
						row := page.Items[0]
						counts := row.Execution
						if board && row.Board != nil {
							counts = &row.Board.Execution
							if issue.State == endpoint.state && string(row.Board.Column) != string(endpoint.status) {
								t.Errorf("%s: terminal Issue column = %s, want %s", phase, row.Board.Column, endpoint.status)
							}
						}
						if counts == nil || counts.Starting != want || counts.Queued != 0 || counts.WaitingExternal != 0 {
							t.Errorf("%s: board=%t issue=%s execution=%+v, want %d starting and no continuation", phase, board, row.State, counts, want)
						}
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}

			check("claimed", 1)
			var payload any = protocol.SetIssueStatusPayload{
				ExpectedAssignmentEpoch: issue.AssignmentEpoch, Status: endpoint.status,
			}
			if endpoint.action == "cancel" {
				payload = protocol.IssueControlPayload{
					ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Cancel settled responsibility",
				}
			}
			issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, endpoint.action, payload))
			if issue.State != endpoint.state || h.run(assignment.RunID).ObservedState != protocol.RunStarting ||
				h.lease(assignment.RunID).Status != store.LeaseActive {
				t.Fatal("terminal Issue must retain its original Run and lease until the stop receipt")
			}
			check("terminal before stop receipt", 1)

			stopOwnerClaim(t, h, assignment)
			if h.run(assignment.RunID).ObservedState != protocol.RunFailed || h.lease(assignment.RunID).Status != store.LeaseReleased {
				t.Fatal("physical stop must retire the original Run and release its lease")
			}
			check("after stop receipt", 0)
		})
	}
}

func TestIssueExecutionBoardWaitsForCISlotReleaseAndOwnerRestart(t *testing.T) {
	for _, order := range []string{"owner only", "parallel run stops before CI", "CI ends before parallel run stops"} {
		t.Run(order, func(t *testing.T) { testIssueExecutionBoardCI(t, order) })
	}
}

func testIssueExecutionBoardCI(t *testing.T, order string) {
	h, runtime := managerClaimHarness(t)
	delivery := protocol.IssueDelivery{DeliveryID: "board-ci", Provider: "github", Repository: "example/project", Number: 7}
	payload := protocol.CreateIssuePayload{Plan: protocol.IssuePlan{
		Title: "Observe CI", Project: "default", Intent: "Resume with the same owner", Deliveries: []protocol.IssueDelivery{delivery},
	}}
	issue := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, payload, nil), CanonicalOwnerIssueCommand{Action: "create"}))
	check := func(phase string, column protocol.IssueBoardColumn, hint protocol.IssueBoardWaitHint) {
		t.Helper()
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			page, err := views.CanonicalIssues(t.Context(), tx.Q(), views.CanonicalIssueFilter{
				Board: true, BoardColumn: column, ReadRoot: issue.IssueID, Limit: 1,
			})
			if err != nil {
				return err
			}
			if len(page.Items) != 1 || len(page.ColumnCounts) != 5 || page.ColumnCounts[column] != 1 {
				t.Fatalf("%s: expected %s, got %+v", phase, column, page)
			}
			if got := page.Items[0].Board; got.Column != column || got.WaitHint != hint {
				t.Fatalf("%s: got %s / %s, want %s / %s", phase, got.Column, got.WaitHint, column, hint)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	check("recorded backlog", protocol.IssueColumnBacklog, "")
	issue = canonicalLifecycleIssue(t, h.exec(owner, h.env(protocol.OpIssueRequestManager, protocol.Target{Type: "issue", ID: issue.IssueID},
		protocol.RequestIssueManagerPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Track the candidate"}, rev(issue.Revision)), RequestIssueManager{}))
	issue = moveBoardIssue(t, h, issue, protocol.IssueTodo)
	check("admitted", protocol.IssueColumnWaiting, "")
	assignment := ownerClaim(t, h, runtime)
	check("claimed, native process not started", protocol.IssueColumnWaiting, "")
	startBoardManager(t, h, assignment)
	check("owner running", protocol.IssueColumnWIP, "")

	snapshot := func(state string) {
		t.Helper()
		if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
			_, err := tx.PutDeliveryObservationFact(t.Context(), delivery, protocol.DeliveryObservation{
				DeliverySnapshot: protocol.DeliverySnapshot{State: "open", HeadSHA: "candidate", BaseSHA: "base", TargetSHA: "target", CI: &protocol.DeliveryCI{HeadSHA: "candidate", State: state}},
				CheckedAt:        h.clock.Now().UnixMilli(),
			})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot("pending")
	p := protocol.WaitExternalPayload{WaitKey: "ci", Kind: "checks", IssueID: issue.IssueID, TargetID: delivery.DeliveryID, Reason: "Await provider result", Recovery: "Inspect the check result"}
	op := h.runAs(assignment, protocol.OpRunsWaitExternal, p, WaitExternal{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	check("stop requested", protocol.IssueColumnWIP, "")
	if h.lease(assignment.RunID).Status != store.LeaseActive {
		t.Fatal("the external wait released an unconfirmed process")
	}
	stopOwnerClaim(t, h, assignment)
	check("stopped and pending checks", protocol.IssueColumnWaiting, protocol.IssueBoardWaitCI)
	if h.lease(assignment.RunID).Status != store.LeaseReleased {
		t.Fatal("the original stop receipt did not release the lease")
	}

	if order != "owner only" {
		h.clock.Advance(time.Millisecond)
		current := canonicalTaskIssue(t, h, issue.IssueID)
		cmd := CanonicalOwnerTaskCreate{}
		task := canonicalOwnerTaskPayload{
			ExpectedAssignmentEpoch: current.AssignmentEpoch,
			Title:                   "Parallel implementation",
			Spec:                    testSpec,
			Rating:                  protocol.RatingP1,
			IndependentAcceptance:   &protocol.IndependentAcceptanceRequest{Required: false, Reason: "Complete the parallel fixture without a second Run"},
		}
		if op := h.exec(owner, h.env(cmd.Operation(), protocol.Target{Type: "issue", ID: issue.IssueID}, task, rev(current.Revision)), cmd); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		other := ownerClaim(t, h, runtime)
		if other.Role != protocol.RoleImplement || h.run(other.RunID).ObservedState != protocol.RunStarting {
			t.Fatal(other)
		}
		check("parallel worker claimed without starting", protocol.IssueColumnWaiting, protocol.IssueBoardWaitCI)
		if op := h.runAs(other, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		check("parallel worker running", protocol.IssueColumnWIP, "")
		if order == "CI ends before parallel run stops" {
			h.clock.Advance(time.Millisecond)
			snapshot("success")
			check("CI finished while parallel worker is live", protocol.IssueColumnWIP, "")
		}
		h.clock.Advance(time.Millisecond)
		if op := h.runAs(other, protocol.OpRunsSubmitArtifact, testArtifact(h.task(other.TaskID).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		if h.lease(other.RunID).Status != store.LeaseReleased || h.task(other.TaskID).Lifecycle != protocol.TaskCompleted {
			t.Fatal("parallel Run did not stop and complete")
		}
		if order == "CI ends before parallel run stops" {
			check("parallel worker stops after CI finished", protocol.IssueColumnWIP, "")
			return
		}
		check("parallel worker stops while CI is pending", protocol.IssueColumnWaiting, protocol.IssueBoardWaitCI)
	}
	h.clock.Advance(time.Millisecond)
	snapshot("success")
	check("terminal provider result before scheduler sweep", protocol.IssueColumnWaiting, "")
	h.clock.Advance(6 * time.Second)
	if err := h.app.SweepExternalWaits(t.Context()); err != nil {
		t.Fatal(err)
	}
	check("successor pending", protocol.IssueColumnWaiting, "")
	next := ownerClaim(t, h, runtime)
	if next.RunID == assignment.RunID || next.TaskID != assignment.TaskID || next.Execution.Identity != assignment.Execution.Identity ||
		next.ExternalWaitContinuation == nil || next.ExternalWaitContinuation.RunID != assignment.RunID {
		t.Fatalf("CI did not wake the original owner and Task: %+v", next)
	}
	check("successor claimed", protocol.IssueColumnWaiting, "")
	startBoardManager(t, h, next)
	check("successor running", protocol.IssueColumnWIP, "")
}
