package app

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func progressChild(t *testing.T, h *harness, parent protocol.Issue, title string) protocol.Issue {
	t.Helper()
	p := canonicalOwnerIssueCreate{Plan: protocol.IssuePlan{Title: title, Project: "default", Intent: "Independent responsibility", Stages: []protocol.IssueStage{}}, ParentIssueID: parent.IssueID, ExpectedParentRevision: rev(parent.Revision)}
	return admitExistingIssueFixture(t, h, canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, p, nil), CanonicalOwnerIssueCommand{Action: "create"})))
}

func TestIssueRecordingChildKeepsParentTodoAndReplaysCreation(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	turn, principal := startHostedIssueReview(t, h, issue)
	if current := canonicalTaskIssue(t, h, issue.IssueID); current.BoardStatus() != protocol.IssueTodo {
		t.Fatal("a coordinator claim started business work", current)
	}
	p := canonicalOwnerIssueCreate{Plan: protocol.IssuePlan{Title: "Investigate", Project: "default", Intent: "Produce findings", Stages: []protocol.IssueStage{}}, ParentIssueID: issue.IssueID, ExpectedParentRevision: rev(issue.Revision)}
	command := HostedCoordinationCommand{Command: CanonicalOwnerIssueCommand{Action: "create"}, ExecutionID: turn.RequestID, EffectSeq: 1}
	env := h.env(command.Operation(), protocol.Target{Type: "issue"}, p, nil)
	op := h.exec(principal, env, command)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	current := canonicalTaskIssue(t, h, issue.IssueID)
	if current.BoardStatus() != protocol.IssueTodo || current.Revision != issue.Revision {
		t.Fatal("recording a backlog child started parent work", current)
	}
	replay := h.exec(principal, env, command)
	if !bytes.Equal(op.Result, replay.Result) || canonicalTaskIssue(t, h, issue.IssueID).Revision != current.Revision {
		t.Fatal("replay repeated the parent transition")
	}
	if _, pending := statusNotice(t, h, issue.IssueID); pending {
		t.Fatal("delegation caused its own status wakeup")
	}
}

