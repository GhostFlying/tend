package app

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestManagerMigrationBacksUpAndReplaysWithoutDuplicatingResponsibility(t *testing.T) {
	h, _ := managerClaimHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	original := []byte("original attachment\x00\n")
	file := uploadChatFile(t, h, "evidence.bin", "application/octet-stream", original)
	env := h.env(OpManagerMigration, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.ManagerMigrationPayload{Phase: "switch", ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Move responsibility without resetting the ledger"}, rev(issue.Revision))
	first, err := h.app.MigrateManager(t.Context(), owner, env)
	if err != nil || first.Status != protocol.OpSucceeded {
		t.Fatal(first, err)
	}
	replay, err := h.app.MigrateManager(t.Context(), owner, env)
	if err != nil || !bytes.Equal(first.Result, replay.Result) || h.count("issue_manager_tasks") != 1 || h.count("manager_migrations") != 1 {
		t.Fatal("migration retry duplicated state", replay, err)
	}
	receipt, err := h.store.BackupManagers(t.Context(), env.RequestID)
	if err != nil || receipt.Objects != 1 || receipt.ID != env.RequestID || receipt.LedgerSHA256 == "" {
		t.Fatal("missing consistent backup", receipt, err)
	}
	meta, body, err := h.app.OpenConversationAttachment(t.Context(), file.FileID)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	if meta.SHA256 != file.SHA256 {
		t.Fatal("migration changed original bytes")
	}
	var result struct {
		Manager store.IssueManagerTask `json:"manager"`
	}
	if err = json.Unmarshal(first.Result, &result); err != nil || result.Manager.HostID != "" {
		t.Fatal("migration selected a host before claim", err)
	}
}

func TestManagerMigrationStaleSwitchReplaysOriginalRejection(t *testing.T) {
	h, _ := managerClaimHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	env := h.env(OpManagerMigration, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.ManagerMigrationPayload{Phase: "switch", ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Retain the original request identity"}, rev(issue.Revision+1))
	first, err := h.app.MigrateManager(t.Context(), owner, env)
	if err != nil || first.Error == nil || first.Error.Code != protocol.CodeStaleRevision {
		t.Fatal(first, err)
	}
	replayed, err := h.app.MigrateManager(t.Context(), owner, env)
	if err != nil || replayed.Error == nil || replayed.Error.Code != protocol.CodeStaleRevision || !bytes.Equal(first.Result, replayed.Result) {
		t.Fatal("switch retry changed its payload identity", replayed, err)
	}
	if h.count("manager_migrations") != 0 || h.count("issue_manager_tasks") != 0 {
		t.Fatal("stale migration created responsibility")
	}
}

func TestManagerMigrationPreservesHostedConversationAndWaitsForUnknownReceipts(t *testing.T) {
	h, r := managerClaimHarness(t)
	inventoryCategory(t, h, "planner", 0, "active")
	_, prepared := acceptHostedFixture(t, h, hostedConfigurationFixture(), 0)
	h.app = New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(prepared))
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	old, _ := startHostedIssueReview(t, h, issue)
	if err := h.app.AwaitNativeReceipts(t.Context(), old.ThreadID); err != nil {
		t.Fatal(err)
	}
	env := h.env(OpManagerMigration, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.ManagerMigrationPayload{Phase: "switch", ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Retain the original ledger and native history references"}, rev(issue.Revision))
	changed, err := h.app.MigrateManager(t.Context(), owner, env)
	if err != nil || changed.Status != protocol.OpSucceeded {
		t.Fatal(changed, err)
	}
	claim := func() *protocol.Assignment {
		op := h.exec(runner("linux-1"), h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{Runtime: r, WorkspaceProtocol: 1}, nil), Claim{})
		var result struct {
			Assignment *protocol.Assignment `json:"assignment"`
		}
		if err := json.Unmarshal(op.Result, &result); err != nil {
			t.Fatal(err)
		}
		return result.Assignment
	}
	if got := claim(); got != nil {
		t.Fatal("successor started with an unknown hosted native receipt", got)
	}
	if ready, err := h.app.AgentConversationClaimed(t.Context(), "issue:"+issue.IssueID); err != nil || !ready {
		t.Fatal("migration blocked its original receipt reader", ready, err)
	}
	if selection, named, err := h.app.HostedRuntimeSelection(t.Context(), "issue:"+issue.IssueID); err != nil || !named || selection.RunnerManaged || !selection.ReceiptOnly || selection.AssignmentEpoch != old.Hosted.AssignmentEpoch {
		t.Fatal("migration reader lost its old read-only identity", selection, named, err)
	}
	if selection, named, err := h.app.NamedHostedSelection(t.Context(), "issue:"+issue.IssueID); protocol.CodeOf(err) != protocol.CodeInvalidTransition || named || selection.RunnerManaged {
		t.Fatal("pending migration fell back to host execution", selection, named, err)
	}
	// The old host reports the original native receipt; no input is resent.
	old.Status = store.TurnCompleted
	old.NativeReconciled = true
	if err := h.app.PutMainTurn(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	deliveries, err := h.app.InFlightDeliveries(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, delivery := range deliveries {
		if delivery.RequestID == old.RequestID {
			if err := h.app.FinishDelivery(t.Context(), delivery, DeliveryResult{Outcome: Delivered}); err != nil {
				t.Fatal(err)
			}
		}
	}
	got := claim()
	if got == nil || got.Manager == nil || got.Manager.ConversationID != "issue:"+issue.IssueID {
		t.Fatal("migration lost the original conversation", got)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		history, err := store.GetHostedExecution(t.Context(), tx.Q(), old.RequestID)
		if err != nil {
			return err
		}
		message, err := store.GetConversationItem(t.Context(), tx.Q(), old.MessageID)
		if err == nil && (history.Selection.RunnerManaged || history.Selection.AssignmentEpoch != issue.AssignmentEpoch || message.ConversationID != got.Manager.ConversationID) {
			t.Fatal("migration rewrote old native/source identity", history, message)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestManagerCutoverDisablesHostedIssueAssignmentButRetainsMainAndConsultation(t *testing.T) {
	h, _ := managerClaimHarness(t)
	inventoryCategory(t, h, "planner", 0, "active")
	_, prepared := acceptHostedFixture(t, h, hostedConfigurationFixture(), 0)
	h.app = New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(prepared))
	issue := createCanonicalOwnerTestIssue(t, h)
	env := h.env(OpManagerMigration, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.ManagerMigrationPayload{Phase: "finalize", ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "All target responsibilities are migrated"}, rev(issue.Revision))
	if result, err := h.app.MigrateManager(t.Context(), owner, env); err != nil || result.Status != protocol.OpSucceeded {
		t.Fatal(result, err)
	}
	assignment := protocol.IssueAssignmentPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeAgent, Ref: "server-hosted/planner"}, Reason: "Attempt the retired host manager path"}
	result := h.exec(owner, h.env("issues.assign", protocol.Target{Type: "issue", ID: issue.IssueID}, assignment, rev(issue.Revision)), HostedIssueAssignment{})
	if result.Error == nil || result.Error.Code != protocol.CodeInvalidTransition {
		t.Fatal("cutover accepted a hosted manager", result)
	}
	if selected, named, err := h.app.NamedHostedSelection(t.Context(), "main"); err != nil || !named || selected.RunnerManaged {
		t.Fatal("cutover disabled the hosted main agent", selected, named, err)
	}
	consult := h.exec(owner, h.env("conversation.consult", protocol.Target{Type: "conversation"}, protocol.CreateConsultationPayload{AgentRef: "server-hosted/planner", Title: "Independent discussion"}, nil), CreateConsultation{})
	if consult.Status != protocol.OpSucceeded {
		t.Fatal("cutover disabled consultation", consult)
	}
	var conversation protocol.AgentConversation
	if err := json.Unmarshal(consult.Result, &conversation); err != nil {
		t.Fatal(err)
	}
	if selected, named, err := h.app.NamedHostedSelection(t.Context(), conversation.ID); err != nil || !named || selected.RunnerManaged {
		t.Fatal("consultation was moved to a runner", selected, named, err)
	}
}
