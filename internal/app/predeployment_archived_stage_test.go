package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/store"
)

func TestPredeploymentEndedStageDoesNotGateLaterBinding(t *testing.T) {
	for _, oldStage := range []bool{false, true} {
		t.Run(map[bool]string{false: "without-old-stage", true: "with-ended-old-stage"}[oldStage], func(t *testing.T) {

			h, i, as, candidate, progress := ownerBoundDeliveryFixture(t, true)
			if _, err := h.app.QueueManagerNative(t.Context(), as.RunID, "turn", json.RawMessage(`{}`)); err != nil {
				t.Fatal(err)
			}
			if op := h.runAs(as, protocol.OpManagerNativeClaim, map[string]any{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if oldStage {
				stage := testArtifact(h.task(as.TaskID).WorkspaceName)
				stage.Mounts[0].CommitSHA = candidate.HeadSHA
				stage.Report = &protocol.ResultReport{Body: "Old real stage", Predeployment: &protocol.PredeploymentResult{DeliveryID: candidate.DeliveryID, HeadSHA: candidate.HeadSHA, BaseSHA: candidate.BaseSHA}}
				if op := updateDeliveryForTest(t, h, i.IssueID, protocol.UpdateIssueDeliveryPayload{Action: "submit_result", TaskID: as.TaskID, RunID: as.RunID, Result: &stage, IndependentAcceptance: &protocol.IndependentAcceptanceRequest{Required: false, Reason: "Owner quality choice"}, IssueControlPayload: protocol.IssueControlPayload{Reason: "Save old stage"}}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			endDeliveryForTest(t, h, i.IssueID, as.TaskID)
			progress("Continue actual owner work after explicit settled disposition")
			if h.count("issue_delivery_execution_history") != 1 || h.count("issue_delivery_executions") != 0 {
				t.Fatal("old binding was not archived")
			}
			next := candidate
			next.DeliveryID, next.HeadSHA = "next", strings.Repeat("d", 40)
			i = canonicalTaskIssue(t, h, i.IssueID)
			i.Plan.Deliveries = append(i.Plan.Deliveries, protocol.IssueDelivery{DeliveryID: "next", Provider: "github", Repository: "example/repo", Number: 18, URL: "https://example.invalid/repo/pull/18"})
			i = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, i, "update", protocol.UpdateIssuePayload{Plan: i.Plan, ExpectedAssignmentEpoch: i.AssignmentEpoch}))
			next.URL = i.Plan.Deliveries[1].URL
			observeDeliveryForTest(t, h, i.IssueID, next, "open", "")
			var rt protocol.RunnerRuntime
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				host, err := store.GetHostRuntime(t.Context(), tx.Q(), "linux-1")
				rt = host.Runtime
				return err
			}); err != nil {
				t.Fatal(err)
			}
			reviewNextOwnerCandidateForTest(t, h, &rt, i, as.TaskID, next)
			duty := prepareLiveDeliveryForTest(t, h, i.IssueID, as.TaskID, next)
			answerDeliveryForTest(t, h, duty, "approve", "")
			bindSelfCandidateForTest(t, h, i.IssueID, as.TaskID)
			merged := strings.Repeat("c", 40)
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				o, err := store.GetDeliveryObservation(t.Context(), tx.Q(), i.IssueID, next.DeliveryID)
				if err != nil {
					return err
				}
				o.Observation.State, o.Observation.MergeSHA = "merged", merged
				o.Observation.TargetSHA, o.Observation.TargetContainsMerge = merged, true
				return putDeliveryObservationForTest(tx, t.Context(), o)
			}); err != nil {
				t.Fatal(err)
			}
			hash, runnerHash := strings.Repeat("e", 64), strings.Repeat("f", 64)
			manifest := release.Manifest{Format: 1, ReleaseID: "next-owner-install", Protocol: release.Range{Min: protocol.PreviousRunnerProtocol, Max: protocol.CurrentRunnerProtocol}, Storage: release.Storage{ServerSchema: release.Range{Min: 1, Max: 9999}, RunnerJournal: release.Range{Min: 9, Max: 10}}, Artifacts: []release.Artifact{{Role: "server", OS: "linux", Arch: "amd64", Path: "server", Size: 20, SHA256: hash}, {Role: "runner", OS: "linux", Arch: "amd64", Path: "runner", Size: 10, SHA256: runnerHash}}}
			command := StartUpgrade{DeliveryTaskID: as.TaskID, ServerInstance: "server", ServerHash: hash, Manifest: manifest, ManifestSHA256: strings.Repeat("d", 64), ManifestSize: 128, ArtifactSources: map[string]release.SourceIdentity{hash: {Commit: merged}}}
			env := h.env(command.Operation(), protocol.Target{Type: "upgrade", ID: "next-owner-install"}, map[string]string{"release_id": manifest.ReleaseID}, rev(0))
			if _, err := h.app.CheckUpgradeStart(t.Context(), env, command); err != nil {
				t.Fatal("ended historical stage gates real later binding at upgrade preflight", err)
			}
			// A stage in the new responsibility still retains its own quality gate.
			currentStage := testArtifact(h.task(as.TaskID).WorkspaceName)
			currentStage.Mounts[0].CommitSHA = next.HeadSHA
			currentStage.Report = &protocol.ResultReport{Body: "This new stage still requires independent review", Predeployment: &protocol.PredeploymentResult{DeliveryID: next.DeliveryID, HeadSHA: next.HeadSHA, BaseSHA: next.BaseSHA}}
			if op := updateDeliveryForTest(t, h, i.IssueID, protocol.UpdateIssueDeliveryPayload{Action: "submit_result", TaskID: as.TaskID, RunID: as.RunID, Result: &currentStage, IndependentAcceptance: &protocol.IndependentAcceptanceRequest{Required: true, Reason: "Freeze the new responsibility's independent requirement"}, IssueControlPayload: protocol.IssueControlPayload{Reason: "Save the new actual stage before installation"}}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			progress("Ordinary progress cannot erase the new responsibility's stage")
			if _, err := h.app.CheckUpgradeStart(t.Context(), env, command); protocol.CodeOf(err) != protocol.CodeInvalidTransition || !strings.Contains(err.Error(), "owner stage needs its independent review") {
				t.Fatal("new current stage gate lost", err)
			}
			before := h.count("upgrades")
			op, err := h.app.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalSystem}, env, command)
			if err == nil && op.Error != nil {
				err = op.Error
			}
			if protocol.CodeOf(err) != protocol.CodeInvalidTransition || !strings.Contains(err.Error(), "owner stage needs its independent review") || h.count("upgrades") != before {
				t.Fatal("write bypassed new stage", op, err)
			}

		})
	}
}
