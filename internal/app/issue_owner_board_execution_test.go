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
	check := func(phase string, want protocol.ExecutionCounts, event string) {
		t.Helper()
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			page, err := views.CanonicalIssues(t.Context(), tx.Q(), views.CanonicalIssueFilter{Board: true})
			if err != nil {
				return err
			}
			for _, card := range page.Items {
				switch card.IssueID {
				case issue.IssueID:
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
	start := func(as protocol.Assignment) {
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

	check("before carrier creation", protocol.ExecutionCounts{Queued: 1}, "")
	as := ownerClaim(t, h, runtime)
	if as.Role != protocol.RoleIssueManager || as.Manager == nil || as.Manager.IssueID != issue.IssueID {
		t.Fatal(as)
	}
	check("claimed", protocol.ExecutionCounts{Starting: 1}, "run.assigned")
	start(as)
	check("native command claimed", protocol.ExecutionCounts{Running: 1}, "run.manager_started")
	issue = moveBoardIssue(t, h, canonicalTaskIssue(t, h, issue.IssueID), protocol.IssueInReview)
	dependency := createCanonicalTaskForTest(t, h, issue)
	check("ordinary work queued while owner runs", protocol.ExecutionCounts{Running: 1, Queued: 1}, "")

	worker := ownerClaim(t, h, runtime)
	if worker.TaskID != dependency || worker.Role != protocol.RoleImplement {
		t.Fatal(worker)
	}
	if op := h.runAs(worker, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	check("owner and worker running", protocol.ExecutionCounts{Running: 2}, "")
	const waitDeadline = 6 * time.Second
	wait := waitForTask(t, h, as, dependency, h.clock.Now().Add(waitDeadline).UnixMilli())
	check("stop requested while both are still live", protocol.ExecutionCounts{Running: 2}, "run.external_wait_requested")
	stopOwnerClaim(t, h, as)
	if h.lease(as.RunID).Status != store.LeaseReleased {
		t.Fatal("owner did not release its slot")
	}
	check("owner physically stopped", protocol.ExecutionCounts{WaitingExternal: 1, Running: 1}, "run.external_wait_yielded")

	h.clock.Advance(waitDeadline)
	if err := h.app.SweepExternalWaits(t.Context()); err != nil {
		t.Fatal(err)
	}
	check("original wait requeued", protocol.ExecutionCounts{Queued: 1, Running: 1}, "task.external_wait_ready")
	setMode := func(mode protocol.ControlMode) {
		t.Helper()
		op := h.exec(owner, h.env("control.set_mode", taskTarget(as.TaskID), protocol.SetControlModePayload{Mode: mode}, rev(h.task(as.TaskID).ControlRevision)), SetControlMode{})
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	setMode(protocol.ControlPaused)
	check("paused owner action cannot be claimed", protocol.ExecutionCounts{Paused: 1, Running: 1}, "")
	setMode(protocol.ControlManaged)
	check("owner action can be claimed again", protocol.ExecutionCounts{Queued: 1, Running: 1}, "")
	next := ownerClaim(t, h, runtime)
	if next.RunID == as.RunID || next.TaskID != as.TaskID || next.ExternalWaitContinuation == nil || next.ExternalWaitContinuation.WaitID != wait.WaitID {
		t.Fatal("wrong owner successor", next)
	}
	check("successor claimed", protocol.ExecutionCounts{Starting: 1, Running: 1}, "run.assigned")
	start(next)
	check("successor running", protocol.ExecutionCounts{Running: 2}, "run.manager_started")

	artifact := testArtifact(h.task(dependency).WorkspaceName)
	if op := h.runAs(worker, protocol.OpRunsSubmitArtifact, artifact, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.task(dependency).Lifecycle != protocol.TaskAwaitingAcceptance {
		t.Fatal("ordinary Task did not queue its independent acceptance")
	}
	check("independent acceptance queued while owner runs", protocol.ExecutionCounts{Running: 1, Queued: 1}, "")
}
