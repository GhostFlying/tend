package app

import (
	"encoding/json"
	"io"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestFault_OwnerDeployProgressPublishesOriginalBeforeUpgrade(t *testing.T) {
	h, issue, producer, _, _ := ownerBoundDeliveryFixture(t, true)
	frozen := deliveryDutyForTest(t, h, issue.IssueID, producer.TaskID)
	before := h.task(producer.TaskID)
	tasks, runs := h.count("tasks"), h.count("runs")
	const original = "Current candidate: complete local checks passed; deployment has not started.\n"
	file, _ := h.uploadResultFile(producer, "checks.log", []byte(original))
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		allowed, err := communicationResultInScope(t.Context(), tx.Q(), issue.IssueID, file.FileID)
		if err == nil && allowed {
			t.Fatal("an upload was shared before its real result was recorded")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	delivery, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+producer.Manager.ConversationID)
	if err != nil || !ok {
		t.Fatal("the original owner has no admitted trigger", ok, err)
	}
	turn := startHostedReview(t, h, producer.Manager.ConversationID, delivery)
	principal := runPrincipal(producer.RunID, producer.Role)
	principal.Thread, principal.ConversationID = turn.ThreadID, producer.Manager.ConversationID
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	p := protocol.UpdateIssueDeliveryPayload{Action: "submit_result", TaskID: producer.TaskID, RunID: producer.RunID, Result: &protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "New checks for the same responsibility, before merge or installation."}, Files: []protocol.ArtifactFile{file}}, IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Share the original check evidence without inventing an upgrade"}}
	op := hostedEffect(t, h, principal, turn, 1, protocol.Target{Type: "issue", ID: issue.IssueID}, rev(issue.Revision), p, UpdateIssueDelivery{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal("a bound deployment blocked its owner's predeployment original", op)
	}
	current := deliveryDutyForTest(t, h, issue.IssueID, producer.TaskID)
	after := h.task(producer.TaskID)
	if current.Revision != frozen.Revision || current.Status != "executing" || current.ExecutionTaskID != producer.TaskID || !reflect.DeepEqual(current.Candidate, frozen.Candidate) || !reflect.DeepEqual(current.Authorization, frozen.Authorization) || current.ArtifactID == frozen.ArtifactID {
		t.Fatal("recording progress replaced delivery authority", current)
	}
	if before.Lifecycle != after.Lifecycle || before.ControlRevision != after.ControlRevision || before.SpecVersion != after.SpecVersion || h.count("tasks") != tasks || h.count("runs") != runs || h.lease(producer.RunID).Status != store.LeaseActive {
		t.Fatal("recording progress fabricated a task, completion or resource release")
	}
	publication := protocol.CoordinationReply{Body: "The current check original is attached; deployment is pending.", Purpose: "progress", Attachments: []string{file.FileID}}
	op = hostedEffect(t, h, principal, turn, 2, protocol.Target{Type: "logical_turn", ID: turn.RequestID}, nil, publication, CoordinationReply{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal("the same responsibility cannot share its recorded original", op)
	}
	meta, body, err := h.app.OpenConversationAttachment(t.Context(), file.FileID)
	if err != nil {
		t.Fatal(err)
	}
	content, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil || string(content) != original || meta.SHA256 != file.SHA256 || meta.Size != file.Size {
		t.Fatal("published original differs from the producing Run", meta, string(content), err)
	}
	var bound string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT upgrade_id FROM issue_delivery_executions WHERE task_id=?`, producer.TaskID).Scan(&bound)
	}); err != nil || bound != "" || h.count("upgrades") != 0 {
		t.Fatal("a progress result invented an installation", bound, err)
	}
	if op := updateDeliveryForTest(t, h, issue.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: producer.TaskID, Action: "complete", IssueControlPayload: protocol.IssueControlPayload{Reason: "A check report cannot settle a deployment"}}); op.Status != protocol.OpFailed {
		t.Fatal("the check report bypassed actual merge and installation", op)
	}
}

func TestOwnerDeployProgressRetainsExplicitUpgradeValidation(t *testing.T) {
	h, issue, producer, _, register := ownerBoundDeliveryFixture(t, true)
	for _, evidence := range [][]string{{"upgrade:"}, {"upgrade:one", "upgrade:one"}, {"upgrade:missing"}} {
		before := h.task(producer.TaskID).CurrentArtifactID
		artifacts := h.count("artifacts")
		p := protocol.UpdateIssueDeliveryPayload{Action: "submit_result", TaskID: producer.TaskID, RunID: producer.RunID, Result: &protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Explicit installation claims still require their actual binding.", Evidence: evidence}}, IssueControlPayload: protocol.IssueControlPayload{Reason: "Validate the supplied upgrade reference"}}
		if op := updateDeliveryForTest(t, h, issue.IssueID, p); op.Status != protocol.OpFailed || h.count("artifacts") != artifacts || h.task(producer.TaskID).CurrentArtifactID != before {
			t.Fatal("an invalid explicit upgrade was accepted or partially recorded", evidence, op)
		}
	}
	batch := store.UpgradeRow{DeliveryTaskID: producer.TaskID, ServerInstance: "server", View: protocol.UpgradeView{UpgradeID: "original-pending-install", ReleaseID: "original-release", Revision: 1, Status: "attention", Phase: "server_reconcile", CreatedAt: h.clock.Now(), UpdatedAt: h.clock.Now(), Hosts: []protocol.UpgradeHostView{}}}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error { return tx.PutUpgrade(t.Context(), batch) }); err != nil {
		t.Fatal(err)
	}
	p := protocol.UpdateIssueDeliveryPayload{Action: "submit_result", TaskID: producer.TaskID, RunID: producer.RunID, Result: &protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "The original installation requires reconciliation.", Evidence: []string{"upgrade:" + batch.View.UpgradeID}}}, IssueControlPayload: protocol.IssueControlPayload{Reason: "Retain the actual original external operation"}}
	if op := updateDeliveryForTest(t, h, issue.IssueID, p); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	register("Later diagnostic facts do not erase or settle the original pending installation")
	var bound string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT upgrade_id FROM issue_delivery_executions WHERE task_id=?`, producer.TaskID).Scan(&bound)
	}); err != nil || bound != batch.View.UpgradeID || h.count("upgrades") != 1 {
		t.Fatal("progress changed the original installation identity", bound, err)
	}
	p.Result.Report.Evidence = []string{"upgrade:replacement"}
	if op := updateDeliveryForTest(t, h, issue.IssueID, p); op.Status != protocol.OpFailed {
		t.Fatal("a later report replaced the original upgrade", op)
	}
	var saved protocol.ArtifactPayload
	artifact := h.task(producer.TaskID).CurrentArtifactID
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		raw, err := store.GetArtifactRef(t.Context(), tx.Q(), artifact)
		if err != nil {
			return err
		}
		return json.Unmarshal([]byte(raw), &saved)
	}); err != nil || saved.Report == nil || len(saved.Report.Evidence) != 0 {
		t.Fatal("the result falsely inherited an upgrade claim", saved, err)
	}
}
