package app

import (
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"testing"
)

func TestCompactFirstThreadKeepsFrozenRuntimeAfterConfigurationChange(t *testing.T) {
	h := hostedManagerHarness(t)
	original, named, err := h.app.NamedHostedSelection(t.Context(), "main")
	if err != nil || !named {
		t.Fatal(original, named, err)
	}
	request, _, _ := startupCompact(t, h, "main", "unknown", &original)
	reloadHostedAdmissionConfig(t, h, false)
	selected, named, attachment, err := h.app.HostedRuntimeStartupSelection(t.Context(), "main")
	if err != nil || !named || selected.ConfigurationHash != original.ConfigurationHash || !attachment.Reconciliation || attachment.RequestID != request {
		t.Fatal("first-thread compact switched away from its frozen runtime", selected, named, attachment, err)
	}
	if h.count("hosted_executions") != 0 || h.count("main_turns") != 0 {
		t.Fatal("compact pin fabricated a coordination turn")
	}
}

func TestCompactHistoricalThreadRestoresOriginalRuntime(t *testing.T) {
	for _, sameThread := range []bool{true, false} {
		t.Run(map[bool]string{true: "same_thread", false: "other_thread"}[sameThread], func(t *testing.T) {
			h := hostedManagerHarness(t)
			issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
			original := prepareHostedAdmissionRepair(t, h, issue)
			thread := original.ThreadID
			if !sameThread {
				thread += "-different"
			}
			request, _, _ := startupCompactOnThread(t, h, "issue:"+issue.IssueID, "unknown", thread)
			reloadHostedAdmissionConfig(t, h, false)
			selected, named, attachment, err := h.app.HostedRuntimeStartupSelection(t.Context(), "issue:"+issue.IssueID)
			if !sameThread {
				if protocol.CodeOf(err) != protocol.CodeUnknownOutcome {
					t.Fatal("unproven historical runtime was guessed", selected, named, err)
				}
				return
			}
			if err != nil || !named || original.Hosted == nil || selected.ConfigurationHash != original.Hosted.ConfigurationHash || !attachment.Reconciliation || attachment.RequestID != request {
				t.Fatal("historical compact ignored its original native thread execution", selected, named, attachment, err)
			}
		})
	}
}

func TestCompactExplicitLegacyPinSurvivesNamedConfiguration(t *testing.T) {
	h := newHarness(t)
	request, _, _ := startupCompact(t, h, "main", "unknown", nil)
	_, prepared := acceptHostedFixture(t, h, hostedConfigurationFixture(), 0)
	h.app = New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(prepared))
	selected, named, attachment, err := h.app.HostedRuntimeStartupSelection(t.Context(), "main")
	if err != nil || named || !attachment.Reconciliation || attachment.RequestID != request {
		t.Fatal("known legacy compact adopted a newer named binding", selected, named, attachment, err)
	}
}

func TestCompactMissingRuntimeProofRemainsUnknown(t *testing.T) {
	h := hostedManagerHarness(t)
	_, operation, thread := startupCompact(t, h, "main", "unknown")
	_, _, _, err := h.app.HostedRuntimeStartupSelection(t.Context(), "main")
	if protocol.CodeOf(err) != protocol.CodeUnknownOutcome {
		t.Fatal("missing original runtime proof adopted current configuration", err)
	}
	if state, err := h.app.CompactState(t.Context(), operation); err != nil || state != "unknown" {
		t.Fatal(state, err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var actual string
		err := tx.Q().QueryRowContext(t.Context(), `SELECT thread_id FROM agent_compactions WHERE operation_id=?`, operation).Scan(&actual)
		if actual != thread {
			t.Fatal("original native thread changed", actual, thread)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCompactAmbiguousHistoricalRuntimeRemainsUnknown(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	original := prepareHostedAdmissionRepair(t, h, issue)
	conversation := "issue:" + issue.IssueID
	startupCompactOnThread(t, h, conversation, "unknown", original.ThreadID)
	reloadHostedAdmissionConfig(t, h, false)
	other, named, err := h.app.NamedHostedSelection(t.Context(), conversation)
	if err != nil || !named {
		t.Fatal(other, named, err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.InsertHostedExecution(t.Context(), store.HostedExecution{ID: "ambiguous-execution", ConversationID: conversation, IssueID: issue.IssueID, AssignmentEpoch: issue.AssignmentEpoch, Selection: other, CreatedAt: h.clock.Now().UnixMilli()}); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO main_turns(request_id,message_id,thread_id,status,created_at,updated_at) VALUES('ambiguous-execution',?,?,'completed',0,0)`, original.MessageID, original.ThreadID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := h.app.HostedRuntimeStartupSelection(t.Context(), conversation); protocol.CodeOf(err) != protocol.CodeUnknownOutcome {
		t.Fatal("ambiguous native binding was guessed", err)
	}
}

func TestCompactRuntimePinIsImmutable(t *testing.T) {
	h := newHarness(t)
	_, operation, thread := startupCompact(t, h, "main", "unknown", nil)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		pin, err := store.GetCompactionRuntime(t.Context(), tx.Q(), operation)
		if err == nil && (pin.ThreadID != thread || pin.Selection != nil) {
			t.Fatal("legacy pin lost its native identity", pin)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{`UPDATE agent_compaction_runtimes SET thread_id='other' WHERE operation_id=?`, `DELETE FROM agent_compaction_runtimes WHERE operation_id=?`} {
		if err := h.store.Write(t.Context(), func(tx *store.Tx) error { _, err := tx.Exec(t.Context(), query, operation); return err }); err == nil {
			t.Fatal("native pin was mutable")
		}
	}
}

func TestCompactThreadDiscoveryPreservesPendingTurnPriority(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	original, _ := startHostedIssueReview(t, h, issue)
	conversation := "issue:" + issue.IssueID
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_compactions(operation_id,conversation_id,thread_id,status,source,before_json,created_at,updated_at) VALUES('older-compact',?,'different-compact-thread','unknown','manual','{}',0,0)`, conversation)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_, _, attachment, err := h.app.HostedRuntimeStartupSelection(t.Context(), conversation)
	if err != nil || attachment.RequestID != original.RequestID {
		t.Fatal("fixture lost the prior native turn selection", attachment, err)
	}
	thread, pending, err := h.app.UncertainCompactThread(t.Context(), conversation)
	if err != nil || pending || thread != "" {
		t.Fatal("compact thread overrode the selected native turn attachment", thread, pending, err)
	}
}

func TestPendingAgentConversationsQuarantinesUnprovenCompactLocally(t *testing.T) {
	h := hostedManagerHarness(t)
	blocked := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	normal := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	blockedID, normalID := "issue:"+blocked.IssueID, "issue:"+normal.IssueID
	startupCompact(t, h, blockedID, "unknown")
	submitHostedAdmissionInput(t, h, blockedID)
	submitHostedAdmissionInput(t, h, normalID)
	ids, err := h.app.PendingAgentConversations(t.Context())
	if err != nil {
		t.Fatal("one local compact quarantine aborted the entire pool scan", err)
	}
	found := false
	for _, id := range ids {
		if id == blockedID {
			t.Fatal("unproven runtime became runnable")
		}
		if id == normalID {
			found = true
		}
	}
	if !found {
		t.Fatal("unrelated manager lost pool eligibility", ids)
	}
	if _, _, _, err := h.app.HostedRuntimeStartupSelection(t.Context(), blockedID); protocol.CodeOf(err) != protocol.CodeUnknownOutcome {
		t.Fatal("quarantine erased the original unknown", err)
	}
}
