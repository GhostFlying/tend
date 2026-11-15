package app

import (
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestPredeploymentNegativeStageRetainsRepairAfterMainAdvances(t *testing.T) {
	h, r, i, as, _, stage := predeploymentFixture(t)
	original := *deliveryDutyForTest(t, h, i.IssueID, as.TaskID).Candidate
	original.TargetSHA = strings.Repeat("e", 40)
	observeDeliveryForTest(t, h, i.IssueID, original, "merged", strings.Repeat("c", 40))
	if op := h.runAs(as, protocol.OpRunsSubmitArtifact, stage, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	accept, ok := h.claimInputRun(r)
	if !ok || accept.Role != protocol.RoleAccept {
		t.Fatal(accept)
	}
	if op := h.submitVerdict(accept.RunID, protocol.VerdictChangesRequired); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if task := h.task(as.TaskID); task.Lifecycle != protocol.TaskChangesRequired || task.CurrentArtifactID != "" {
		t.Fatal("negative grade was lost", task)
	}
	repair, ok := h.claimInputRun(r)
	if !ok || repair.TaskID != as.TaskID || repair.Feedback == nil || repair.Feedback.Verdict != protocol.VerdictChangesRequired {
		t.Fatal("real stage correction cannot start", repair, ok)
	}
	task := h.task(as.TaskID)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		if err := requireInstallationStage(t.Context(), tx.Q(), task, original); protocol.CodeOf(err) != protocol.CodeInvalidTransition {
			t.Fatal("negative preparation acquired installation permission", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPredeploymentInstallationRechecksWholePRAndStageAtWriteBoundary(t *testing.T) {
	for _, scenario := range []string{"unlinked", "failed-ci", "missing-ci", "unknown-ci", "negative-stage", "passed"} {
		t.Run(scenario, func(t *testing.T) {
			h, i, as, command := autoDeploymentLifecycleFixture(t, false)
			var rt *protocol.RunnerRuntime
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				x, err := store.GetHostRuntime(t.Context(), tx.Q(), "linux-1")
				rt = &x.Runtime
				return err
			}); err != nil {
				t.Fatal(err)
			}
			duty := deliveryDutyForTest(t, h, i.IssueID, as.TaskID)
			stage := testArtifact(h.task(as.TaskID).WorkspaceName)
			stage.Mounts[0].CommitSHA = duty.Candidate.HeadSHA
			stage.Report = &protocol.ResultReport{Body: "Candidate preparation, not installation", Predeployment: &protocol.PredeploymentResult{DeliveryID: duty.Candidate.DeliveryID, HeadSHA: duty.Candidate.HeadSHA, BaseSHA: duty.Candidate.BaseSHA}}
			if op := h.runAs(as, protocol.OpRunsSubmitArtifact, stage, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if scenario == "negative-stage" {
				accept, ok := h.claimInputRun(rt)
				if !ok || accept.Role != protocol.RoleAccept {
					t.Fatal(accept)
				}
				if op := h.submitVerdict(accept.RunID, protocol.VerdictChangesRequired); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			} else {
				acceptStageForTest(t, h, rt, as.TaskID)
				if scenario == "unlinked" {
					sharedReviewResult(t, sharedReviewAction(t, h, i.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "unlink", DeliveryID: duty.Candidate.DeliveryID, TaskID: as.TaskID}))
				}
				if strings.HasSuffix(scenario, "ci") {
					if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
						o, err := store.GetDeliveryObservation(t.Context(), tx.Q(), i.IssueID, duty.Candidate.DeliveryID)
						if err != nil {
							return err
						}
						switch scenario {
						case "failed-ci":
							o.Observation.CI.State = "failure"
						case "missing-ci":
							o.Observation.CI = nil
						case "unknown-ci":
							o.Observation.CI.ErrorKind = "unavailable"
						}
						return putDeliveryObservationForTest(tx, t.Context(), o)
					}); err != nil {
						t.Fatal(err)
					}
				}
				if op := continueTaskForTest(t, h, as.TaskID, "Prepare under the retained responsibility"); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			next, ok := h.claimInputRun(rt)
			if !ok || next.TaskID != as.TaskID {
				t.Fatal("preparation must remain possible", next, ok)
			}
			if op := h.runAs(next, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			env := h.env(command.Operation(), protocol.Target{Type: "upgrade", ID: "review-install"}, map[string]string{"release_id": command.Manifest.ReleaseID}, rev(0))
			_, preflight := h.app.CheckUpgradeStart(t.Context(), env, command)
			before := h.count("upgrades")
			op, err := h.app.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalSystem}, env, command)
			if err == nil && op.Error != nil {
				err = op.Error
			}
			if scenario == "passed" {
				if preflight != nil || err != nil || op.Status != protocol.OpSucceeded || h.count("upgrades") != before+1 {
					t.Fatal("legitimate installation blocked", preflight, op, err)
				}
			} else if protocol.CodeOf(preflight) != protocol.CodeInvalidTransition || protocol.CodeOf(err) != protocol.CodeInvalidTransition || h.count("upgrades") != before {
				t.Fatal("actual installation bypassed current gate", scenario, preflight, op, err)
			}
		})
	}
}
