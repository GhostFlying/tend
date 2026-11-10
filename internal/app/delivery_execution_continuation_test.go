package app

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func TestDeliveryContinuationKeepsHistoricalRunAndReleasesOldBinding(t *testing.T) {
	h, issue, source, execution, batch := historicalDeploymentFixture(t)
	if op := updateDeliveryForTest(t, h, issue.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: source, Action: "complete", UpgradeID: batch.View.UpgradeID, IssueControlPayload: protocol.IssueControlPayload{Reason: "Settle the real original independently accepted installation"}}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	old := h.task(execution)
	var originalRun string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT run_id FROM artifacts WHERE artifact_id=?`, old.CurrentArtifactID).Scan(&originalRun)
	}); err != nil {
		t.Fatal(err)
	}
	if op := continueTaskForTest(t, h, execution, "Continue the same integration goal for the next actual candidate"); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		current, err := views.DeliveryExecutionAuthority(t.Context(), tx.Q(), execution)
		if err != nil {
			return err
		}
		if current == nil || current.Valid || len(current.History) != 1 || current.History[0].RunID != originalRun {
			t.Fatal("old authorization was reused as current", current)
		}
		previous, err := views.HistoricalDeliveryExecutionAuthority(t.Context(), tx.Q(), originalRun, 0)
		if err != nil {
			return err
		}
		if !previous.Historical || previous.Valid || previous.Settlement == nil || !previous.Settlement.Eligible || previous.UpgradeID != batch.View.UpgradeID || previous.Settlement.ArtifactID != old.CurrentArtifactID {
			t.Fatal("original accepted installation lost identity", previous)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	read, err := h.app.ReadIssueMaterials(t.Context(), issue.IssueID, protocol.IssueMaterialsQuery{Kind: "delivery_authorization", ID: originalRun})
	if err != nil || read.DeliveryAuthorization == nil || !read.DeliveryAuthorization.Historical {
		t.Fatal("fresh session cannot select the original Run", read, err)
	}
	next, ok, _ := h.claim("linux-1")
	if !ok || next.TaskID != execution || next.RunID == originalRun {
		t.Fatal(next)
	}
	if op := h.runAs(next, protocol.OpRunsSubmitArtifact, protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "The same Task's new candidate preparation does not replay its previous installation."}}, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal("new result was forced to repeat the old upgrade reference", op)
	}
}

func TestDeliveryContinuationCannotEraseUnsettledOriginalExecution(t *testing.T) {
	h, _, _, execution, _ := historicalDeploymentFixture(t)
	op := continueTaskForTest(t, h, execution, "Continue before the original responsibility is settled")
	if op.Error == nil || op.Error.Code != protocol.CodeInvalidTransition || h.count("issue_delivery_execution_history") != 0 {
		t.Fatal("unsettled original execution was discarded", op)
	}
}

func TestDeliveryContinuationPreservesUnknownOriginalUpgrade(t *testing.T) {
	h, issue, source, execution, batch := historicalDeploymentFixture(t)
	if op := updateDeliveryForTest(t, h, issue.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: source, Action: "complete", UpgradeID: batch.View.UpgradeID, IssueControlPayload: protocol.IssueControlPayload{Reason: "Settle the accepted original installation before checking external uncertainty"}}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	// Model an independently observed unresolved external receipt. A completed
	// Task or historical duty must not erase a still-unknown installation.
	batch.View.Status, batch.View.Phase = "attention", "server_reconcile"
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error { return tx.PutUpgrade(t.Context(), batch) }); err != nil {
		t.Fatal(err)
	}
	before := h.task(execution)
	env := h.env("tasks.continue", taskTarget(execution), protocol.ContinueTaskPayload{Reason: "Continue the same goal without replaying an unknown external operation"}, rev(before.ControlRevision))
	op, err := h.app.Execute(t.Context(), owner, env, ContinueTask{})
	if err == nil {
		err = op.Error
	}
	if protocol.CodeOf(err) != protocol.CodeUnknownOutcome || h.count("issue_delivery_execution_history") != 0 || h.task(execution).ControlRevision != before.ControlRevision {
		t.Fatal("continuation erased the unknown original upgrade", op)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var retained string
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT upgrade_id FROM issue_delivery_executions WHERE task_id=?`, execution).Scan(&retained); err != nil {
			return err
		}
		if retained != batch.View.UpgradeID {
			t.Fatal("original reconciliation identity was replaced", retained)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDeliveryContinuationCancelledScopeNeedsEveryActualCancellationACK(t *testing.T) {
	h, issue, source, execution, batch := historicalDeploymentFixture(t)
	endDeliveryForTest(t, h, issue.IssueID, source)
	cfg := testConfig()
	second := cfg.Hosts[0]
	second.HostID = "linux-2"
	cfg.Hosts = append(cfg.Hosts, second)
	if op := h.syncConfig(cfg); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	for _, host := range []string{"linux-1", "linux-2"} {
		if op := h.versionedHello(host, testRuntime("confirmed-old-"+host)); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	// The independently observed upgrade may stop before all targets. Its
	// accepted report and ended responsibility remain real historical facts.
	batch.View.Status = "attention"
	batch.Hosts = append(batch.Hosts, store.UpgradeHost{HostID: "linux-2"})
	batch.View.Hosts = append(batch.View.Hosts, protocol.UpgradeHostView{HostID: "linux-2", Status: "skipped"})
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error { return tx.PutUpgrade(t.Context(), batch) }); err != nil {
		t.Fatal(err)
	}
	op := h.exec(owner, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: batch.View.UpgradeID}, protocol.UpgradeControlPayload{Action: "cancel"}, rev(batch.View.Revision)), ControlUpgrade{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	blocked := func() {
		t.Helper()
		before := h.task(execution)
		op, err := h.app.Execute(t.Context(), owner, h.env("tasks.continue", taskTarget(execution), protocol.ContinueTaskPayload{Reason: "Continue only after the original cancelled operation is actually fenced"}, rev(before.ControlRevision)), ContinueTask{})
		if err == nil {
			err = op.Error
		}
		if protocol.CodeOf(err) != protocol.CodeUnknownOutcome || h.count("issue_delivery_execution_history") != 0 || h.task(execution).ControlRevision != before.ControlRevision {
			t.Fatal("intent was mistaken for a terminal receipt", op, err)
		}
		failure, ok := err.(*protocol.Error)
		var pending struct {
			UpgradeID string   `json:"upgrade_id"`
			Server    bool     `json:"server_cancellation_pending"`
			Hosts     []string `json:"pending_host_ids"`
		}
		if !ok || json.Unmarshal(failure.Details, &pending) != nil || pending.UpgradeID != batch.View.UpgradeID || !pending.Server && len(pending.Hosts) == 0 {
			t.Fatal("unknown result hid the actual outstanding cancellation ACK", err)
		}
	}
	blocked()
	upgradeAck(h, "linux-1", protocol.RunnerNormal)
	blocked()
	if _, err := h.app.TickUpgrade(t.Context(), ServerObservation{InstanceID: "wrong-server", ServerCancellations: []string{batch.View.UpgradeID}}); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("another server supplied cancellation authority", err)
	}
	upgradeTick(h, ServerObservation{InstanceID: batch.ServerInstance, ServerCancellations: []string{batch.View.UpgradeID}})
	blocked() // The second original runner still owes its actual cancellation ACK.
	upgradeAck(h, "linux-2", protocol.RunnerNormal)
	if op := continueTaskForTest(t, h, execution, "The original server and both runners confirmed cancellation; continue the same goal"); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		original, err := store.GetUpgrade(t.Context(), tx.Q(), batch.View.UpgradeID)
		if err != nil {
			return err
		}
		if original.View.Status != "cancelled" || !original.ServerCancellationConfirmed || !original.Hosts[0].CancellationConfirmed || !original.Hosts[1].CancellationConfirmed {
			t.Fatal("original cancellation was rewritten or not fully confirmed", original)
		}
		current, err := views.DeliveryExecutionAuthority(t.Context(), tx.Q(), execution)
		if err == nil && (current.Valid || len(current.History) != 1 || current.History[0].UpgradeID != batch.View.UpgradeID) {
			t.Fatal("cancellation manufactured new authority or lost the original", current)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
