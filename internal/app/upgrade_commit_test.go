package app

import (
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func upgradeCommitAck(h *harness, b protocol.UpgradeBinding) {
	h.t.Helper()
	var rt protocol.RunnerRuntime
	if err := h.store.Read(h.t.Context(), func(tx *store.ReadTx) error {
		r, err := store.GetHostRuntime(h.t.Context(), tx.Q(), b.InstanceID)
		rt = r.Runtime
		return err
	}); err != nil {
		h.t.Fatal(err)
	}
	rt.Sequence++
	rt.UpdaterReceipt = &protocol.UpdaterReceipt{Binding: b, Phase: "committed", CommittedConfirmed: true, CurrentProcessInstanceID: rt.InstanceID, CurrentSHA256: rt.Build.BinarySHA256}
	if _, err := h.app.RuntimeHeartbeat(h.t.Context(), runner(b.InstanceID), protocol.HeartbeatPayload{Runtime: &rt}); err != nil {
		h.t.Fatal(err)
	}
}

func TestUpgradeHealthNeedsCoordinatorAndDurableCommitBeforeAdmission(t *testing.T) {
	h, c := upgradeHarness(t)
	upgradeToRunners(h, c)
	upgradeTick(h, ServerObservation{})
	upgradeAck(h, "linux-1", protocol.RunnerReady)
	upgradeTick(h, ServerObservation{})
	upgradeAck(h, "linux-1", protocol.RunnerReady)
	ctl := upgradeCtl(h, "linux-1")
	b := protocol.UpgradeBinding{UpgradeID: "upgrade-one", ReleaseID: c.Manifest.ReleaseID, ManifestSHA256: c.ManifestSHA256, ArtifactSHA256: upgradeRead(h).Hosts[0].TargetHash, InstanceID: "linux-1", ProcessInstanceID: "candidate"}
	if op := h.exec(runner(b.InstanceID), h.env("runner.upgrade_authorize", protocol.Target{Type: "host", ID: b.InstanceID}, b, rev(ctl.Revision)), AuthorizeRunnerUpgrade{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	rt := *testRuntime(b.ProcessInstanceID)
	rt.JournalVersion = 9
	rt.Build.BinarySHA256 = b.ArtifactSHA256
	rt.Mode = protocol.RunnerReady
	rt.UpgradeID = b.UpgradeID
	rt.AppliedControlRevision = ctl.Revision
	if op := h.versionedHello(b.InstanceID, &rt); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	health, err := h.app.RunnerUpgradeHealth(t.Context(), runner(b.InstanceID), b)
	if err != nil || health.Healthy || !health.AdmissionFenced || !health.RollbackSafe || health.ControlRevision != ctl.Revision {
		t.Fatal(health, err)
	}
	upgradeTick(h, ServerObservation{})
	h.clock.Advance(8 * time.Second)
	upgradeAck(h, b.InstanceID, protocol.RunnerReady)
	upgradeTick(h, ServerObservation{})
	health, err = h.app.RunnerUpgradeHealth(t.Context(), runner(b.InstanceID), b)
	if err != nil || !health.Healthy || !health.AdmissionFenced || health.NewWorkAccepted {
		t.Fatal(health, err)
	}
	if upgradeCtl(h, b.InstanceID).Mode != protocol.RunnerMaintenance {
		t.Fatal("health admitted uncommitted candidate")
	}
	r := upgradeRead(h)
	_, err = h.app.Execute(t.Context(), owner, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: b.UpgradeID}, map[string]string{"action": "cancel"}, rev(r.View.Revision)), ControlUpgrade{})
	if protocol.CodeOf(err) != protocol.CodeUnknownOutcome {
		t.Fatal("cancel admitted uncommitted candidate", err)
	}
	// Clearing control metadata cannot bypass an unresolved candidate.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		cleared := ctl
		cleared.UpgradeID = ""
		return tx.PutHostControl(t.Context(), b.InstanceID, cleared, h.clock.Now().UnixMilli())
	}); err != nil {
		t.Fatal(err)
	}
	_, err = h.app.Execute(t.Context(), owner, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: b.UpgradeID}, map[string]string{"action": "cancel"}, rev(r.View.Revision)), ControlUpgrade{})
	if protocol.CodeOf(err) != protocol.CodeUnknownOutcome {
		t.Fatal("empty control ID bypassed commitment gate", err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		return tx.PutHostControl(t.Context(), b.InstanceID, ctl, h.clock.Now().UnixMilli())
	}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"binding", "current", "hash", "unconfirmed", "contradictory"} {
		proof := &protocol.UpdaterReceipt{Binding: b, Phase: "committed", CommittedConfirmed: true, CurrentProcessInstanceID: rt.InstanceID, CurrentSHA256: rt.Build.BinarySHA256}
		switch bad {
		case "binding":
			proof.Binding.ProcessInstanceID = "other"
		case "current":
			proof.CurrentProcessInstanceID = "other"
		case "hash":
			proof.CurrentSHA256 = "bad"
		case "contradictory":
			proof.RollbackConfirmed = true
		case "unconfirmed":
			proof.CommittedConfirmed = false
		}
		rt = upgradeAck(h, b.InstanceID, protocol.RunnerReady)
		rt.Sequence++
		rt.UpdaterReceipt = proof
		if _, err := h.app.RuntimeHeartbeat(t.Context(), runner(b.InstanceID), protocol.HeartbeatPayload{Runtime: &rt}); err != nil {
			t.Fatal(err)
		}
		upgradeTick(h, ServerObservation{})
		if upgradeCtl(h, b.InstanceID).Mode != protocol.RunnerMaintenance {
			t.Fatal("invalid proof admitted", bad)
		}
	}
	upgradeCommitAck(h, b)
	upgradeTick(h, ServerObservation{})
	if upgradeCtl(h, b.InstanceID).Mode != protocol.RunnerNormal || upgradeRead(h).Hosts[0].Committed == nil {
		t.Fatal("commit did not admit")
	}
	// A new incarnation must prove continuity and build its own healthy window.
	rt = upgradeAck(h, b.InstanceID, protocol.RunnerNormal)
	rt.InstanceID = "restarted"
	rt.Generation++
	rt.Sequence = 1
	rt.PendingClaims = 1
	rt.PendingOperations = 1
	rt.Blockers = []string{"active business work"}
	rt.UpdaterReceipt.CurrentProcessInstanceID = rt.InstanceID
	if op := h.versionedHello(b.InstanceID, &rt); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	upgradeTick(h, ServerObservation{})
	r = upgradeRead(h)
	if r.Hosts[0].CandidateInstance != b.ProcessInstanceID || r.Hosts[0].Healthy != 1 || r.View.Hosts[0].Status == "completed" {
		t.Fatal("restart reused old observations", r.Hosts[0])
	}
	h.clock.Advance(8 * time.Second)
	upgradeAck(h, b.InstanceID, protocol.RunnerNormal)
	upgradeTick(h, ServerObservation{})
	upgradeTick(h, ServerObservation{})
	r = upgradeRead(h)
	if r.View.Hosts[0].Status != "completed" || r.Hosts[0].Committed.CurrentProcessInstanceID != "restarted" {
		t.Fatal("restart stranded committed candidate", r.Hosts[0])
	}
	if op := h.exec(owner, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: b.UpgradeID}, map[string]string{"action": "cancel"}, rev(r.View.Revision)), ControlUpgrade{}); op.Status != protocol.OpSucceeded {
		t.Fatal("persisted committed successor could not cancel", op)
	}
}
