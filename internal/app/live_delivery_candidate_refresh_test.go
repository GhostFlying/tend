package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/store"
)

func bindSelfCandidateForTest(t *testing.T, h *harness, issue, task string) protocol.IssueDeliveryDuty {
	t.Helper()
	if op := updateDeliveryForTest(t, h, issue, protocol.UpdateIssueDeliveryPayload{Action: "bind", TaskID: task, ExecutionTaskID: task, ExpectedTaskControlRevision: rev(h.task(task).ControlRevision), IssueControlPayload: protocol.IssueControlPayload{Reason: "Keep delivery in its original active responsibility"}}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	return deliveryDutyForTest(t, h, issue, task)
}

func observeSelfRefreshTarget(t *testing.T, h *harness, issue string, candidate protocol.IssueDeliveryCandidate) protocol.IssueDeliveryCandidate {
	t.Helper()
	candidate.TargetSHA = strings.Repeat("d", 40)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		row, err := store.GetDeliveryObservation(t.Context(), tx.Q(), issue, candidate.DeliveryID)
		if err != nil {
			return err
		}
		row.Observation.TargetSHA = candidate.TargetSHA
		return putDeliveryObservationForTest(tx, t.Context(), row)
	}); err != nil {
		t.Fatal(err)
	}
	return candidate
}

func TestFault_LiveSelfDeliveryRefreshPreservesResponsibility(t *testing.T) {
	for _, artifact := range []bool{false, true} {
		t.Run(map[bool]string{false: "without-artifact", true: "owner-with-artifact"}[artifact], func(t *testing.T) {
			var h *harness
			var issue protocol.Issue
			var as protocol.Assignment
			var candidate protocol.IssueDeliveryCandidate
			if artifact {
				h, issue, as, candidate, _ = ownerBoundDeliveryFixture(t, false)
			} else {
				h, _, issue, as, candidate = liveReviewedDeliveryFixture(t)
				prepareLiveDeliveryForTest(t, h, issue.IssueID, as.TaskID, candidate)
				bindSelfCandidateForTest(t, h, issue.IssueID, as.TaskID)
			}
			before := h.task(as.TaskID)
			original := deliveryDutyForTest(t, h, issue.IssueID, as.TaskID)
			tasks, runs, artifacts := h.count("tasks"), h.count("runs"), h.count("artifacts")
			prose := candidate
			prose.Validation = "Clarified wording for the same existing evidence"
			if op := updateDeliveryForTest(t, h, issue.IssueID, protocol.UpdateIssueDeliveryPayload{Action: "prepare", TaskID: as.TaskID, ExpectedTaskControlRevision: rev(before.ControlRevision), Candidate: &prose, IssueControlPayload: protocol.IssueControlPayload{Reason: "Read the same candidate without superseding it"}}); op.Status != protocol.OpSucceeded || h.count("issue_delivery_execution_history") != 0 || !reflect.DeepEqual(original, deliveryDutyForTest(t, h, issue.IssueID, as.TaskID)) {
				t.Fatal("same candidate or prose-only retry replaced the live binding", op)
			}
			candidate = observeSelfRefreshTarget(t, h, issue.IssueID, candidate)
			op := updateDeliveryForTest(t, h, issue.IssueID, protocol.UpdateIssueDeliveryPayload{Action: "prepare", TaskID: as.TaskID, ExpectedTaskControlRevision: rev(before.ControlRevision), Candidate: &candidate, IssueControlPayload: protocol.IssueControlPayload{Reason: "Rebase target changed before any merge or installation; continue the same responsibility"}})
			if op.Status != protocol.OpSucceeded {
				t.Fatal("same responsibility cannot refresh its unexecuted candidate", op)
			}
			current := deliveryDutyForTest(t, h, issue.IssueID, as.TaskID)
			if current.ExecutionTaskID != "" || current.Candidate.TargetSHA != candidate.TargetSHA || current.Revision <= original.Revision || h.count("issue_delivery_execution_history") != 1 || h.count("issue_delivery_executions") != 0 {
				t.Fatal("refresh did not preserve old execution separately", current)
			}
			after := h.task(as.TaskID)
			if after.ControlRevision != before.ControlRevision || after.SpecVersion != before.SpecVersion || after.Lifecycle != before.Lifecycle || after.CurrentArtifactID != before.CurrentArtifactID || h.count("tasks") != tasks || h.count("runs") != runs || h.count("artifacts") != artifacts || h.lease(as.RunID).Status != store.LeaseActive {
				t.Fatal("candidate refresh fabricated execution, result or resource release")
			}
			selected, err := h.app.ReadIssueMaterials(t.Context(), issue.IssueID, protocol.IssueMaterialsQuery{Kind: "delivery_authorization", ID: as.TaskID, DutyRevision: original.Revision})
			if err != nil || selected.DeliveryAuthorization == nil {
				t.Fatal("fresh session cannot read the old same-Task snapshot", selected, err)
			}
			history := selected.DeliveryAuthorization
			if !history.Historical || history.Valid || history.DutyStatus != "executing" || history.Candidate.TargetSHA != original.Candidate.TargetSHA || history.Settlement == nil || history.Settlement.Eligible {
				t.Fatal("old candidate became execution or settlement authority", history)
			}
			if artifact {
				if current.Status != "awaiting_owner" || current.AttentionID == original.AttentionID || current.Authorization == nil || current.Authorization.Mode != "manual" {
					t.Fatal("manual approval was silently copied to a new candidate", current)
				}
				answerDeliveryForTest(t, h, current, "approve", "")
			}
			bindSelfCandidateForTest(t, h, issue.IssueID, as.TaskID)
			currentIndex, err := h.app.ReadIssueMaterials(t.Context(), issue.IssueID, protocol.IssueMaterialsQuery{Kind: "delivery_authorization", ID: as.TaskID})
			if err != nil || currentIndex.DeliveryAuthorization == nil || !currentIndex.DeliveryAuthorization.Valid || currentIndex.DeliveryAuthorization.Historical || currentIndex.DeliveryAuthorization.Candidate.TargetSHA != candidate.TargetSHA {
				t.Fatal("current authority did not select only the rebound candidate", currentIndex, err)
			}
		})
	}
}

