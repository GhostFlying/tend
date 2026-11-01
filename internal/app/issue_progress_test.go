package app

import (
	"errors"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func TestHostedIssueProgressUsesExactManagerScope(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	turn, principal := startHostedIssueReview(t, h, issue)
	checkIssueProgressManagerScope(t, h, issue, turn, principal)
}

func TestRunnerIssueProgressUsesClaimedManagerScope(t *testing.T) {
	h, _, issue, assignment, _ := reviewManagerAssignment(t)
	turn, _ := startHostedIssueReview(t, h, issue)
	principal := runPrincipal(assignment.RunID, protocol.RoleIssueManager)
	principal.Thread = turn.ThreadID
	principal.ConversationID = assignment.Manager.ConversationID
	checkIssueProgressManagerScope(t, h, issue, turn, principal)
}

func checkIssueProgressManagerScope(t *testing.T, h *harness, issue protocol.Issue, turn store.MainTurn, principal protocol.Principal) {
	t.Helper()
	other := createCanonicalOwnerTestIssue(t, h)
	target := protocol.Target{Type: "issue", ID: issue.IssueID}
	var contextRevision int64
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT revision FROM work_item_contexts WHERE work_item_id=?`, issue.IssueID).Scan(&contextRevision); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO background_assessments(assessment_id,conversation_id,delivery_id,context_revision,disposition,note,evidence_json,created_at) VALUES(?,?,?,?,?,?,?,?)`, "progress-assessment", "issue:"+issue.IssueID, "progress-delivery", contextRevision, "reviewed", "Reviewed source context", "{}", 1)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	payload := protocol.UpdateIssueProgressPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Summary: "Findings reviewed", NextStep: "Delegate acceptance"}
	out := hostedEffect(t, h, principal, turn, 1, target, rev(issue.Revision), payload, UpdateIssueProgress{})
	if out.Status != protocol.OpSucceeded {
		t.Fatal(out)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		current, err := views.CanonicalIssue(t.Context(), tx.Q(), issue.IssueID, 0)
		if err != nil {
			return err
		}
		if current.Conversation == nil || current.Conversation.ConversationID != "issue:"+issue.IssueID || !current.Conversation.CanSend {
			t.Fatal(current.Conversation)
		}
		var stale int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT stale FROM background_assessments WHERE assessment_id=?`, "progress-assessment").Scan(&stale); err != nil {
			return err
		}
		if stale != 0 || current.ContextRevision != contextRevision {
			t.Fatalf("progress invalidated source assessment: stale=%d context=%d want=%d", stale, current.ContextRevision, contextRevision)
		}
		activity, err := views.IssueActivity(t.Context(), tx.Q(), issue.IssueID, 0)
		if err != nil {
			return err
		}
		found := false
		for _, entry := range activity.Items {
			if entry.Block != nil {
				found = true
				if principal.Kind == protocol.PrincipalRun && entry.Author != issue.Assignee.Ref {
					t.Fatal("runner reply lost its frozen author", entry)
				}
				if entry.Block.Trigger.ConversationID != "issue:"+issue.IssueID {
					t.Fatal(entry)
				}
			}
		}
		if !found {
			t.Fatal("admitted manager conversation missing", activity)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	badEnv := h.env("issues.update_progress", protocol.Target{Type: "issue", ID: other.IssueID}, payload, rev(other.Revision))
	_, err := h.app.Execute(t.Context(), principal, badEnv, HostedCoordinationCommand{Command: UpdateIssueProgress{}, ExecutionID: turn.RequestID, EffectSeq: 2})
	var pe *protocol.Error
	if !errors.As(err, &pe) || pe.Code != protocol.CodeForbidden {
		t.Fatalf("cross-issue=%v", err)
	}
	raw := h.env("issues.update_progress", target, payload, rev(issue.Revision+1))
	_, err = h.app.Execute(t.Context(), principal, raw, UpdateIssueProgress{})
	if !errors.As(err, &pe) || pe.Code != protocol.CodeForbidden {
		t.Fatalf("unscoped=%v", err)
	}
}

func TestIssueConversationPendingManagerKeepsActualBinding(t *testing.T) {
	h, runtime := managerClaimHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	request := func(replace bool) {
		t.Helper()
		op := h.exec(owner, h.env(protocol.OpIssueRequestManager, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.RequestIssueManagerPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Replace: replace, Reason: "Delegate continuing responsibility"}, rev(issue.Revision)), RequestIssueManager{})
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	read := func(revision int64) protocol.IssueView {
		t.Helper()
		var out protocol.IssueView
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			var err error
			out, err = views.CanonicalIssue(t.Context(), tx.Q(), issue.IssueID, revision)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return out
	}
	request(false)
	queued := read(0)
	if queued.Manager == nil || queued.Conversation == nil || !queued.Conversation.CanSend || queued.Conversation.Reason != "manager_pending" || queued.Conversation.ConversationID != "issue:"+issue.IssueID {
		t.Fatal("pending manager did not expose its queued conversation", queued)
	}
	if len(queued.Tasks) != 0 || h.task(queued.Manager.TaskID).Kind != protocol.TaskKindIssueManager {
		t.Fatal("management responsibility leaked into execution summaries", queued.Tasks)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		activity, err := views.IssueActivity(t.Context(), tx.Q(), issue.IssueID, 0)
		if err != nil {
			return err
		}
		found := false
		for _, entry := range activity.Items {
			found = found || entry.Kind == "task.manager_requested" && entry.TaskID == queued.Manager.TaskID
		}
		if !found {
			t.Fatal("published responsibility missing from activity", activity)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assignment := reviewClaimManager(t, h, runtime)
	claimed := read(0)
	if claimed.Conversation == nil || !claimed.Conversation.CanSend || claimed.Conversation.ConversationID != assignment.Manager.ConversationID {
		t.Fatal("claim did not expose the actual conversation", claimed)
	}
	issue = claimed.Issue
	request(true)
	fenced := read(0)
	if fenced.Conversation == nil || !fenced.Conversation.CanSend || fenced.Conversation.Reason != "manager_pending" || fenced.Conversation.ConversationID != claimed.Conversation.ConversationID {
		t.Fatal("pending replacement did not preserve the queued conversation", fenced)
	}
	historical := read(1)
	if historical.Manager != nil || historical.Conversation != nil {
		t.Fatal("history leaked current manager facts", historical)
	}
}
