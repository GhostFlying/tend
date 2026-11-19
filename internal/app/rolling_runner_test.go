package app

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
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
	old.Capabilities = append(old.Capabilities, protocol.CapabilityUsageCollection)
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
	if op.Error != nil || json.Unmarshal(op.Result, &grant) != nil || grant.Allowed {
		t.Fatal("legacy workspace preparation was not protected", op)
	}
	if started := h.runAs(original, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); started.Error != nil {
		t.Fatal(started)
	}
	op = h.exec(runner(host), h.env("runner.rolling_authorize", target, req, rev(old.AppliedControlRevision)), AuthorizeRollingRunner{})
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
	// Owner commands for an existing source follow its Run across handoff.
	reg := protocol.NativeUsageRegistration{RunID: original.RunID, ThreadID: "old-thread", SessionID: "old-thread", Provider: "codex"}
	if op := h.exec(runner(host), h.env(protocol.OpRunnerUsageRegister, target, reg, nil), RegisterNativeUsage{}); op.Error != nil {
		t.Fatal(op)
	}
	collection := protocol.NewUsageCollection(false, true)
	observation := protocol.UsageCollectionObservation{LocalSourceID: "local-old", RunID: original.RunID, ThreadID: reg.ThreadID, SessionID: reg.SessionID, Collection: collection, Sequence: 1}
	if op := h.exec(runner(host), h.env(protocol.OpRunnerUsageCollection, target, observation, nil), ObserveUsageCollection{}); op.Error != nil {
		t.Fatal(op)
	}
	command := h.env(protocol.OpResumeUsageCollection, protocol.Target{Type: "usage_source", ID: original.RunID + "/old-thread"}, protocol.UsageCollectionControl{}, rev(1))
	if op := h.exec(owner, command, ControlUsageCollection{Resume: true}); op.Error != nil {
		t.Fatal(op)
	}
	candidate.Capabilities = append(candidate.Capabilities, protocol.CapabilityUsageCollection)
	candidate.Sequence++
	nextHeartbeat, err := h.app.RuntimeHeartbeat(t.Context(), runner(host), protocol.HeartbeatPayload{Runtime: candidate})
	if err != nil || nextHeartbeat.Control == nil || len(nextHeartbeat.Control.UsageCollectionCommands) != 0 {
		t.Fatal("foreign source sent to new journal", nextHeartbeat, err)
	}
	old.Sequence++
	hb, err = h.app.RuntimeHeartbeat(t.Context(), runner(host), protocol.HeartbeatPayload{Runtime: &old, Runs: []protocol.LeaseRef{ref}})
	if err != nil || len(hb.Runs) != 1 || !hb.Runs[0].Renewed || hb.Control == nil || len(hb.Control.UsageCollectionCommands) != 1 || hb.Control.UsageCollectionCommands[0].RequestID != command.RequestID {
		t.Fatal("draining journal did not receive the original command and renewal", hb, err)
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
	// Seed a confirmed physical retirement to cover dispatch of a late command.
	// Process stop and acknowledgement are exercised by the three-process fixture.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO runner_retirements(host_id,instance_id,upgrade_id,journal_id,authorization_id,process_json,confirmed_at) VALUES(?,?,?,?,?,?,1)`, host, old.InstanceID, batch.View.UpgradeID, old.JournalID, "retired-grant", "{}"); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `DELETE FROM draining_runners WHERE host_id=? AND instance_id=?`, host, old.InstanceID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	candidate.Sequence++
	hb, err = h.app.RuntimeHeartbeat(t.Context(), runner(host), protocol.HeartbeatPayload{Runtime: candidate})
	retired := hb.RetiredWork[old.JournalID]
	if err != nil || hb.Control == nil || len(hb.Control.UsageCollectionCommands) != 0 || len(retired.Usage) != 1 || retired.Usage[0].RequestID != command.RequestID {
		t.Fatal("late command did not retain its journal and request", hb, err)
	}

}

func TestBusyGenerationReceivesAutomaticRollingTarget(t *testing.T) {
	h, c := selfUpgradeFixture(t)
	selfUpgradeHosts(t, h, true)
	if op := h.syncConfig(testConfig()); op.Error != nil {
		t.Fatal(op)
	}
	const host = "linux-1"
	// Begin with an already installed independent journal; this test drives
	// the subsequent release through the ordinary fleet upgrade entry point.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		rt, err := store.GetHostRuntime(t.Context(), tx.Q(), host)
		if err != nil {
			return err
		}
		rt.Runtime.JournalID = rt.Runtime.InstanceID
		return tx.PutHostRuntime(t.Context(), host, rt)
	}); err != nil {
		t.Fatal(err)
	}
	h.createTask(owner, createTaskPayload{Title: "active predecessor", Spec: testSpec})
	rt := upgradeAck(h, host, protocol.RunnerNormal)
	target := protocol.Target{Type: "host", ID: host}
	claim := h.env(protocol.OpRunnerClaim, target, protocol.ClaimPayload{Worker: h.worker(host), WorkspaceProtocol: 1, Runtime: &rt}, nil)
	as, ok := h.claimWith(host, claim)
	if !ok {
		t.Fatal("no original assignment")
	}
	c.ServerCapabilities = []string{release.ServerCapabilityRunnerJournals}
	c.ServerUpdaterSupported = true
	if op := startPreparedTestUpgrade(h, "next", c); op.Error != nil {
		t.Fatal(op)
	}
	batch := upgradeRead(h)
	server := ServerObservation{MainDrained: true, BinarySHA256: c.ServerHash, ProcessInstanceID: "current-server", Healthy: true}
	server.Updater = selfUpgradeObservation(batch, "server", server.ProcessInstanceID, c.ServerHash)
	for range 5 {
		if upgradeTick(h, server).Hosts[0].Phase == "draining" {
			break
		}
	}
	rt = upgradeAck(h, host, protocol.RunnerMaintenance)
	upgradeTick(h, server)
	rt.Sequence++
	hb, err := h.app.RuntimeHeartbeat(t.Context(), runner(host), protocol.HeartbeatPayload{Runtime: &rt, Runs: []protocol.LeaseRef{{RunID: as.RunID, LeaseID: as.LeaseID}}})
	if err != nil || len(hb.Runs) != 1 || !hb.Runs[0].Renewed || hb.Control == nil || hb.Control.Upgrade == nil || !hb.Control.Upgrade.Rolling || hb.Control.Upgrade.PrepareOnly || !hb.Control.Upgrade.Updater.Authorized {
		t.Fatalf("busy generation missing rolling target: control=%+v phase=%+v journal=%q err=%v", hb.Control, upgradeRead(h).View.Hosts, rt.JournalID, err)
	}
}