func TestLiveSelfDeliveryRefreshRollsBackOnInvalidFacts(t *testing.T) {
	for _, scenario := range []string{"task-cas", "missing-new-review", "missing-checks", "original-merged", "bound-upgrade", "unattached-upgrade", "unknown-native-effect"} {
		t.Run(scenario, func(t *testing.T) {
			h, issue, as, candidate, _ := ownerBoundDeliveryFixture(t, false)
			candidate = observeSelfRefreshTarget(t, h, issue.IssueID, candidate)
			p := protocol.UpdateIssueDeliveryPayload{Action: "prepare", TaskID: as.TaskID, ExpectedTaskControlRevision: rev(h.task(as.TaskID).ControlRevision), Candidate: &candidate, IssueControlPayload: protocol.IssueControlPayload{Reason: "Refresh only if the original external outcome and current evidence permit it"}}
			code := protocol.CodeUnknownOutcome
			switch scenario {
			case "task-cas":
				p.ExpectedTaskControlRevision = rev(h.task(as.TaskID).ControlRevision + 1)
				code = protocol.CodeStaleRevision
			case "missing-new-review":
				candidate.HeadSHA = strings.Repeat("e", 40)
				observeDeliveryForTest(t, h, issue.IssueID, candidate, "open", "")
				code = protocol.CodeInvalidTransition
			case "missing-checks":
				issue = setPRPolicyForTest(t, h, issue.IssueID, "auto")
				observeDeliveryForTest(t, h, issue.IssueID, candidate, "open", "")
				code = protocol.CodeInvalidTransition
			case "original-merged":
				candidate.ProviderState, candidate.MergeSHA = "merged", strings.Repeat("c", 40)
				candidate.Deploy, candidate.Deployment = true, &protocol.IssueDeliveryDeployment{ServerInstance: "server", HostIDs: []string{}}
				observeDeliveryForTest(t, h, issue.IssueID, candidate, "merged", candidate.MergeSHA)
			case "bound-upgrade", "unattached-upgrade":
				batch := store.UpgradeRow{DeliveryTaskID: as.TaskID, View: protocol.UpgradeView{UpgradeID: "actual-original-install", Revision: 1, Status: "attention", Phase: "server_reconcile", CreatedAt: h.clock.Now(), UpdatedAt: h.clock.Now()}}
				if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
					if err := tx.PutUpgrade(t.Context(), batch); err != nil {
						return err
					}
					if scenario == "bound-upgrade" {
						_, err := tx.Exec(t.Context(), `UPDATE issue_delivery_executions SET upgrade_id=? WHERE task_id=?`, batch.View.UpgradeID, as.TaskID)
						return err
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			case "unknown-native-effect":
				if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
					_, err := tx.Exec(t.Context(), `INSERT INTO manager_native_requests(request_id,run_id,method,payload_json,status,created_at) VALUES(?,?,'turn','{}','unknown',?)`, h.ids.New(protocol.PrefixRequest), as.RunID, h.clock.Now().UnixMilli())
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			original := deliveryDutyForTest(t, h, issue.IssueID, as.TaskID)
			issue = canonicalTaskIssue(t, h, issue.IssueID)
			p.ExpectedAssignmentEpoch = issue.AssignmentEpoch
			op, err := h.app.Execute(t.Context(), owner, h.env("issues.update_delivery", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(issue.Revision)), UpdateIssueDelivery{})
			if err == nil {
				err = op.Error
			}
			if protocol.CodeOf(err) != code || h.count("issue_delivery_execution_history") != 0 || h.count("issue_delivery_executions") != 1 || !reflect.DeepEqual(original, deliveryDutyForTest(t, h, issue.IssueID, as.TaskID)) {
				t.Fatal("invalid refresh changed the original binding or outcome", op, err)
			}
		})
	}
}

func TestLiveSelfDeliveryRefreshRetainsStoppedExecutionAndAuxiliaryACK(t *testing.T) {
	h, issue, as, candidate, _ := ownerBoundDeliveryFixture(t, false)
	stopOwnerClaim(t, h, as)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO manager_native_requests(request_id,run_id,method,payload_json,status,coordinator_consumed,runner_acked,created_at) VALUES(?,?,'close','{}','completed',1,0,?)`, h.ids.New(protocol.PrefixRequest), as.RunID, h.clock.Now().UnixMilli())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	before := h.run(as.RunID)
	candidate = observeSelfRefreshTarget(t, h, issue.IssueID, candidate)
	prepareLiveDeliveryForTest(t, h, issue.IssueID, as.TaskID, candidate)
	if !reflect.DeepEqual(before, h.run(as.RunID)) || h.lease(as.RunID).Status != store.LeaseReleased || h.countWhere(`SELECT COUNT(*) FROM manager_native_requests WHERE run_id=? AND method='close' AND runner_acked=0`, as.RunID) != 1 {
		t.Fatal("refresh rewrote physical stop or auxiliary unacknowledged close facts")
	}
}

func TestLiveSelfDeliveryRefreshIgnoresOnlyArchivedInstallations(t *testing.T) {
	h, issue, as, candidate, _ := ownerBoundDeliveryFixture(t, false)
	// The original accepted installation and logical delivery settle through
	// their actual endpoint before the Task publishes its next real result.
	batch := store.UpgradeRow{DeliveryTaskID: as.TaskID, View: protocol.UpgradeView{UpgradeID: "earlier-settled-installation", Revision: 3, Status: "completed", Phase: "completed", CreatedAt: h.clock.Now(), UpdatedAt: h.clock.Now()}}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.PutUpgrade(t.Context(), batch); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE issue_delivery_executions SET upgrade_id=? WHERE task_id=?`, batch.View.UpgradeID, as.TaskID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	endDeliveryForTest(t, h, issue.IssueID, as.TaskID)
	if op := updateDeliveryForTest(t, h, issue.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: as.TaskID, RunID: as.RunID, Action: "submit_result", Result: &protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "A later actual result of the same continuing responsibility"}}, IssueControlPayload: protocol.IssueControlPayload{Reason: "Keep the historical installation separate from the next unexecuted candidate"}}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	d := prepareLiveDeliveryForTest(t, h, issue.IssueID, as.TaskID, candidate)
	answerDeliveryForTest(t, h, d, "approve", "")
	bindSelfCandidateForTest(t, h, issue.IssueID, as.TaskID)
	candidate = observeSelfRefreshTarget(t, h, issue.IssueID, candidate)
	prepareLiveDeliveryForTest(t, h, issue.IssueID, as.TaskID, candidate)
	if h.count("upgrades") != 1 || h.count("issue_delivery_execution_history") != 2 || h.countWhere(`SELECT COUNT(*) FROM issue_delivery_execution_history WHERE upgrade_id=?`, batch.View.UpgradeID) != 1 {
		t.Fatal("a past archived installation prevented or was duplicated by the next candidate")
	}
}

