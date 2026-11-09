package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

// The Task, approval, artifact and acceptance use their normal commands. Only
// the updater's independently persisted installation facts are test doubles.
func historicalDeploymentFixture(t *testing.T) (*harness, protocol.Issue, string, string, store.UpgradeRow) {
	t.Helper()
	return historicalDeploymentStateFixture(t, "merged")
}

func historicalDeploymentStateFixture(t *testing.T, state string) (*harness, protocol.Issue, string, string, store.UpgradeRow) {
	t.Helper()
	return historicalDeploymentHarnessFixture(t, newHarness(t), state)
}

func historicalDeploymentHarnessFixture(t *testing.T, h *harness, state string) (*harness, protocol.Issue, string, string, store.UpgradeRow) {
	t.Helper()
	_, issue, source, candidate := deliveryFixtureOnHarness(t, h)
	candidate.ProviderState = "merged"
	candidate.MergeSHA = strings.Repeat("c", 40)
	candidate.TargetSHA = candidate.MergeSHA
	candidate.Deploy = true
	candidate.Deployment = &protocol.IssueDeliveryDeployment{ServerInstance: "server", HostIDs: []string{"linux-1"}}
	candidate.DeploymentScope = "Install this exact merged source on the server and retained runner"
	if state == "open" {
		candidate.ProviderState, candidate.MergeSHA, candidate.TargetSHA = "open", "", candidate.BaseSHA
	}
	observeDeliveryForTest(t, h, issue.IssueID, candidate, state, candidate.MergeSHA)
	duty := prepareDeliveryForTest(t, h, issue.IssueID, source, candidate)
	answerDeliveryForTest(t, h, duty, "approve", "")
	op := createDeliveryExecutionForTest(t, h, issue.IssueID, duty)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var result taskResult
	if err := json.Unmarshal(op.Result, &result); err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("e", 64)
	batch := store.UpgradeRow{DeliveryTaskID: result.TaskID, View: protocol.UpgradeView{UpgradeID: "historical-install", ReleaseID: "original-release", SourceCommit: strings.Repeat("c", 40), Revision: 22, Status: "completed", Phase: "completed", CreatedAt: h.clock.Now(), UpdatedAt: h.clock.Now(), Hosts: []protocol.UpgradeHostView{{HostID: "linux-1", Status: "skipped"}}}, Manifest: release.Manifest{ReleaseID: "original-release"}, ManifestSHA256: hash, ServerHash: hash, ServerInstance: "server", ServerProcess: "original-process", ServerHealthy: 2, Hosts: []store.UpgradeHost{{HostID: "linux-1", PreviousHash: hash, TargetHash: hash}}}
	executeAcceptedDeliveryTaskForTest(t, h, result.TaskID, func() protocol.ArtifactPayload {
		if state == "open" {
			merged := candidate
			merged.TargetSHA = batch.View.SourceCommit
			observeDeliveryForTest(t, h, issue.IssueID, merged, "merged", batch.View.SourceCommit)
		}
		if err := h.store.Write(t.Context(), func(tx *store.Tx) error { return tx.PutUpgrade(t.Context(), batch) }); err != nil {
			t.Fatal(err)
		}
		return protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Verified the original controlled installation", Evidence: []string{"upgrade:" + batch.View.UpgradeID}}}
	})
	return h, canonicalTaskIssue(t, h, issue.IssueID), source, result.TaskID, batch
}

func advanceHistoricalDeployment(t *testing.T, h *harness, issue, source string, batch store.UpgradeRow) {
	t.Helper()
	advanceHistoricalDeploymentFacts(t, h, issue, source, batch, true)
}