func TestIssueExecutionStartsAncestorsAndKeepsChildExecutionSeparate(t *testing.T) {
	h, runtime, issue := issueOwnerHarness(t)
	parent := createCanonicalOwnerTestIssue(t, h)
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "reparent", struct {
		canonicalOwnerControl
		ParentIssueID string `json:"parent_issue_id"`
	}{canonicalOwnerControl{ExpectedAssignmentEpoch: issue.AssignmentEpoch}, parent.IssueID}))
	op := createOwnerExecution(t, h, issue, "implementer")
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	ownerWorkerClaim(t, h, runtime, protocol.RoleImplement)
	if current := canonicalTaskIssue(t, h, parent.IssueID); current.BoardStatus() != protocol.IssueInProgress {
		t.Fatal("ancestor stayed in todo", current)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		card, err := views.CanonicalIssues(t.Context(), tx.Q(), views.CanonicalIssueFilter{Board: true, ReadRoot: parent.IssueID, Limit: 10})
		if err != nil {
			return err
		}
		board := card.Items[0].Board
		if board.Execution.Running != 0 || board.Execution.Starting != 0 || board.Children == nil || board.Children.Total != 1 || board.Children.Items[0].IssueID != issue.IssueID || board.Children.Items[0].Execution.Starting != 1 {
			t.Fatal("parent and child execution scopes mixed", board)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIssueChildrenPreviewCountsTerminalKindsAndExcludesGrandchildren(t *testing.T) {
	h := newHarness(t)
	parent := createCanonicalOwnerTestIssue(t, h)
	first := progressChild(t, h, parent, "Finished")
	moveBoardIssue(t, h, first, protocol.IssueDone)
	second := progressChild(t, h, parent, "Cancelled")
	moveBoardIssue(t, h, second, protocol.IssueWorkCancelled)
	third := progressChild(t, h, parent, "Working")
	third = moveBoardIssue(t, h, third, protocol.IssueInProgress)
	progressChild(t, h, third, "Grandchild")
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		view, err := views.CanonicalIssue(t.Context(), tx.Q(), parent.IssueID, 0)
		if err != nil {
			return err
		}
		c := view.Children
		if c == nil || c.Total != 3 || c.Done != 1 || c.Cancelled != 1 || c.Running != 0 || len(c.Items) != 2 || c.Items[0].IssueID != third.IssueID {
			t.Fatalf("child projection = %+v", c)
		}
		history, err := views.CanonicalIssue(t.Context(), tx.Q(), parent.IssueID, 1)
		if history.Children != nil {
			t.Fatal("historical snapshot acquired current children")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIssueWorkStartPreservesExplicitParentBoundaries(t *testing.T) {
	for _, boundary := range []string{"backlog", "in_review", "paused", "handoff", "closed"} {
		t.Run(boundary, func(t *testing.T) {
			h := newHarness(t)
			parent := createCanonicalOwnerTestIssue(t, h)
			child := progressChild(t, h, parent, "Execution child")
			switch boundary {
			case "backlog", "in_review":
				parent = moveBoardIssue(t, h, parent, protocol.IssueWorkStatus(boundary))
			case "paused":
				parent = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, parent, "pause", canonicalOwnerControl{ExpectedAssignmentEpoch: parent.AssignmentEpoch}))
			}
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				if boundary == "handoff" || boundary == "closed" {
					next := parent
					next.Revision++
					if boundary == "handoff" {
						next.AssignmentState = "handoff"
						next.PendingAssignee = &protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: owner.Subject}
						next.PendingHandoffID = "handoff-test"
						next.AssignmentEpoch++
						previousOwner, _ := json.Marshal(parent.Assignee)
						pendingOwner, _ := json.Marshal(next.PendingAssignee)
						if _, err := tx.Exec(t.Context(), `INSERT INTO issue_assignment_handoffs(handoff_id,issue_id,from_epoch,to_epoch,from_assignee_json,target_assignee_json,status,obligations_json,request_id,reason,created_at,updated_at) VALUES(?,?,?,?,?,?,'fenced','[]',?,'Fixture handoff',?,?)`, next.PendingHandoffID, parent.IssueID, parent.AssignmentEpoch, next.AssignmentEpoch, string(previousOwner), string(pendingOwner), h.ids.New(protocol.PrefixRequest), h.clock.Now().UnixMilli(), h.clock.Now().UnixMilli()); err != nil {
							return err
						}
					} else {
						next.State = protocol.IssueClosed
					}
					if err := tx.SaveCanonicalIssue(t.Context(), next, parent.Revision, store.IssueRevisionProvenance{Actor: owner, RequestID: h.ids.New(protocol.PrefixRequest)}); err != nil {
						return err
					}
					parent = next
				}
				x := &Exec{Env: h.env("issues.create_task", protocol.Target{Type: "issue", ID: child.IssueID}, nil, nil), Principal: owner, Now: h.clock.Now(), IDs: h.ids}
				_, err := markIssueWorkStarted(t.Context(), x, tx, child.IssueID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if actual := canonicalTaskIssue(t, h, parent.IssueID); actual.Revision != parent.Revision || actual.WorkStatus != parent.WorkStatus {
				t.Fatal("work start overwrote parent intervention", actual)
			}
		})
	}
}

func TestIssueOrdinaryExecutionStartsAncestors(t *testing.T) {
	h := newHarness(t)
	parent := createCanonicalOwnerTestIssue(t, h)
	child := progressChild(t, h, parent, "Child execution")
	task := createCanonicalTaskForTest(t, h, child)
	as, ok, _ := h.claim("linux-1")
	if !ok || as.TaskID != task {
		t.Fatal("claim failed", as)
	}
	for _, id := range []string{parent.IssueID, child.IssueID} {
		issue := canonicalTaskIssue(t, h, id)
		if issue.BoardStatus() != protocol.IssueInProgress {
			t.Errorf("claimed execution leaves Issue %s status %s", id, issue.BoardStatus())
		}
	}
}

func TestIssueAcceptanceExecutionAttributedToReviewChild(t *testing.T) {
	h, runtime, issue := issueOwnerHarness(t)
	op := createOwnerExecution(t, h, issue, "implementer")
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	impl := ownerWorkerClaim(t, h, runtime, protocol.RoleImplement)
	if op := h.runAs(impl, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.runAs(impl, protocol.OpRunsSubmitArtifact, testArtifact(h.task(impl.TaskID).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	accept := ownerWorkerClaim(t, h, runtime, protocol.RoleAccept)
	if op := h.runAs(accept, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		page, err := views.CanonicalIssues(t.Context(), tx.Q(), views.CanonicalIssueFilter{Board: true, ReadRoot: issue.IssueID, Limit: 10})
		if err != nil {
			return err
		}
		b := page.Items[0].Board
		if b.Execution.Accepting != 0 || b.Children.Running != 1 || b.Children.Items[0].IssueID != accept.Responsibility.IssueID || b.Children.Items[0].Execution.Accepting != 1 {
			t.Error("active review child missing acceptance execution")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIssueBlockedAcceptanceStaysWithReviewChild(t *testing.T) {
	h, runtime, issue := issueOwnerHarness(t)
	if op := createOwnerExecution(t, h, issue, "implementer"); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	impl := ownerWorkerClaim(t, h, runtime, protocol.RoleImplement)
	if op := h.runAs(impl, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.runAs(impl, protocol.OpRunsSubmitArtifact, testArtifact(h.task(impl.TaskID).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	accept := ownerWorkerClaim(t, h, runtime, protocol.RoleAccept)
	if op := h.runAs(accept, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictBlocked}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		page, err := views.CanonicalIssues(t.Context(), tx.Q(), views.CanonicalIssueFilter{Board: true, ReadRoot: issue.IssueID, Limit: 10})
		if err != nil {
			return err
		}
		b := page.Items[0].Board
		if b.Children.Items[0].Execution.WaitingSupervisor != 1 || b.Children.Items[0].Execution.WaitingOwner != 0 || b.Execution.WaitingSupervisor != 0 || b.Execution.WaitingOwner != 0 {
			t.Error("blocked acceptance moved from explicit review responsibility to parent")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
