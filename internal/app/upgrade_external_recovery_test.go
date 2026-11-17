package app

import (
	"github.com/GhostFlying/tend/internal/protocol"
	"strings"
	"testing"
)

func externalReceipt(instance, process, hash, old string) *protocol.UpdaterReceipt {
	return &protocol.UpdaterReceipt{ExternalRecovery: true, SupersededUpgradeID: old, Binding: protocol.UpgradeBinding{UpgradeID: "external-recovery", InstanceID: instance, ProcessInstanceID: process, ArtifactSHA256: hash}, Phase: "committed", CommittedConfirmed: true, CurrentProcessInstanceID: process, CurrentSHA256: hash}
}

func TestExternalRecoverySupersedesOnlyItsOriginalInstallation(t *testing.T) {
	h, c := upgradeHarness(t)
	if op := startTestUpgrade(h, "old-install", c); op.Error != nil {
		t.Fatal(op)
	}
	hash := strings.Repeat("e", 64)
	receipt := externalReceipt(c.ServerInstance, "external-process", hash, "old-install")
	observation := ServerObservation{InstanceID: c.ServerInstance, ProcessInstanceID: "external-process", BinarySHA256: hash, UpdaterReceipt: receipt}
	wrong := observation
	wrong.ProcessInstanceID = "unrelated-process"
	if v := upgradeTick(h, wrong); v.Status != "running" || h.count("external_recoveries") != 0 {
		t.Fatal("unproved receipt changed the batch", v)
	}
	if v := upgradeTick(h, observation); v.Status != "cancelled" || v.Phase != "external_recovery" || h.count("external_recoveries") != 1 {
		t.Fatal(v)
	}
	// The unmodified hosts keep their original installation identity. A local
	// replacement is never permission to release another machine's stop fence.
	if control := upgradeCtl(h, "linux-1"); control.UpgradeID != "old-install" {
		t.Fatal(control)
	}
	// Repeating the durable proof is observational and cannot cancel a new batch.
	revision := upgradeRead(h).View.Revision
	upgradeTick(h, observation)
	if upgradeRead(h).View.Revision != revision || h.count("external_recoveries") != 1 {
		t.Fatal("receipt repeated its transition")
	}
}

func TestExternalRecoveryHostClearsOnlyItsOwnControl(t *testing.T) {
	h, c := upgradeHarness(t)
	if op := startTestUpgrade(h, "old-install", c); op.Error != nil {
		t.Fatal(op)
	}
	rt := upgradeAck(h, "linux-1", protocol.RunnerNormal)
	rt.Sequence++
	rt.UpdaterReceipt = externalReceipt("linux-1", rt.InstanceID, rt.Build.BinarySHA256, "old-install")
	if _, err := h.app.RuntimeHeartbeat(t.Context(), runner("linux-1"), protocol.HeartbeatPayload{Runtime: &rt}); err != nil {
		t.Fatal(err)
	}
	if v := upgradeTick(h, ServerObservation{}); v.Status != "cancelled" {
		t.Fatal(v)
	}
	if ctl := upgradeCtl(h, "linux-1"); ctl.Mode != protocol.RunnerNormal || ctl.UpgradeID != "" || ctl.Upgrade != nil {
		t.Fatal(ctl)
	}
	if ctl := upgradeCtl(h, "linux-2"); ctl.UpgradeID != "old-install" {
		t.Fatal("external recovery released another host", ctl)
	}
}

func TestOldExternalRecoveryDoesNotCancelAnotherBatch(t *testing.T) {
	h, c := upgradeHarness(t)
	if op := startTestUpgrade(h, "new-install", c); op.Error != nil {
		t.Fatal(op)
	}
	hash := strings.Repeat("e", 64)
	o := ServerObservation{InstanceID: c.ServerInstance, ProcessInstanceID: "external-process", BinarySHA256: hash, UpdaterReceipt: externalReceipt(c.ServerInstance, "external-process", hash, "previous-install")}
	if v := upgradeTick(h, o); v.Status != "running" || v.UpgradeID != "new-install" {
		t.Fatal("old recovery cancelled later authority", v)
	}
}
