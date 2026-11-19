package app

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// The prepared candidate and supervisor receipts are observations from the
// fixture. Claims, rolling admission, journal routing and renewal use commands.
func TestRollingRunnerKeepsOriginalLeaseAfterIndependentJournalHello(t *testing.T) {
	h, c := selfUpgradeFixture(t)
	selfUpgradeHosts(t, h, true)
	cfg := testConfig()
	cfg.Hosts[0].Slots = 2
	if op := h.syncConfig(cfg); op.Error != nil {
		t.Fatal(op)
	}
	const host = "linux-1"
	old := upgradeAck(h, host, protocol.RunnerNormal)
	old.Sequence++
	old.Build.PID = 91
	old.Protocol, old.ProtocolMin, old.ProtocolMax = protocol.MinimumRunnerProtocol, protocol.MinimumRunnerProtocol, protocol.MinimumRunnerProtocol
	if op := h.versionedHello(host, &old); op.Error != nil {
		t.Fatal(op)
	}
	h.createTask(owner, createTaskPayload{Title: "predecessor", Spec: testSpec})
	target := protocol.Target{Type: "host", ID: host}
	claim := h.env(protocol.OpRunnerClaim, target, protocol.ClaimPayload{Worker: h.worker(host), WorkspaceProtocol: 1, Runtime: &old}, nil)
	original, ok := h.claimWith(host, claim)
	if !ok {
		t.Fatal("old runner did not claim")
	}
	c.ServerUpdaterSupported = true
	if op := startPreparedTestUpgrade(h, "rolling", c); op.Error != nil {
		t.Fatal(op)
	}
	batch := upgradeRead(h)
	server := ServerObservation{MainDrained: true, BinarySHA256: c.ServerHash, ProcessInstanceID: "current-server", Healthy: true}
	server.Updater = selfUpgradeObservation(batch, "server", server.ProcessInstanceID, c.ServerHash)
	for range 5 {
		view := upgradeTick(h, server)
		if view.Hosts[0].Phase == "draining" {
			break
		}
	}
	old = upgradeAck(h, host, protocol.RunnerMaintenance)
	old.Blockers = []string{"run:" + original.RunID}
	batch = upgradeRead(h)
	b := protocol.UpgradeBinding{UpgradeID: batch.View.UpgradeID, ReleaseID: c.Manifest.ReleaseID, ManifestSHA256: c.ManifestSHA256, ArtifactSHA256: batch.Hosts[0].TargetHash, InstanceID: host, ProcessInstanceID: "next-instance"}
	req := protocol.RollingRunnerGrantRequest{Binding: b, Updater: *selfUpgradeObservation(batch, host, old.InstanceID, old.Build.BinarySHA256)}
	op := h.exec(runner(host), h.env("runner.rolling_authorize", target, req, rev(old.AppliedControlRevision)), AuthorizeRollingRunner{})
	var grant protocol.UpgradeGrant
	if op.Error != nil || json.Unmarshal(op.Result, &grant) != nil || !grant.Allowed {
		t.Fatalf("rolling grant refused at %s/%s: %+v", batch.View.Phase, batch.View.Hosts[0].Phase, op)
	}
	ctl := upgradeCtl(h, host)
	if stop := h.exec(runner(host), h.env("runner.upgrade_authorize", target, b, rev(ctl.Revision)), AuthorizeRunnerUpgrade{}); stop.Error == nil {
		t.Fatal("rolling grant allowed stopping the predecessor")
	}
	candidate := testRuntime(b.ProcessInstanceID)
	candidate.Generation = 1
	candidate.JournalID = b.ProcessInstanceID
	candidate.JournalVersion = c.Manifest.Storage.RunnerJournal.Max
	candidate.Build = protocol.RunnerBuild{BinarySHA256: b.ArtifactSHA256, PID: 92}
	candidate.Mode = protocol.RunnerReady
	candidate.UpgradeID = b.UpgradeID
	candidate.AppliedControlRevision = ctl.Revision
	op = h.versionedHello(host, candidate)
	var hello protocol.HelloResult
	if op.Error != nil || json.Unmarshal(op.Result, &hello) != nil || len(hello.Reassigned) != 0 {
		t.Fatalf("new journal consumed predecessor claim: %+v", op)
	}
	old.Sequence++
	ref := protocol.LeaseRef{RunID: original.RunID, LeaseID: original.LeaseID}
	hb, err := h.app.RuntimeHeartbeat(t.Context(), runner(host), protocol.HeartbeatPayload{Runtime: &old, Runs: []protocol.LeaseRef{ref}})
	if err != nil || len(hb.Runs) != 1 || !hb.Runs[0].Renewed || hb.Control == nil || hb.Control.Mode != protocol.RunnerMaintenance {
		t.Fatalf("old lease did not survive handoff: %+v %v", hb, err)
	}
	candidate.Sequence++
	if _, err := h.app.RuntimeHeartbeat(t.Context(), runner(host), protocol.HeartbeatPayload{Runtime: candidate, Runs: []protocol.LeaseRef{ref}}); err == nil {
		t.Fatal("another journal renewed original lease")
	}
	// Post-commit admission of the candidate is covered by supervisor tests.
	// Supply its committed receipt and observe the business transition here.
	candidate.Capabilities = append(candidate.Capabilities, protocol.CapabilityUpdaterSelfUpdate)
	candidate.Updater = selfUpgradeObservation(batch, host, candidate.InstanceID, candidate.Build.BinarySHA256)
	candidate.Updater.SupervisedProcess.PID = int(candidate.Build.PID)
	candidate.UpdaterReceipt = &protocol.UpdaterReceipt{Phase: "committed", CommittedConfirmed: true, Binding: b, CurrentSHA256: b.ArtifactSHA256, CurrentProcessInstanceID: candidate.InstanceID}
	candidate.Sequence++
	if _, err := h.app.RuntimeHeartbeat(t.Context(), runner(host), protocol.HeartbeatPayload{Runtime: candidate}); err != nil {
		t.Fatal(err)
	}
	upgradeTick(h, server)
	ctl = upgradeCtl(h, host)
	if ctl.Mode != protocol.RunnerNormal {
		t.Fatal("old lease kept the committed candidate in maintenance", upgradeRead(h).View)
	}
	candidate.AppliedControlRevision = ctl.Revision
	candidate.Mode = protocol.RunnerNormal
	candidate.Sequence++
	if _, err := h.app.RuntimeHeartbeat(t.Context(), runner(host), protocol.HeartbeatPayload{Runtime: candidate}); err != nil {
		t.Fatal(err)
	}
	h.createTask(owner, createTaskPayload{Title: "successor", Spec: testSpec})
	newClaim := func(rt *protocol.RunnerRuntime) (protocol.Assignment, bool) {
		return h.claimWith(host, h.env(protocol.OpRunnerClaim, target, protocol.ClaimPayload{Worker: h.worker(host), WorkspaceProtocol: 1, Runtime: rt}, nil))
	}
	if _, ok := newClaim(&old); ok {
		t.Fatal("draining runner admitted new work")
	}
	assigned, ok := newClaim(candidate)
	if !ok || assigned.RunID == original.RunID {
		t.Fatal("candidate failed to take new work", assigned)
	}
	h.createTask(owner, createTaskPayload{Title: "capacity", Spec: testSpec})
	if _, ok := newClaim(candidate); ok {
		t.Fatal("physical host capacity counted twice")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		lease, err := store.GetLeaseByRun(t.Context(), tx.Q(), original.RunID)
		if err == nil && (lease.Status != store.LeaseActive || lease.LeaseID != original.LeaseID) {
			t.Fatal("predecessor lease was replaced", lease)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
