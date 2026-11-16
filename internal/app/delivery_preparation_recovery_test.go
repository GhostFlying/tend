package app

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func stopLegacyDeliveryForTest(t *testing.T, h *harness, as protocol.Assignment) {
	t.Helper()
	if op := h.runAs(as, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "The old runtime stopped before any upgrade or typed preparation result"}, Release{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}

}

func recoverPreparationForTest(t *testing.T, h *harness, issue, task string) protocol.Operation {
	t.Helper()
	return updateDeliveryForTest(t, h, issue, protocol.UpdateIssueDeliveryPayload{Action: "recover_preparation", TaskID: task, ExpectedTaskControlRevision: rev(h.task(task).ControlRevision), IssueControlPayload: protocol.IssueControlPayload{Reason: "Release the proved unstarted old binding and continue the original Task through current result and review APIs"}})
}

func TestLegacyUnstartedDeliveryRecoversThroughRealResultAndCurrentPreparation(t *testing.T) {
	h, r, i, as, next, artifact := predeploymentFixture(t)
	original := deliveryDutyForTest(t, h, i.IssueID, as.TaskID)
	stopLegacyDeliveryForTest(t, h, as)
	before := h.task(as.TaskID)
	if op := recoverPreparationForTest(t, h, i.IssueID, as.TaskID); op.Status != protocol.OpSucceeded {
		t.Fatal("old empty-upgrade binding has no normal recovery", op)
	}
	duty := deliveryDutyForTest(t, h, i.IssueID, as.TaskID)
	if duty.Status != "preparing" || duty.ExecutionTaskID != "" || duty.Authorization != nil || duty.AcceptanceID != "" || duty.ArtifactID != "" || !reflect.DeepEqual(duty.Candidate, original.Candidate) || h.count("issue_delivery_executions") != 0 || h.count("issue_delivery_execution_history") != 1 || h.count("upgrades") != 0 {
		t.Fatal("recovery invented completion, source or installation", duty)
	}
	if after := h.task(as.TaskID); !reflect.DeepEqual(after, before) {
		t.Fatal("coordination recovery changed the original Task", before, after)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var raw string
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT duty_json FROM issue_delivery_execution_history WHERE task_id=?`, as.TaskID).Scan(&raw); err != nil {
			return err
		}
		var saved protocol.IssueDeliveryDuty
		if err := json.Unmarshal([]byte(raw), &saved); err != nil {
			return err
		}
		if !reflect.DeepEqual(saved, original) {
			t.Fatal("old frozen binding was not preserved exactly", saved, original)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if op := h.requestAction(as.TaskID, protocol.ActionImplement, before.ImplementGeneration+1); op.Status != protocol.OpSucceeded {
		t.Fatal("normal retry admission failed", op)
	}
	retry, ok := h.claimInputRun(r)
	if !ok || retry.TaskID != as.TaskID || retry.RunID == as.RunID {
		t.Fatal("normal original Task retry is unavailable", retry)
	}
	// With no active deployment binding this is a genuine ordinary result,
	// not a fabricated stage or upgrade report from the old runtime.
	artifact.Report.Predeployment = nil
	if op := h.runAs(retry, protocol.OpRunsSubmitArtifact, artifact, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal("normal result still requires an invented old upgrade", op)
	}
	acceptStageForTest(t, h, r, as.TaskID)
	if op := continueTaskForTest(t, h, as.TaskID, "Continue the unchanged delivery goal with the checked current-main candidate"); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	preparation, ok := h.claimInputRun(r)
	if !ok || preparation.TaskID != as.TaskID {
		t.Fatal("continuing preparation was not admitted", preparation)
	}
	reviewNextPredeploymentForTest(t, h, r, i, as.TaskID, next)
	prepareLiveDeliveryForTest(t, h, i.IssueID, as.TaskID, next)
	bindSelfCandidateForTest(t, h, i.IssueID, as.TaskID)
	if current := deliveryDutyForTest(t, h, i.IssueID, as.TaskID); current.Status != "executing" || current.Candidate.DeliveryID != next.DeliveryID || current.ExecutionTaskID != as.TaskID || h.count("upgrades") != 0 {
		t.Fatal("normal current candidate binding failed or fabricated installation", current)
	}
}

func TestPreparationRecoveryPreservesEveryUnsettledOriginalBoundary(t *testing.T) {
	for _, scenario := range []string{"active_run", "active_lease", "pending_native", "unknown_native", "bound_upgrade", "unattached_upgrade", "changed_authority", "stale_task", "stale_duty", "typed_stage"} {
		t.Run(scenario, func(t *testing.T) {
			h, _, i, as, _, stage := predeploymentFixture(t)
			if scenario == "typed_stage" {
				if op := h.runAs(as, protocol.OpRunsSubmitArtifact, stage, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				resource := h.resourceForTask(as.TaskID)
				h.releaseResource(as.TaskID, resource.WorkspaceID)
			} else if scenario != "active_run" {
				stopLegacyDeliveryForTest(t, h, as)
			}
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				switch scenario {
				case "active_lease":
					_, err := tx.Exec(t.Context(), `UPDATE leases SET status='active' WHERE run_id=?`, as.RunID)
					return err
				case "pending_native", "unknown_native":
					status := "pending"
					if scenario == "unknown_native" {
						status = "unknown"
					}
					_, err := tx.Exec(t.Context(), `INSERT INTO manager_native_requests(request_id,run_id,method,payload_json,status,created_at) VALUES(?,?,'start','{}',?,?)`, h.ids.New(protocol.PrefixRequest), as.RunID, status, h.clock.Now().UnixMilli())
					return err
				case "bound_upgrade":
					_, err := tx.Exec(t.Context(), `UPDATE issue_delivery_executions SET upgrade_id='original-upgrade' WHERE task_id=?`, as.TaskID)
					return err
				case "unattached_upgrade":
					return tx.PutUpgrade(t.Context(), store.UpgradeRow{DeliveryTaskID: as.TaskID, View: protocol.UpgradeView{UpgradeID: "original-unattached-upgrade", Revision: 1, Status: "attention", Phase: "reconcile", CreatedAt: h.clock.Now(), UpdatedAt: h.clock.Now()}})
				case "stale_duty":
					_, err := tx.Exec(t.Context(), `UPDATE issue_delivery_executions SET duty_revision=duty_revision+1 WHERE task_id=?`, as.TaskID)
					return err
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if scenario == "changed_authority" {
				setPRPolicyForTest(t, h, i.IssueID, "manual")
			}
			before := deliveryDutyForTest(t, h, i.IssueID, as.TaskID)
			payload := protocol.UpdateIssueDeliveryPayload{Action: "recover_preparation", TaskID: as.TaskID, ExpectedTaskControlRevision: rev(h.task(as.TaskID).ControlRevision), IssueControlPayload: protocol.IssueControlPayload{Reason: "Do not clear unresolved original facts"}}
			if scenario == "stale_task" {
				payload.ExpectedTaskControlRevision = rev(*payload.ExpectedTaskControlRevision + 1)
			}
			currentIssue := canonicalTaskIssue(t, h, i.IssueID)
			payload.ExpectedAssignmentEpoch = currentIssue.AssignmentEpoch
			op, err := h.app.Execute(t.Context(), owner, h.env("issues.update_delivery", protocol.Target{Type: "issue", ID: i.IssueID}, payload, rev(currentIssue.Revision)), UpdateIssueDelivery{})
			if err == nil && op.Status != protocol.OpFailed || h.count("issue_delivery_execution_history") != 0 || !reflect.DeepEqual(deliveryDutyForTest(t, h, i.IssueID, as.TaskID), before) {
				t.Fatal("unsafe recovery changed original facts", op)
			}
		})
	}
}

func TestPreparationRecoveryRetainsUnknownObservationsWithoutBlockingBusiness(t *testing.T) {
	for _, method := range []string{"models", "find", "usage"} {
		t.Run(method, func(t *testing.T) {
			h, _, issue, as, _, _ := predeploymentFixture(t)
			stopLegacyDeliveryForTest(t, h, as)
			request := h.ids.New(protocol.PrefixRequest)
			// Persist an observation-only old transport outcome at the failure
			// boundary; the actual business recovery still uses the normal API.
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				_, err := tx.Exec(t.Context(), `INSERT INTO manager_native_requests(request_id,run_id,method,payload_json,status,created_at) VALUES(?,?,?,'{}','unknown',?)`, request, as.RunID, method, h.clock.Now().UnixMilli())
				return err
			}); err != nil {
				t.Fatal(err)
			}
			beforeRuns, beforeTasks := h.count("runs"), h.count("tasks")
			if op := recoverPreparationForTest(t, h, issue.IssueID, as.TaskID); op.Status != protocol.OpSucceeded {
				t.Fatal("observation-only uncertainty blocked normal recovery", op)
			}
			if h.count("runs") != beforeRuns || h.count("tasks") != beforeTasks || h.count("upgrades") != 0 {
				t.Fatal("recovery replayed work to resolve an unknown read")
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				var status string
				if err := tx.Q().QueryRowContext(t.Context(), `SELECT status FROM manager_native_requests WHERE request_id=?`, request).Scan(&status); err != nil {
					return err
				}
				if status != "unknown" {
					t.Fatal("unknown original read was rewritten", status)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
