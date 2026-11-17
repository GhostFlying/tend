package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/store"
)

// The real owner submits both results under its running native lease. The
// manifest is verified-boundary fixture data, never a physical installation.
func TestPredeploymentOwnerProgressRetainsPersistentStageGate(t *testing.T) {
	for _, scenario := range []string{"no-stage", "unaccepted", "waived", "new-unaccepted-after-waived"} {
		t.Run(scenario, func(t *testing.T) {
			h, i, as, candidate, progress := ownerBoundDeliveryFixture(t, true)
			if _, err := h.app.QueueManagerNative(t.Context(), as.RunID, "turn", json.RawMessage(`{}`)); err != nil {
				t.Fatal(err)
			}
			if op := h.runAs(as, protocol.OpManagerNativeClaim, map[string]any{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			stage := testArtifact(h.task(as.TaskID).WorkspaceName)
			stage.Mounts[0].CommitSHA = candidate.HeadSHA
			stage.Report = &protocol.ResultReport{Body: "Actual candidate stage", Predeployment: &protocol.PredeploymentResult{DeliveryID: candidate.DeliveryID, HeadSHA: candidate.HeadSHA, BaseSHA: candidate.BaseSHA}}
			submit := func(required bool) string {
				t.Helper()
				op := updateDeliveryForTest(t, h, i.IssueID, protocol.UpdateIssueDeliveryPayload{Action: "submit_result", TaskID: as.TaskID, RunID: as.RunID, Result: &stage, IndependentAcceptance: &protocol.IndependentAcceptanceRequest{Required: required, Reason: "Freeze this actual stage's quality agreement"}, IssueControlPayload: protocol.IssueControlPayload{Reason: "Record actual stage under the retained deployment"}})
				if op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				return h.task(as.TaskID).CurrentArtifactID
			}
			stageID := ""
			if scenario != "no-stage" {
				stageID = submit(scenario == "unaccepted")
				if scenario == "new-unaccepted-after-waived" {
					stage.Report.Body = "A newer stage still needs its own independent review"
					stageID = submit(true)
				}
			}
			merged := strings.Repeat("c", 40)
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				row, err := store.GetDeliveryObservation(t.Context(), tx.Q(), i.IssueID, candidate.DeliveryID)
				if err != nil {
					return err
				}
				row.Observation.State, row.Observation.MergeSHA = "merged", merged
				row.Observation.TargetSHA, row.Observation.TargetContainsMerge = merged, true
				return putDeliveryObservationForTest(tx, t.Context(), row)
			}); err != nil {
				t.Fatal(err)
			}
			hash, runnerHash := strings.Repeat("e", 64), strings.Repeat("f", 64)
			manifest := release.Manifest{Format: 1, ReleaseID: "owner-stage-gate", Protocol: release.Range{Min: protocol.MinimumRunnerProtocol, Max: protocol.CurrentRunnerProtocol}, Storage: release.Storage{ServerSchema: release.Range{Min: 1, Max: 9999}, RunnerJournal: release.Range{Min: 9, Max: 10}}, Artifacts: []release.Artifact{{Role: "server", OS: "linux", Arch: "amd64", Path: "server", Size: 20, SHA256: hash}, {Role: "runner", OS: "linux", Arch: "amd64", Path: "runner", Size: 10, SHA256: runnerHash}}}
			command := StartUpgrade{DeliveryTaskID: as.TaskID, ServerInstance: "server", ServerHash: hash, Manifest: manifest, ManifestSHA256: strings.Repeat("d", 64), ManifestSize: 128, ArtifactSources: map[string]release.SourceIdentity{hash: {Commit: merged}}}
			rejected := scenario == "unaccepted" || scenario == "new-unaccepted-after-waived"
			check := func() {
				t.Helper()
				env := h.env(command.Operation(), protocol.Target{Type: "upgrade", ID: "owner-report-install"}, map[string]string{"release_id": manifest.ReleaseID}, rev(0))
				_, err := h.app.CheckUpgradeStart(t.Context(), env, command)
				if !rejected {
					if err != nil {
						t.Fatal("valid legacy or waived stage rejected", err)
					}
					return
				}
				if protocol.CodeOf(err) != protocol.CodeInvalidTransition || !strings.Contains(err.Error(), "owner stage needs its independent review") {
					t.Fatal("original stage requirement bypassed", err)
				}
				before := h.count("upgrades")
				op, err := h.app.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalSystem}, env, command)
				if err == nil && op.Error != nil {
					err = op.Error
				}
				if protocol.CodeOf(err) != protocol.CodeInvalidTransition || !strings.Contains(err.Error(), "owner stage needs its independent review") || h.count("upgrades") != before {
					t.Fatal("actual installation write bypassed stage", op, err)
				}
			}
			check()
			progress("Ordinary owner progress does not pass or erase a candidate stage")
			progress("A second ordinary report still preserves the original stage agreement")
			if stageID != "" && h.task(as.TaskID).CurrentArtifactID == stageID {
				t.Fatal("fixture did not replace the lifecycle pointer")
			}
			check()
		})
	}
}

func TestPredeploymentServerOnlyScopeContinuesOriginalCandidate(t *testing.T) {
	h, r, i, as, next, stage := predeploymentFixture(t, []string{})
	if op := h.runAs(as, protocol.OpRunsSubmitArtifact, stage, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	acceptStageForTest(t, h, r, as.TaskID)
	if op := continueTaskForTest(t, h, as.TaskID, "Continue the identical server-only scope"); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	current, ok := h.claimInputRun(r)
	if !ok || current.TaskID != as.TaskID {
		t.Fatal(current, ok)
	}
	if op := h.runAs(current, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	reviewNextPredeploymentForTest(t, h, r, i, as.TaskID, next)
	op := updateDeliveryForTest(t, h, i.IssueID, protocol.UpdateIssueDeliveryPayload{Action: "prepare", TaskID: as.TaskID, Candidate: &next, ExpectedTaskControlRevision: rev(h.task(as.TaskID).ControlRevision), IssueControlPayload: protocol.IssueControlPayload{Reason: "Retain the identical complete server-only scope"}})
	if op.Status != protocol.OpSucceeded {
		t.Fatal("identical server-only continuation rejected", op)
	}
	duty := deliveryDutyForTest(t, h, i.IssueID, as.TaskID)
	if duty.Candidate.Deployment.HostIDs == nil || len(duty.Candidate.Deployment.HostIDs) != 0 || h.count("issue_delivery_execution_history") != 1 {
		t.Fatal("server-only scope or original history changed", duty)
	}
}