func TestLiveDeliveryUpgradeUsesActualCurrentLease(t *testing.T) {
	for _, scenario := range []string{"current-owner", "expired", "released", "stopped", "receipt-only", "withdrawn-owner-source"} {
		t.Run(scenario, func(t *testing.T) {
			h, issue, as, candidate, _ := ownerBoundDeliveryFixture(t, true)
			command, err := h.app.QueueManagerNative(t.Context(), as.RunID, "turn", json.RawMessage(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			if op := h.runAs(as, protocol.OpManagerNativeClaim, map[string]any{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if h.run(as.RunID).ObservedState != protocol.RunRunning {
				t.Fatal("the actual native start was not observed", command)
			}
			candidate.TargetSHA = strings.Repeat("c", 40)
			observeDeliveryForTest(t, h, issue.IssueID, candidate, "merged", candidate.TargetSHA)
			switch scenario {
			case "expired":
				h.clock.Advance(2 * h.app.LeaseTTL())
			case "released", "stopped", "receipt-only":
				if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
					switch scenario {
					case "released":
						return tx.SetLease(t.Context(), as.LeaseID, store.LeaseActive, store.LeaseReleased, 0, h.clock.Now().UnixMilli())
					case "stopped":
						_, err := tx.Exec(t.Context(), `UPDATE runs SET desired_state='stopped' WHERE run_id=?`, as.RunID)
						return err
					default:
						_, err := tx.Exec(t.Context(), `UPDATE plan_actions SET trigger_json=json_set(trigger_json,'$.receipt_only',1) WHERE action_id=?`, as.ActionID)
						return err
					}
				}); err != nil {
					t.Fatal(err)
				}
			case "withdrawn-owner-source":
				var source string
				if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
					m, err := store.GetIssueManagerTask(t.Context(), tx.Q(), as.TaskID)
					source = m.SourceMessageID
					return err
				}); err != nil {
					t.Fatal(err)
				}
				withdrawOwnerSource(t, h, source)
			}
			hash := strings.Repeat("e", 64)
			err = h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				_, err := checkDeliveryUpgradeStart(t.Context(), &Exec{Now: h.clock.Now()}, tx.Q(), StartUpgrade{DeliveryTaskID: as.TaskID, ServerInstance: "server", ServerHash: hash, ArtifactSources: map[string]release.SourceIdentity{hash: {Commit: candidate.TargetSHA}}})
				return err
			})
			if scenario == "current-owner" && err != nil || scenario != "current-owner" && protocol.CodeOf(err) != protocol.CodeForbidden {
				t.Fatal("upgrade admission did not follow the real current execution lease and ownership", scenario, err)
			}
		})
	}
}
