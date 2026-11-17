package app

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func TestIdenticalIssueProgressDoesNotCreateAnotherOwnerWake(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	p := protocol.UpdateIssueProgressPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Summary: "Checking the current candidate", NextStep: "Wait for the existing check"}
	write := func(p protocol.UpdateIssueProgressPayload) protocol.Operation {
		t.Helper()
		i := canonicalTaskIssue(t, h, issue.IssueID)
		return h.exec(owner, h.env("issues.update_progress", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(i.Revision)), UpdateIssueProgress{})
	}
	first := write(p)
	if first.Status != protocol.OpSucceeded {
		t.Fatal(first)
	}
	before := canonicalTaskIssue(t, h, issue.IssueID)
	counts := map[string]int{}
	for _, table := range []string{"events", "outbox", "issue_progress_revisions", "issue_control_revisions"} {
		counts[table] = h.count(table)
	}
	p.Summary = "  " + p.Summary + "  "
	second := write(p)
	var original, current protocol.IssueProgress
	if json.Unmarshal(first.Result, &original) != nil || json.Unmarshal(second.Result, &current) != nil || current != original || second.Status != protocol.OpSucceeded {
		t.Fatal("repeated progress lost its original fact", first, second)
	}
	if canonicalTaskIssue(t, h, issue.IssueID).Revision != before.Revision {
		t.Fatal("unchanged progress advanced the Issue revision")
	}
	for table, count := range counts {
		if h.count(table) != count {
			t.Fatal("unchanged progress produced a new fact or wake", table)
		}
	}
	p.Summary = "Check failed; original external outcome remains unknown"
	p.NextStep = "Reconcile the original operation"
	if op := write(p); op.Status != protocol.OpSucceeded || h.count("issue_progress_revisions") != counts["issue_progress_revisions"]+1 {
		t.Fatal("a real failure/unknown update disappeared", op)
	}
	stale := h.exec(owner, h.env("issues.update_progress", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(before.Revision)), UpdateIssueProgress{})
	if stale.Error == nil || stale.Error.Code != protocol.CodeStaleRevision {
		t.Fatal("deduplication bypassed current revision checks", stale)
	}
}

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
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT revision FROM work_item_contexts WHERE work_item_id=?`, issue.IssueID).Scan(&contextRevision)
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
		if current.ContextRevision != contextRevision {
			t.Fatalf("progress revised the original source context: context=%d want=%d", current.ContextRevision, contextRevision)
		}
		activity, err := views.IssueActivity(t.Context(), tx.Q(), issue.IssueID, 0)
		if err != nil {
			return err
		}
		found := false
		for _, entry := range activity.Items {
			if entry.Block != nil {
				found = true
				// The fixture's frozen inventory calls this execution Alpha;
				// retain its concrete execution reference beside that frozen name.
				if principal.Kind == protocol.PrincipalRun && entry.Author != "Alpha · linux-1/alpha" {
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

func TestIssueConversationPendingSelectionKeepsActualBinding(t *testing.T) {
	h, runtime := managerClaimHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	request := func(replace bool) {
		issue = canonicalTaskIssue(t, h, issue.IssueID)
		op := h.exec(owner, h.env(protocol.OpIssueRequestManager, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.RequestIssueManagerPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Replace: replace, Reason: "Delegate continuing responsibility"}, rev(issue.Revision)), RequestIssueManager{})
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		issue = canonicalTaskIssue(t, h, issue.IssueID)
	}
	read := func(revision int64) protocol.IssueView {
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
	if queued.Manager != nil || queued.QueueReason == nil || queued.Conversation == nil || !queued.Conversation.CanSend || queued.Conversation.Reason != "owner_pending" || h.count("tasks") != 0 {
		t.Fatal("pending selection invented a task or lost its conversation", queued)
	}
	assignment := reviewClaimManager(t, h, runtime)
	claimed := read(0)
	if claimed.Manager == nil || claimed.Manager.TaskID != assignment.TaskID || claimed.Conversation == nil || !claimed.Conversation.CanSend || claimed.Conversation.ConversationID != assignment.Manager.ConversationID {
		t.Fatal("claim did not expose actual responsibility", claimed)
	}
	request(true)
	fenced := read(0)
	if fenced.Conversation == nil || !fenced.Conversation.CanSend || fenced.Conversation.ConversationID != claimed.Conversation.ConversationID {
		t.Fatal("replacement lost queued conversation", fenced)
	}
	historical := read(1)
	if historical.Manager != nil || historical.Conversation != nil {
		t.Fatal("history leaked current manager facts", historical)
	}
}