func advanceHistoricalDeploymentFacts(t *testing.T, h *harness, issue, source string, batch store.UpgradeRow, legacyExpiry bool) {
	t.Helper()
	h.clock.Advance(time.Second)
	maintenance := batch
	maintenance.DeliveryTaskID = ""
	maintenance.View.UpgradeID = "later-platform-maintenance"
	maintenance.View.SourceCommit = strings.Repeat("f", 40)
	maintenance.View.CreatedAt, maintenance.View.UpdatedAt = h.clock.Now(), h.clock.Now()
	maintenance.View.Revision = 24
	duty := deliveryDutyForTest(t, h, issue, source)
	observation := protocol.DeliveryObservation{WorkItemID: issue, DeliveryID: duty.Candidate.DeliveryID, CheckedAt: h.clock.Now().UnixMilli(), Observer: "server", BaselineUpgradeID: maintenance.View.UpgradeID, BaselineUpgradeRevision: maintenance.View.Revision, DeliverySnapshot: protocol.DeliverySnapshot{URL: duty.Candidate.URL, State: "merged", HeadSHA: duty.Candidate.HeadSHA, BaseSHA: duty.Candidate.BaseSHA, MergeSHA: duty.Candidate.MergeSHA, TargetSHA: maintenance.View.SourceCommit, TargetContainsMerge: true}}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.PutUpgrade(t.Context(), maintenance); err != nil {
			return err
		}
		if err := putDeliveryObservationForTest(tx, t.Context(), store.DeliveryObservationRow{Observation: observation}); err != nil {
			return err
		}
		if legacyExpiry {
			// Existing servers already persisted this version transition. Keep
			// that real historical shape covered after future reads stop doing it.
			current, err := store.GetIssueDeliveryDuty(t.Context(), tx.Q(), issue, source)
			if err != nil {
				return err
			}
			if current.Status == "executing" {
				current.Status = "preparing"
				current.Revision++
				return tx.PutIssueDeliveryDuty(t.Context(), issue, current)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestHistoricalDeliveryMaintenanceLeavesBoundResponsibilityForOwnerAssessment(t *testing.T) {
	h, issue, source, execution, batch := historicalDeploymentFixture(t)
	before := deliveryDutyForTest(t, h, issue.IssueID, source)
	advanceHistoricalDeploymentFacts(t, h, issue.IssueID, source, batch, false)
	after := deliveryDutyForTest(t, h, issue.IssueID, source)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("target/baseline drift mechanically changed the bound responsibility", before, after)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		view, err := views.DeliveryExecutionAuthority(t.Context(), tx.Q(), execution)
		if err == nil && (view.Valid || view.Settlement == nil || !view.Settlement.Eligible) {
			t.Fatal("owner assessment restored mutation authority or lost historical completion", view)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	op := updateDeliveryForTest(t, h, issue.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: source, Action: "complete", UpgradeID: batch.View.UpgradeID, IssueControlPayload: protocol.IssueControlPayload{Reason: "The owner assesses the completed original installation after unrelated maintenance"}})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}

func TestHistoricalDeliveryBaselineOnlyChangeNotifiesExistingOwnerWindow(t *testing.T) {
	h, issue, source, _, batch := historicalDeploymentHarnessFixture(t, hostedManagerHarness(t), "merged")
	issue = assignHostedManager(t, h, canonicalTaskIssue(t, h, issue.IssueID))
	h.app.serverDeliveryObservations = true
	duty := deliveryDutyForTest(t, h, issue.IssueID, source)
	// Establish an ordinary shared observation with the original installation
	// as baseline. Then change only the baseline, leaving every PR field equal.
	record := func(refresh bool) {
		t.Helper()
		if refresh {
			current := canonicalTaskIssue(t, h, issue.IssueID)
			payload := protocol.IssueRefreshDeliveryPayload{ExpectedAssignmentEpoch: current.AssignmentEpoch, DeliveryID: duty.Candidate.DeliveryID}
			if op := h.exec(owner, h.env("issues.refresh_delivery", protocol.Target{Type: "issue", ID: issue.IssueID}, payload, rev(current.Revision)), IssueRefreshDelivery{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
		}
		query := claimBaselineRefresh(t, h)
		snapshot := protocol.DeliverySnapshot{URL: duty.Candidate.URL, State: "merged", HeadSHA: duty.Candidate.HeadSHA, BaseSHA: duty.Candidate.BaseSHA, MergeSHA: duty.Candidate.MergeSHA, TargetSHA: duty.Candidate.TargetSHA, TargetContainsMerge: true}
		if err := h.app.RecordServerDelivery(t.Context(), query.ID, &snapshot, ""); err != nil {
			t.Fatal(err)
		}
	}
	record(true)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		// Clear the earlier notification's due marker, preserving its history,
		// so a checked-only result cannot pass as the new baseline notice.
		if _, err := tx.Exec(t.Context(), `UPDATE issue_change_notifications SET due_at=0 WHERE issue_id=?`, issue.IssueID); err != nil {
			return err
		}
		later := batch
		later.View.UpgradeID, later.DeliveryTaskID = "later-platform-maintenance", ""
		later.View.CreatedAt = h.clock.Now().Add(time.Second)
		later.View.Revision++
		if err := tx.PutUpgrade(t.Context(), later); err != nil {
			return err
		}
		// Exercise the same D190 path as a real settled upgrade, rather than
		// letting a separate explicit owner refresh create the notification.
		return refreshDeliveryBaselineQueries(t.Context(), &Exec{Now: h.clock.Now(), IDs: h.ids, serverDeliveryObservations: true}, tx)
	}); err != nil {
		t.Fatal(err)
	}
	record(false)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var changed int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM issue_change_notifications WHERE issue_id=? AND due_at>0 AND kind='work_item.delivery_changed'`, issue.IssueID).Scan(&changed); err != nil {
			return err
		}
		if changed != 1 {
			t.Fatal("baseline-only observation did not enter the owner's existing coalescing window", changed)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := deliveryDutyForTest(t, h, issue.IssueID, source); !reflect.DeepEqual(got, duty) {
		t.Fatal("passive observations replaced the original responsibility", duty, got)
	}
}

func TestHistoricalDeliverySettlesAfterUnrelatedMaintenance(t *testing.T) {
	h, issue, source, execution, batch := historicalDeploymentFixture(t)
	var frozen string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT candidate_json FROM issue_delivery_executions WHERE task_id=?`, execution).Scan(&frozen)
	}); err != nil {
		t.Fatal(err)
	}
	advanceHistoricalDeployment(t, h, issue.IssueID, source, batch)
	duty := deliveryDutyForTest(t, h, issue.IssueID, source)
	if duty.Status != "preparing" || duty.ExecutionTaskID != execution {
		t.Fatal("fixture must retain the actual expired execution", duty)
	}
	tasks, runs, upgrades, acceptances := h.count("tasks"), h.count("runs"), h.count("upgrades"), h.count("acceptances")
	current := canonicalTaskIssue(t, h, issue.IssueID)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		authority, err := views.DeliveryExecutionAuthority(t.Context(), tx.Q(), execution)
		if err != nil {
			return err
		}
		if authority.Valid || authority.Settlement == nil || !authority.Settlement.Eligible || authority.Settlement.UpgradeID != batch.View.UpgradeID || authority.Settlement.TargetSHA != batch.View.SourceCommit || authority.Settlement.AcceptanceID == "" || authority.Settlement.CurrentDutyRevision != duty.Revision || authority.Settlement.DutyRevision >= duty.Revision {
			t.Fatal("historical completion must not revive current execution authority", authority)
		}
		// New execution and physical install remain fenced by the current
		// candidate even when accounting for the original one is eligible.
		_, err = deliveryExecutionAuthorized(t.Context(), tx.Q(), current, protocol.IssueDeliveryAuthorization{TaskID: source, Revision: authority.DutyRevision})
		if protocol.CodeOf(err) != protocol.CodeStaleRevision {
			t.Fatal("historical completion granted another execution", err)
		}
		_, err = checkDeliveryUpgradeStart(t.Context(), &Exec{Now: h.clock.Now()}, tx.Q(), StartUpgrade{DeliveryTaskID: execution})
		if protocol.CodeOf(err) != protocol.CodeInvalidTransition {
			t.Fatal("historical completion granted another installation", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	op := updateDeliveryForTest(t, h, issue.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: source, Action: "complete", UpgradeID: batch.View.UpgradeID, IssueControlPayload: protocol.IssueControlPayload{Reason: "Settle the independently accepted original installation, preserving later maintenance"}})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	settled := deliveryDutyForTest(t, h, issue.IssueID, source)
	if settled.Status != "complete" || settled.ExecutionTaskID != execution || !reflect.DeepEqual(settled.Candidate, duty.Candidate) || !reflect.DeepEqual(settled.Authorization, duty.Authorization) || settled.Revision != duty.Revision+1 || h.count("tasks") != tasks || h.count("runs") != runs || h.count("upgrades") != upgrades || h.count("acceptances") != acceptances {
		t.Fatal("settlement repeated or rewrote completed work", settled)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var after string
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT candidate_json FROM issue_delivery_executions WHERE task_id=?`, execution).Scan(&after); err != nil {
			return err
		}
		if after != frozen {
			t.Fatal("settlement changed the original immutable contract")
		}
		latest, err := store.LatestUpgrade(t.Context(), tx.Q())
		if err == nil && (latest.View.UpgradeID != "later-platform-maintenance" || latest.View.SourceCommit == batch.View.SourceCommit) {
			t.Fatal("settlement rewrote the current deployment", latest.View)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestHistoricalDeliverySettlementCanBeFinishedByCurrentResponsibility(t *testing.T) {
	h, issue, source, execution, batch := historicalDeploymentFixture(t)
	advanceHistoricalDeployment(t, h, issue.IssueID, source, batch)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		current, err := store.GetCanonicalIssue(t.Context(), tx.Q(), issue.IssueID)
		if err != nil {
			return err
		}
		expected := current.Revision
		current.Revision++
		current.AssignmentEpoch++
		return tx.SaveCanonicalIssue(t.Context(), current, expected, store.IssueRevisionProvenance{Actor: owner, RequestID: h.ids.New(protocol.PrefixRequest), Reason: "Current responsibility succeeds the original deployment owner"})
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		view, err := views.DeliveryExecutionAuthority(t.Context(), tx.Q(), execution)
		if err == nil && (view.Valid || view.AssignmentEpoch == view.CurrentAssignmentEpoch || view.Settlement == nil || !view.Settlement.Eligible) {
			t.Fatal("current responsibility could not account for the original history", view)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	op := updateDeliveryForTest(t, h, issue.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: source, Action: "complete", UpgradeID: batch.View.UpgradeID, IssueControlPayload: protocol.IssueControlPayload{Reason: "The current responsible owner settles the original immutable installation"}})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}

func TestHistoricalDeliverySettlementRetainsTypedMergeAndDeploySource(t *testing.T) {
	h, issue, source, execution, batch := historicalDeploymentStateFixture(t, "open")
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM server_delivery_jobs WHERE EXISTS(SELECT 1 FROM server_delivery_references r WHERE r.issue_id=? AND r.provider=server_delivery_jobs.provider AND r.repository=server_delivery_jobs.repository AND r.number=server_delivery_jobs.number)`, issue.IssueID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		settlement, err := store.DeliveryDeploymentSettlement(t.Context(), tx.Q(), execution, 0)
		if err == nil && (!settlement.Eligible || settlement.TargetSHA != batch.View.SourceCommit) {
			t.Fatal("the exact typed completed installation lost its original merged source", settlement)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	op := updateDeliveryForTest(t, h, issue.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: source, Action: "complete", UpgradeID: batch.View.UpgradeID, IssueControlPayload: protocol.IssueControlPayload{Reason: "The typed completed batch preserves its real admission source"}})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}

func TestHistoricalDeliverySettlementKeepsRecordedProofAcrossProviderRefresh(t *testing.T) {
	for _, change := range []string{"refresh_pending", "read_error", "missing_current_read"} {
		t.Run(change, func(t *testing.T) {
			h, issue, source, execution, batch := historicalDeploymentFixture(t)
			advanceHistoricalDeployment(t, h, issue.IssueID, source, batch)
			if change == "refresh_pending" {
				current := canonicalTaskIssue(t, h, issue.IssueID)
				op := h.exec(owner, h.env(protocol.OpRefreshDelivery, workTarget(issue.IssueID), protocol.RefreshDeliveryPayload{DeliveryID: "pr"}, rev(current.Revision)), RefreshDelivery{})
				if op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				switch change {
				case "refresh_pending":
					return nil
				case "read_error":
					row, err := store.GetDeliveryObservation(t.Context(), tx.Q(), issue.IssueID, "pr")
					if err != nil {
						return err
					}
					row.Observation.ErrorKind = "timeout"
					return putDeliveryObservationForTest(tx, t.Context(), row)
				default:
					_, err := tx.Exec(t.Context(), `DELETE FROM server_delivery_jobs WHERE EXISTS(SELECT 1 FROM server_delivery_references r WHERE r.issue_id=? AND r.provider=server_delivery_jobs.provider AND r.repository=server_delivery_jobs.repository AND r.number=server_delivery_jobs.number)`, issue.IssueID)
					return err
				}
			}); err != nil {
				t.Fatal(err)
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				view, err := views.DeliveryExecutionAuthority(t.Context(), tx.Q(), execution)
				if err == nil && (view.Valid || view.Settlement == nil || !view.Settlement.Eligible) {
					t.Fatal("a scheduled current read erased completed installation evidence", view)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			op := updateDeliveryForTest(t, h, issue.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: source, Action: "complete", UpgradeID: batch.View.UpgradeID, IssueControlPayload: protocol.IssueControlPayload{Reason: "Only historical accounting; no new provider mutation or install"}})
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			for _, evidence := range deliveryDutyForTest(t, h, issue.IssueID, source).Evidence {
				if evidence.Kind == protocol.WorkItemEvidenceDelivery {
					t.Fatal("settlement invented a known current provider observation", evidence)
				}
			}
		})
	}
}

func TestHistoricalDeliverySettlementRejectsUnprovedOrReplacedFacts(t *testing.T) {
	for _, change := range []string{"unfinished", "unaccepted", "upgrade_running", "upgrade_unknown", "installed_source", "server_instance", "server_health", "server_process", "hosts", "unconfirmed_receipt", "wrong_receipt", "wrong_bound_task", "wrong_payload_batch", "provider_url", "provider_head", "provider_base", "provider_merge", "provider_state", "source_withdrawn", "source_edited", "decision_reopened", "replacement", "candidate_changed", "grant_changed", "deferred", "historical_epoch"} {
		t.Run(change, func(t *testing.T) {
			h, issue, source, execution, batch := historicalDeploymentFixture(t)
			advanceHistoricalDeployment(t, h, issue.IssueID, source, batch)
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				duty, err := store.GetIssueDeliveryDuty(t.Context(), tx.Q(), issue.IssueID, source)
				if err != nil {
					return err
				}
				switch change {
				case "unfinished":
					_, err = tx.Exec(t.Context(), `UPDATE tasks SET lifecycle='awaiting_acceptance' WHERE task_id=?`, execution)
					return err
				case "unaccepted":
					_, err = tx.Exec(t.Context(), `UPDATE acceptances SET verdict='inconclusive' WHERE task_id=?`, execution)
					return err
				case "source_withdrawn":
					_, err = tx.Exec(t.Context(), `UPDATE conversation_message_controls SET withdrawn_at=1 WHERE message_id IN (SELECT message_id FROM issue_delivery_answers)`)
					return err
				case "source_edited":
					_, err = tx.Exec(t.Context(), `UPDATE message_dispositions SET content_revision=content_revision+1 WHERE message_id IN (SELECT message_id FROM issue_delivery_answers)`)
					return err
				case "decision_reopened":
					_, err = tx.Exec(t.Context(), `UPDATE decisions SET status='open',version=version+1 WHERE decision_id=(SELECT decision_id FROM attentions WHERE attention_id=?)`, duty.AttentionID)
					return err
				case "replacement", "candidate_changed", "grant_changed", "deferred", "historical_epoch":
					switch change {
					case "replacement":
						duty.ExecutionTaskID = source
					case "candidate_changed":
						duty.Candidate.TargetSHA = strings.Repeat("d", 40)
					case "grant_changed":
						duty.Authorization = &protocol.IssueDeliveryGrant{Mode: "auto"}
					case "deferred":
						duty.Status = "deferred"
					case "historical_epoch":
						duty.AssignmentEpoch++
					}
					return tx.PutIssueDeliveryDuty(t.Context(), issue.IssueID, duty)
				case "provider_url", "provider_head", "provider_base", "provider_merge", "provider_state":
					row, err := store.GetDeliveryObservation(t.Context(), tx.Q(), issue.IssueID, "pr")
					if err != nil {
						return err
					}
					switch change {
					case "provider_url":
						row.Observation.URL = "https://example.invalid/repo/pull/18"
					case "provider_head":
						row.Observation.HeadSHA = strings.Repeat("d", 40)
					case "provider_base":
						row.Observation.BaseSHA = strings.Repeat("d", 40)
					case "provider_merge":
						row.Observation.MergeSHA = strings.Repeat("d", 40)
					case "provider_state":
						row.Observation.State = "open"
					}
					return putDeliveryObservationForTest(tx, t.Context(), row)
				case "upgrade_running":
					batch.View.Status = "running"
				case "upgrade_unknown":
					batch.View.Status = "attention"
				case "installed_source":
					batch.View.SourceCommit = strings.Repeat("d", 40)
				case "server_instance":
					batch.ServerInstance = "other"
				case "server_health":
					batch.ServerHealthy = 1
				case "server_process":
					batch.ServerProcess = ""
				case "hosts":
					batch.Hosts[0].HostID = "other"
				case "wrong_bound_task":
					batch.DeliveryTaskID = source
				case "unconfirmed_receipt", "wrong_receipt":
					batch.View.Hosts[0].Status = "completed"
					batch.Hosts[0].Committed = &protocol.UpdaterReceipt{Phase: "committed", CurrentSHA256: batch.Hosts[0].TargetHash, CurrentProcessInstanceID: "installed-runner", Binding: protocol.UpgradeBinding{UpgradeID: batch.View.UpgradeID, ReleaseID: batch.View.ReleaseID, ManifestSHA256: batch.ManifestSHA256, ArtifactSHA256: batch.Hosts[0].TargetHash}}
					if change == "wrong_receipt" {
						batch.Hosts[0].Committed.CommittedConfirmed = true
						batch.Hosts[0].Committed.Binding.UpgradeID = "later-platform-maintenance"
					}
				}
				return tx.PutUpgrade(t.Context(), batch)
			}); err != nil {
				t.Fatal(err)
			}
			if change != "wrong_payload_batch" {
				if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
					settlement, err := store.DeliveryDeploymentSettlement(t.Context(), tx.Q(), execution, 0)
					if settlement != nil && settlement.Eligible || err == nil && change != "replacement" {
						t.Fatal("unproved history became settlement authority", settlement, err)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			id := batch.View.UpgradeID
			if change == "wrong_payload_batch" {
				id = "later-platform-maintenance"
			}
			current := canonicalTaskIssue(t, h, issue.IssueID)
			payload := protocol.UpdateIssueDeliveryPayload{TaskID: source, Action: "complete", UpgradeID: id, IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: current.AssignmentEpoch, Reason: "A historical read cannot manufacture missing completion facts"}}
			op, err := h.app.Execute(t.Context(), owner, h.env("issues.update_delivery", protocol.Target{Type: "issue", ID: issue.IssueID}, payload, rev(current.Revision)), UpdateIssueDelivery{})
			if err != nil && protocol.CodeOf(err) == protocol.CodeInternal {
				t.Fatal(err)
			}
			if err == nil && op.Status == protocol.OpSucceeded || deliveryDutyForTest(t, h, issue.IssueID, source).Status == "complete" {
				t.Fatal("invalid history settled delivery", op, err)
			}
		})
	}
}
