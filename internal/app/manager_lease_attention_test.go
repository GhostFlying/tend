package app

import (
	"reflect"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func expiredManagerProblem(t *testing.T, h *harness, as protocol.Assignment) protocol.Attention {
	t.Helper()
	var id string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT attention_id FROM attentions WHERE json_extract(body_json,'$.task_id')=?`, as.TaskID).Scan(&id)
	}); err != nil {
		t.Fatal("manager lease expiry did not create a durable supervisor problem", err)
	}
	return readAttentionForTest(t, h, id)
}

func TestManagerHistoryReadingDoesNotCreateExpiringLeaseOrSupervisorFault(t *testing.T) {
	h, original, pending := stoppedRetainedManager(t)
	payload := receiptReaderPayload(t, h, original)
	if op := h.exec(owner, h.env(protocol.OpReconcileManagerRun, taskTarget(original.TaskID), payload, rev(h.task(original.TaskID).ControlRevision)), ReconcileManagerRun{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	beforeRun, beforeTask, attentions := h.run(original.RunID), h.task(original.TaskID), h.count("attentions")
	h.clock.Advance(DefaultLeaseTTL + time.Second)
	if n, err := h.app.ExpireLeases(t.Context()); err != nil || n != 0 {
		t.Fatal("history read acquired a business lease", n, err)
	}
	if h.count("attentions") != attentions || !reflect.DeepEqual(beforeRun, h.run(original.RunID)) || !reflect.DeepEqual(beforeTask, h.task(original.TaskID)) {
		t.Fatal("history read generated a lease fault")
	}
	if pending, err := h.app.NativeReceiptPending(t.Context(), pending); err != nil || !pending {
		t.Fatal("unknown receipt was guessed", pending, err)
	}
}

func TestFault_ManagerLeaseExpiryStopsAtParentOwner(t *testing.T) {
	h, _, issue, as, _ := reviewManagerAssignment(t)
	renewalBoundFixture(t, h, as)
	parent := createCanonicalOwnerTestIssue(t, h)
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	payload := struct {
		canonicalOwnerControl
		ParentIssueID          string `json:"parent_issue_id"`
		ExpectedParentRevision *int64 `json:"expected_parent_revision"`
	}{canonicalOwnerControl{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Move manager responsibility under the parent owner"}, parent.IssueID, rev(parent.Revision)}
	canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "reparent", payload))
	h.clock.Advance(DefaultLeaseTTL + time.Second)
	if n, err := h.app.ExpireLeases(t.Context()); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	a := expiredManagerProblem(t, h, as)
	if a.Responsibility == nil || a.Responsibility.RecipientKind != "owner" || a.Responsibility.IssueID != parent.IssueID || a.Responsibility.OriginIssueID != issue.IssueID {
		t.Fatal("manager failure bypassed its actual parent owner", a)
	}
	assertHumanProblemCount(t, h, 1)
}

func TestManagerWaitingAndPauseDoNotRaiseLeaseProblem(t *testing.T) {
	h, _, issue, as, _ := reviewManagerAssignment(t)
	if n, err := h.app.ExpireLeases(t.Context()); err != nil || n != 0 || h.count("attentions") != 0 {
		t.Fatal("ordinary waiting was reported as a failure", n, err)
	}
	paused := h.exec(owner, h.env("issues.pause", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Owner explicitly pauses work"}, rev(issue.Revision)), CanonicalOwnerIssueCommand{Action: "pause"})
	if paused.Status != protocol.OpSucceeded {
		t.Fatal(paused)
	}
	if n, err := h.app.ExpireLeases(t.Context()); err != nil || n != 0 || h.count("attentions") != 0 || h.run(as.RunID).ObservedState == protocol.RunUnknown {
		t.Fatal("pause without lease expiry was reported as a technical failure", n, err)
	}
}
