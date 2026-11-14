package app

import (
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/store"
)

// The fixture establishes real policy, shared review, continuing Task and lease.
// Only provider and physical process observations are substitutes. In particular
// no completed batch, health counter, accepted artifact or duty is pre-inserted.
func TestAutomaticDeploymentRetainedFleetReachesFormalCompletion(t *testing.T) {
	for _, independent := range []bool{false, true} {
		name := "bound execution"
		if independent {
			name = "independent maintenance adopted by original responsibility"
		}
		t.Run(name, func(t *testing.T) {
			h, issue, as, c := autoDeploymentLifecycleFixture(t, false)
			duty := deliveryDutyForTest(t, h, issue.IssueID, as.TaskID)
			candidate := *duty.Candidate
			c.Manifest.Artifacts[1].SHA256 = strings.Repeat("a", 64)
			c.ReleaseSet.Artifacts[1].SHA256 = c.Manifest.Artifacts[1].SHA256
			c.ArtifactSources[c.Manifest.Artifacts[1].SHA256] = release.SourceIdentity{Commit: c.ReleaseSet.Commit}
			if independent {
				c.DeliveryTaskID = ""
			}
			if op := startTestUpgrade(h, "original-complete-fleet", c); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			initial := upgradeRead(h)
			artifact := testArtifact(h.task(as.TaskID).WorkspaceName)
			artifact.Mounts[0].CommitSHA = candidate.HeadSHA
			artifact.Report = &protocol.ResultReport{Body: "The reviewed head is merged; the exact controlled installation still awaits its physical receipts and independent acceptance", Evidence: []string{"upgrade:" + initial.View.UpgradeID}}
			if op := h.runAs(as, protocol.OpRunsSubmitArtifact, artifact, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			o := ServerObservation{InstanceID: c.ServerInstance, ProcessInstanceID: "retained-server", BinarySHA256: c.ServerHash, Commit: c.ReleaseSet.Commit, MainDrained: true, Healthy: true}
			o.Updater = retainedFleetObservation(initial, c.ServerInstance, o.ProcessInstanceID, c.ServerHash)
			upgradeTick(h, o)
			upgradeTick(h, o)
			h.clock.Advance(5 * time.Second)
			if v := upgradeTick(h, o); v.Phase != "runners" {
				t.Fatal(v)
			}
			for range 20 {
				for _, host := range []string{"linux-1", "linux-2"} {
					ctl := upgradeCtl(h, host)
					mode := protocol.RunnerNormal
					if ctl.Mode == protocol.RunnerMaintenance {
						mode = protocol.RunnerReady
					}
					rt := upgradeAck(h, host, mode)
					if ctl.Upgrade != nil && ctl.Upgrade.Updater != nil {
						rt.Sequence++
						rt.UpgradePhase = "updater_ready"
						if ctl.Upgrade.Updater.Authorized {
							rt.Build.PID = 91
							rt.Updater = retainedFleetObservation(upgradeRead(h), host, rt.InstanceID, rt.Build.BinarySHA256)
						}
						if _, err := h.app.RuntimeHeartbeat(t.Context(), runner(host), protocol.HeartbeatPayload{Runtime: &rt}); err != nil {
							t.Fatal(err)
						}
					}
				}
				if v := upgradeTick(h, o); v.Status == "completed" {
					break
				} else if v.Status != "running" {
					t.Fatal(v)
				}
				h.clock.Advance(time.Second)
			}
			if v := upgradeRead(h).View; v.Status != "completed" || v.OverallDeadlineMillis != initial.View.OverallDeadlineMillis {
				t.Fatal("original batch never completed within its unchanged authority", v)
			}
			for _, host := range []string{"linux-1", "linux-2"} {
				upgradeAck(h, host, protocol.RunnerNormal)
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				proof, err := store.DeliveryUpgradeEvidence(t.Context(), tx.Q(), *candidate.Deployment, initial.View.UpgradeID, h.task(as.TaskID).CreatedAt, h.clock.Now().UnixMilli())
				if err == nil && (proof.SourceCommit != c.ReleaseSet.Commit || proof.ServerProcessInstanceID != o.ProcessInstanceID) {
					t.Fatal(proof)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			accept, ok, _ := h.claim("linux-1")
			if !ok || accept.TaskID != as.TaskID || accept.Role != protocol.RoleAccept {
				t.Fatal("original responsibility lost its independent acceptance", accept)
			}
			if op := h.submitVerdict(accept.RunID, protocol.VerdictPassed); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			resource := h.resourceForTask(as.TaskID)
			if !h.releaseResource(as.TaskID, resource.WorkspaceID) {
				t.Fatal("accepted original resources did not release")
			}
			if op := updateDeliveryForTest(t, h, issue.IssueID, protocol.UpdateIssueDeliveryPayload{Action: "complete", TaskID: as.TaskID, UpgradeID: initial.View.UpgradeID, IssueControlPayload: protocol.IssueControlPayload{Reason: "Original reviewed source, actual retained fleet and independent acceptance verified"}}); op.Status != protocol.OpSucceeded {
				t.Fatal("normal installed delivery could not settle", op)
			}
			if d := deliveryDutyForTest(t, h, issue.IssueID, as.TaskID); d.Status != "complete" || d.ExecutionTaskID != as.TaskID || h.count("upgrades") != 1 {
				t.Fatal("completion repeated installation or replaced the original Task", d)
			}
		})
	}
}

func retainedFleetObservation(row store.UpgradeRow, host, child, hash string) *protocol.UpdaterSelfObservation {
	o := selfUpgradeObservation(row, host, child, hash)
	for _, artifact := range row.ReleaseSet.Artifacts {
		if artifact.Role == "updater" && artifact.OS == "linux" && artifact.Arch == "amd64" {
			o.Process.SHA256 = artifact.SHA256
			o.Receipt.Previous.SHA256, o.Receipt.Installed.SHA256 = artifact.SHA256, artifact.SHA256
		}
	}
	return o
}
