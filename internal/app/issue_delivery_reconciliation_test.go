package app

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/store"
)

func TestDeliveryReconcilesAcceptedLegacyInstallationWithoutRelabelingOrReplay(t *testing.T) {
	h, i, original, c := deliveryFixture(t)
	spec := testSpec
	spec.Deliverables = []string{domain.DeliveryReport}
	op := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: i.IssueID}, canonicalOwnerTaskPayload{ExpectedAssignmentEpoch: i.AssignmentEpoch, Title: "Complete first installation using the existing interface", Spec: spec}, rev(i.Revision)), CanonicalOwnerTaskCreate{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var prior taskResult
	if err := json.Unmarshal(op.Result, &prior); err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("e", 64)
	source := strings.Repeat("c", 40)
	batch := store.UpgradeRow{View: protocol.UpgradeView{UpgradeID: "original-bootstrap", ReleaseID: "first-release", SourceCommit: source, Revision: 5, Status: "completed", Phase: "completed", CreatedAt: h.clock.Now(), UpdatedAt: h.clock.Now(), Hosts: []protocol.UpgradeHostView{{HostID: "linux-1", Status: "skipped"}}}, Manifest: release.Manifest{ReleaseID: "first-release"}, ManifestSHA256: hash, ServerHash: hash, ServerInstance: "server", ServerProcess: "installed-server", ServerHealthy: 2, Hosts: []store.UpgradeHost{{HostID: "linux-1", PreviousHash: hash, TargetHash: hash}}}
	executeAcceptedDeliveryTaskForTest(t, h, prior.TaskID, func() protocol.ArtifactPayload {
		if err := h.store.Write(t.Context(), func(tx *store.Tx) error { return tx.PutUpgrade(t.Context(), batch) }); err != nil {
			t.Fatal(err)
		}
		a := testArtifact(h.task(prior.TaskID).WorkspaceName)
		a.Report = &protocol.ResultReport{Body: "Existing merge and original controlled upgrade verified against original behavior", Evidence: []string{"upgrade:original-bootstrap"}}
		return a
	})
	priorTask := h.task(prior.TaskID)
	priorRuns := deliveryRunCount(t, h, prior.TaskID)
	// The newly installed mechanism verifies existing facts. It cannot pretend the
	// earlier task had the typed authority which did not exist on its old server.
	h.clock.Advance(time.Second)
	c.ProviderState, c.MergeSHA, c.BaseSHA = "merged", source, strings.Repeat("d", 40)
	c.Deploy = true
	c.Deployment = &protocol.IssueDeliveryDeployment{ServerInstance: "server", HostIDs: []string{"linux-1"}}
	c.DeploymentScope = "Verify only the original completed installation and actual original Issue behavior"
	c.Reconciliation = &protocol.IssueDeliveryReconciliation{EvidenceTaskID: prior.TaskID, UpgradeID: batch.View.UpgradeID}
	observeDeliveryForTest(t, h, i.IssueID, c, "merged", source)
	for _, invalid := range []string{"different-batch", "unknown", "source"} {
		t.Run(invalid, func(t *testing.T) {
			candidate := c
			r := *c.Reconciliation
			candidate.Reconciliation = &r
			changed := batch
			switch invalid {
			case "different-batch":
				r.UpgradeID = "other"
			case "unknown":
				changed.View.Status = "attention"
			case "source":
				r.SourceCommit = strings.Repeat("f", 40)
			}
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error { return tx.PutUpgrade(t.Context(), changed) }); err != nil {
				t.Fatal(err)
			}
			current := canonicalTaskIssue(t, h, i.IssueID)
			payload := protocol.UpdateIssueDeliveryPayload{TaskID: original, Action: "prepare", Candidate: &candidate, IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: current.AssignmentEpoch, Reason: "Verify exact original installation"}}
			op, err := h.app.Execute(t.Context(), owner, h.env("issues.update_delivery", protocol.Target{Type: "issue", ID: i.IssueID}, payload, rev(current.Revision)), UpdateIssueDelivery{})
			if err == nil && op.Status == protocol.OpSucceeded {
				t.Fatal("invalid installation authorized", op)
			}
		})
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error { return tx.PutUpgrade(t.Context(), batch) }); err != nil {
		t.Fatal(err)
	}
	duty := prepareDeliveryForTest(t, h, i.IssueID, original, c)
	if duty.Candidate.Reconciliation.SourceCommit != source || strings.Contains(deliveryCandidateQuestion(*duty.Candidate), "Tend executes the controlled self-update itself") || !strings.Contains(deliveryCandidateQuestion(*duty.Candidate), "Verify existing installation only") {
		t.Fatal(duty)
	}
	expectWorkCode(t, createDeliveryExecutionForTest(t, h, i.IssueID, duty), protocol.CodeForbidden)
	answerDeliveryForTest(t, h, duty, "approve", "")
	op = createDeliveryExecutionForTest(t, h, i.IssueID, duty)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var verification taskResult
	if err := json.Unmarshal(op.Result, &verification); err != nil {
		t.Fatal(err)
	}
	frozen := h.storedSpec(verification.TaskID, 1)
	if !strings.Contains(strings.Join(frozen.Constraints, "\n"), "verification only: existing merge") {
		t.Fatal("lost read-only scope", frozen)
	}
	executeAcceptedDeliveryTaskForTest(t, h, verification.TaskID, func() protocol.ArtifactPayload {
		// Even a verifier with a running lease cannot start another installation.
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			_, err := checkDeliveryUpgradeStart(t.Context(), &Exec{Now: h.clock.Now()}, tx.Q(), StartUpgrade{DeliveryTaskID: verification.TaskID})
			if protocol.CodeOf(err) != protocol.CodeForbidden {
				t.Fatalf("verification authorized installation: %v", err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Independently review original installation and original behavior; no mutation performed", Evidence: []string{"upgrade:original-bootstrap"}}}
	})
	op = updateDeliveryForTest(t, h, i.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: original, Action: "complete", UpgradeID: batch.View.UpgradeID, IssueControlPayload: protocol.IssueControlPayload{Reason: "New independent verification confirms original accepted installation"}})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	duty = deliveryDutyForTest(t, h, i.IssueID, original)
	if duty.Status != "complete" || len(duty.Evidence) != 4 || duty.Evidence[3].Upgrade.SourceCommit != source {
		t.Fatal(duty)
	}
	if deliveryRunCount(t, h, prior.TaskID) != priorRuns || h.task(prior.TaskID).CurrentArtifactID != priorTask.CurrentArtifactID || h.task(prior.TaskID).Lifecycle != protocol.TaskCompleted || h.count("upgrades") != 1 || h.count("issue_delivery_executions") != 1 {
		t.Fatal("historical execution was relabeled, revived or replayed")
	}
	var row store.UpgradeRow
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		row, err = store.GetUpgrade(t.Context(), tx.Q(), batch.View.UpgradeID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if row.View.Revision != batch.View.Revision || row.DeliveryTaskID != "" {
		t.Fatal("historical batch changed", row)
	}
}

func TestDeliveryTechnicalSupervisorCannotAnswerHumanIntegrationConfirmation(t *testing.T) {
	h := hostedManagerHarness(t)
	_, i, task, c := deliveryFixtureOnHarness(t, h)
	i = assignHostedManager(t, h, canonicalTaskIssue(t, h, i.IssueID))
	duty := prepareDeliveryForTest(t, h, i.IssueID, task, c)
	a := readAttentionForTest(t, h, duty.AttentionID)
	if a.Responsibility == nil {
		t.Fatal("missing normal supervisor routing", a)
	}
	op := supervisorHandle(t, h, i, a, false, "approve")
	expectWorkCode(t, op, protocol.CodeForbidden)
	a = readAttentionForTest(t, h, a.AttentionID)
	if a.Status != "open" || a.QuestionVersion != 1 || h.count("issue_delivery_executions") != 0 {
		t.Fatal("technical answer consumed human decision", a)
	}
	if op := supervisorHandle(t, h, i, a, true, ""); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	// A real owner answer through the original versioned interface still works.
	answerDeliveryForTest(t, h, duty, "approve", "")
	if op := createDeliveryExecutionForTest(t, h, i.IssueID, duty); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	observerHello(t, h, "linux-1", "expiry-observer")
	assignments := pollDelivery(t, h, "linux-1", "expiry-observer")
	if len(assignments) != 1 {
		t.Fatal(assignments)
	}
	aQuery := assignments[0]
	changed := protocol.DeliverySnapshot{URL: c.URL, HeadSHA: strings.Repeat("e", 40), BaseSHA: c.BaseSHA, State: "open"}
	payload := protocol.DeliveryReportPayload{InstanceID: "expiry-observer", WorkItemID: i.IssueID, DeliveryID: c.DeliveryID, Generation: aQuery.Generation, Nonce: aQuery.Nonce, Snapshot: &changed}
	if !accepted(t, h.exec(runner("linux-1"), h.env(protocol.OpDeliveriesReport, protocol.Target{Type: "host", ID: "linux-1"}, payload, nil), ReportDelivery{})) {
		t.Fatal("new provider fact rejected")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var count int
		err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM background_sources s JOIN background_batches b USING(batch_id) JOIN conversation_messages m ON m.message_id=s.message_id WHERE s.kind='issue.delivery_candidate_expired' AND b.work_item_id=? AND m.conversation_id<> 'main'`, i.IssueID).Scan(&count)
		if err == nil && count != 1 {
			t.Fatalf("expired candidate did not route exactly once to original manager: %d", count)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDeliveryOwnerHandoffFencesOldAnswerWithoutRestartingImplementation(t *testing.T) {
	h := hostedManagerHarness(t)
	_, i, task, c := deliveryFixtureOnHarness(t, h)
	old := prepareDeliveryForTest(t, h, i.IssueID, task, c)
	answerDeliveryForTest(t, h, old, "approve", "")
	originalArtifact := h.task(task).CurrentArtifactID
	before := deliveryRunCount(t, h, task)
	i = assignHostedManager(t, h, canonicalTaskIssue(t, h, i.IssueID))
	if i.AssignmentEpoch == old.AssignmentEpoch {
		t.Fatal("handoff did not fence owner")
	}
	expectWorkCode(t, createDeliveryExecutionForTest(t, h, i.IssueID, old), protocol.CodeStaleRevision)
	current := prepareDeliveryForTest(t, h, i.IssueID, task, c)
	if current.AttentionID == old.AttentionID || current.AssignmentEpoch != i.AssignmentEpoch {
		t.Fatal("new owner inherited old approval", current)
	}
	expectWorkCode(t, createDeliveryExecutionForTest(t, h, i.IssueID, current), protocol.CodeForbidden)
	if h.task(task).CurrentArtifactID != originalArtifact || h.task(task).Lifecycle != protocol.TaskCompleted || deliveryRunCount(t, h, task) != before {
		t.Fatal("handoff replayed terminal implementation")
	}
}
