package app

import (
	"encoding/json"
	"reflect"
	"strings"
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

func TestFault_ManagerReceiptReaderLeaseExpiryNotifiesSupervisorOnce(t *testing.T) {
	h, original, pending := stoppedRetainedManager(t)
	payload := receiptReaderPayload(t, h, original)
	op := h.exec(owner, h.env(protocol.OpReconcileManagerRun, taskTarget(original.TaskID), payload, rev(h.task(original.TaskID).ControlRevision)), ReconcileManagerRun{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var runtime protocol.RunnerRuntime
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		host, err := store.GetHostRuntime(t.Context(), tx.Q(), "linux-1")
		runtime = host.Runtime
		return err
	}); err != nil {
		t.Fatal(err)
	}
	reader := reviewClaimManager(t, h, &runtime)
	if reader.Manager == nil || !reader.Manager.ReceiptOnly {
		t.Fatal("fixture did not claim a receipt reader", reader)
	}
	models, err := h.app.QueueManagerNative(t.Context(), reader.RunID, "models", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	beforeOriginal, beforeTask := h.run(original.RunID), h.task(reader.TaskID)
	h.clock.Advance(DefaultLeaseTTL + time.Second)
	if n, err := h.app.ExpireLeases(t.Context()); err != nil || n != 1 {
		t.Fatal("expire reader lease", n, err)
	}
	a := expiredManagerProblem(t, h, reader)
	if a.Kind != "action" || a.Status != "open" || a.Responsibility == nil || a.Responsibility.RecipientKind != "main" || a.Responsibility.OriginIssueID != reader.Manager.IssueID || len(a.Responsibility.History) != 1 {
		t.Fatal("reader fault did not reach the existing supervisor chain", a)
	}
	assertHumanProblemCount(t, h, 0)
	if !strings.Contains(a.Title, reader.RunID) || !strings.Contains(a.Title, reader.LeaseID) {
		t.Fatal("problem lost its original run/lease identity", a)
	}
	if h.run(reader.RunID).ObservedState != protocol.RunUnknown || !reflect.DeepEqual(h.run(original.RunID), beforeOriginal) || !reflect.DeepEqual(h.task(reader.TaskID), beforeTask) {
		t.Fatal("notification changed retained execution or owner control")
	}
	if h.countWhere(`SELECT COUNT(*) FROM native_turn_receipts_pending WHERE request_id=?`, pending) != 1 || h.countWhere(`SELECT COUNT(*) FROM manager_native_requests WHERE request_id=?`, models) != 0 {
		t.Fatal("notification changed the pending receipt or archived an ordinary read RPC")
	}
	if _, ready, exists := h.app.nativeCalls.result(reader.RunID, models); !exists || ready {
		t.Fatal("lease notification settled the still-pending read transport")
	}
	if h.countWhere(`SELECT COUNT(*) FROM leases WHERE lease_id=? AND status='lapsed'`, reader.LeaseID) != 1 || h.countWhere(`SELECT COUNT(*) FROM run_agent_executions WHERE run_id=? AND pin_json=?`, reader.RunID, string(mustJSON(t, reader.Execution))) != 1 {
		t.Fatal("notification changed lease isolation or frozen execution")
	}
	if h.countWhere(`SELECT COUNT(*) FROM conversation_bindings WHERE message_id=? AND ((object_type='run' AND object_id=?) OR (object_type='lease' AND object_id=?))`, a.SourceMessageID, reader.RunID, reader.LeaseID) != 2 {
		t.Fatal("source did not bind the exact expired run and lease")
	}
	if h.countWhere(`SELECT COUNT(*) FROM background_sources s JOIN conversation_bindings b ON b.message_id=s.message_id WHERE b.object_type='attention' AND b.object_id=? AND s.kind='attention.responsibility_changed'`, a.AttentionID) != 1 {
		t.Fatal("supervisor did not receive a durable problem source")
	}
	before := h.count("attentions")
	h.app = New(h.store, h.ids, h.clock.Now)
	if n, err := h.app.ExpireLeases(t.Context()); err != nil || n != 0 || h.count("attentions") != before {
		t.Fatal("restart or repeated expiry duplicated the original problem", n, err)
	}
	if h.countWhere(`SELECT COUNT(*) FROM conversation_bindings WHERE object_type='lease' AND object_id=? AND kind='manager_lease_expired'`, reader.LeaseID) != 1 || h.countWhere(`SELECT COUNT(*) FROM inbox WHERE source=?`, "manager-lease-problem:"+reader.RunID) != 0 {
		t.Fatal("lease problem did not use its actual lease/message identity")
	}
	if op := h.exec(owner, h.env("categories.put", protocol.Target{Type: "category", ID: "planner"}, protocol.PutAgentCategoryPayload{Title: "Planner", State: "active"}, rev(0)), PutAgentCategory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	accepted, hosted := acceptHostedFixture(t, h, hostedConfigurationFixture(), 0)
	if accepted.Status != protocol.OpSucceeded {
		t.Fatal(accepted)
	}
	h.app = New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(hosted))
	_, delivery := h.say("Inspect the manager reconciliation problem")
	turn := startHostedReview(t, h, "main", delivery)
	escalated := hostedEffect(t, h, mainAgent, turn, 1, protocol.Target{Type: "attention", ID: a.AttentionID}, rev(a.Responsibility.Revision), protocol.HandleAttentionPayload{Reason: "Original runtime receipts remain unavailable; supervisor cannot finish reconciliation"}, HandleAttention{Escalate: true})
	if escalated.Status != protocol.OpSucceeded {
		t.Fatal("main could not escalate the original technical problem", escalated)
	}
	assertHumanProblemCount(t, h, 1)
}

func TestFault_ManagerLeaseExpiryStopsAtParentOwner(t *testing.T) {
	h, _, issue, as, _ := reviewManagerAssignment(t)
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
