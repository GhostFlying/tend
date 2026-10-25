package app

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/store"
)

func upgradeHarness(t *testing.T) (*harness, StartUpgrade) {
	t.Helper()
	h := newHarness(t)
	cfg := testConfig()
	second := cfg.Hosts[0]
	second.HostID = "linux-2"
	cfg.Hosts = append(cfg.Hosts, second)
	if op := h.syncConfig(cfg); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	for _, host := range []string{"linux-1", "linux-2"} {
		rt := testRuntime("old-" + host)
		rt.Capabilities = append(rt.Capabilities, capabilityUpdater)
		rt.Build.BinarySHA256 = strings.Repeat("a", 64)
		rt.Build.Version = "old"
		rt.JournalVersion = 9
		if op := h.versionedHello(host, rt); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	return h, StartUpgrade{Manifest: release.Manifest{Format: 1, ReleaseID: "release-new", Protocol: release.Range{Min: 0, Max: 1}, Storage: release.Storage{ServerSchema: release.Range{Min: 27, Max: 37}, RunnerJournal: release.Range{Min: 9, Max: 9}}, Artifacts: []release.Artifact{{Role: "server", OS: "linux", Arch: "amd64", Path: "server", Size: 20, SHA256: strings.Repeat("c", 64)}, {Role: "runner", OS: "linux", Arch: "amd64", Path: "runner", Size: 10, SHA256: strings.Repeat("b", 64)}}}, ManifestSHA256: strings.Repeat("d", 64), ManifestSize: 128, ServerHash: strings.Repeat("c", 64), ServerInstance: "server"}
}
func startTestUpgrade(h *harness, id string, c StartUpgrade) protocol.Operation {
	h.t.Helper()
	return h.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, h.env(c.Operation(), protocol.Target{Type: "upgrade", ID: id}, map[string]string{"release_id": c.Manifest.ReleaseID}, rev(0)), c)
}
func upgradeRead(h *harness) store.UpgradeRow {
	h.t.Helper()
	r, err := h.app.GetUpgrade(h.t.Context(), "")
	if err != nil {
		h.t.Fatal(err)
	}
	return r
}
func upgradeCtl(h *harness, host string) protocol.RunnerControl {
	h.t.Helper()
	var c protocol.RunnerControl
	if err := h.store.Read(h.t.Context(), func(tx *store.ReadTx) error {
		var err error
		c, err = runnerControl(h.t.Context(), tx.Q(), host, h.clock.Now().UnixMilli())
		return err
	}); err != nil {
		h.t.Fatal(err)
	}
	return c
}
func upgradeAck(h *harness, host, mode string) protocol.RunnerRuntime {
	h.t.Helper()
	var rt store.HostRuntimeRow
	if err := h.store.Read(h.t.Context(), func(tx *store.ReadTx) error {
		var err error
		rt, err = store.GetHostRuntime(h.t.Context(), tx.Q(), host)
		return err
	}); err != nil {
		h.t.Fatal(err)
	}
	ctl := upgradeCtl(h, host)
	r := rt.Runtime
	r.Sequence++
	r.Mode = mode
	r.AppliedControlRevision = ctl.Revision
	r.UpgradeID = ctl.UpgradeID
	r.ConfirmedCancelledUpgradeID = ctl.CancelledUpgradeID
	r.AuthorizationID = ""
	if ctl.Authorization != nil {
		r.AuthorizationID = ctl.Authorization.ID
		r.AuthorizationDeadlineMillis = ctl.Authorization.DeadlineMillis - (ctl.Authorization.RemainingMillis+3)/4
	}
	if _, err := h.app.RuntimeHeartbeat(h.t.Context(), runner(host), protocol.HeartbeatPayload{Runtime: &r}); err != nil {
		h.t.Fatal(err)
	}
	return r
}
func upgradeTick(h *harness, o ServerObservation) protocol.UpgradeView {
	h.t.Helper()
	v, err := h.app.TickUpgrade(h.t.Context(), o)
	if err != nil {
		h.t.Fatal(err)
	}
	return v
}
func upgradeToRunners(h *harness, c StartUpgrade) protocol.UpgradeBinding {
	h.t.Helper()
	if op := startTestUpgrade(h, "upgrade-one", c); op.Status != protocol.OpSucceeded {
		h.t.Fatal(op)
	}
	if v := upgradeTick(h, ServerObservation{MainDrained: false}); v.Phase != "server_draining" {
		h.t.Fatal(v)
	}
	if ctl := upgradeCtl(h, "linux-1"); ctl.Mode != protocol.RunnerNormal {
		h.t.Fatal("maintenance before main drain", ctl)
	}
	if v := upgradeTick(h, ServerObservation{MainDrained: true, BinarySHA256: strings.Repeat("a", 64)}); v.Phase != "server_ready" {
		h.t.Fatal(v)
	}
	for _, host := range []string{"linux-1", "linux-2"} {
		upgradeAck(h, host, protocol.RunnerMaintenance)
	}
	b := protocol.UpgradeBinding{UpgradeID: "upgrade-one", ReleaseID: c.Manifest.ReleaseID, ManifestSHA256: c.ManifestSHA256, ArtifactSHA256: c.ServerHash, InstanceID: c.ServerInstance, ProcessInstanceID: "new-server"}
	g, err := h.app.ServerUpgradeAuthorize(h.t.Context(), b)
	if err != nil || !g.Allowed {
		h.t.Fatalf("server grant %+v %v", g, err)
	}
	o := ServerObservation{InstanceID: "server", ProcessInstanceID: b.ProcessInstanceID, BinarySHA256: c.ServerHash, Healthy: true, MainDrained: true}
	if v := upgradeTick(h, o); v.Phase != "server_checking" {
		h.t.Fatal(v)
	}
	if v := upgradeTick(h, o); v.Phase != "server_checking" {
		h.t.Fatal("duplicate observation counted twice", v)
	}
	h.clock.Advance(5 * time.Second)
	if v := upgradeTick(h, o); v.Phase != "runners" {
		h.t.Fatal(v)
	}
	for _, host := range []string{"linux-1", "linux-2"} {
		upgradeAck(h, host, protocol.RunnerNormal)
	}
	return b
}

func TestUpgradePreflightRejectsUnsafeFleet(t *testing.T) {
	for _, kind := range []string{"offline", "incompatible", "missing-updater", "retired-run"} {
		t.Run(kind, func(t *testing.T) {
			h, c := upgradeHarness(t)
			switch kind {
			case "offline":
				h.clock.Advance(61 * time.Second)
			case "incompatible":
				c.Manifest.Protocol = release.Range{Min: 1, Max: 2}
				r := testRuntime("protocol-zero")
				r.Protocol = 0
				r.ProtocolMax = 0
				r.Capabilities = append(r.Capabilities, capabilityUpdater)
				if op := h.versionedHello("linux-2", r); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			case "missing-updater":
				r := testRuntime("no-updater")
				if op := h.versionedHello("linux-2", r); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			case "retired-run":
				h.createTask(owner, createTaskPayload{Title: "legacy active", Spec: testSpec})
				r := testRuntime("active")
				r.Capabilities = append(r.Capabilities, capabilityUpdater)
				h.versionedHello("linux-1", r)
				env := h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{Worker: h.worker("linux-1"), WorkspaceProtocol: 1, Runtime: r}, nil)
				a, ok := h.claimWith("linux-1", env)
				if !ok {
					t.Fatal("no run")
				}
				if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE runs SET protocol=0 WHERE run_id=?`, a.RunID)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				c.Manifest.Protocol = release.Range{Min: 1, Max: 2}
			}
			op := startTestUpgrade(h, "rejected", c)
			if op.Status != protocol.OpFailed {
				t.Fatalf("accepted %s: %+v", kind, op)
			}
			if h.count("upgrades") != 0 || h.count("message_notices") != 0 {
				t.Fatal("failed preflight left release state/notification")
			}
		})
	}
}
func TestUpgradeSingleUnfinishedAndFixedServerGrant(t *testing.T) {
	h, c := upgradeHarness(t)
	if op := startTestUpgrade(h, "one", c); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := startTestUpgrade(h, "two", c); op.Status != protocol.OpFailed {
		t.Fatal("overlapping batch", op)
	}
	upgradeTick(h, ServerObservation{MainDrained: true})
	initial := upgradeCtl(h, "linux-1").Authorization
	if initial == nil {
		t.Fatal("missing maintenance coverage")
	}
	h.clock.Advance(time.Second)
	upgradeTick(h, ServerObservation{MainDrained: true})
	if got := upgradeCtl(h, "linux-1").Authorization; got.ID != initial.ID || got.DeadlineMillis != initial.DeadlineMillis {
		t.Fatal("tick renewed fixed coverage", got)
	}
	for _, host := range []string{"linux-1", "linux-2"} {
		upgradeAck(h, host, protocol.RunnerMaintenance)
	}
	b := protocol.UpgradeBinding{UpgradeID: "one", ReleaseID: c.Manifest.ReleaseID, ManifestSHA256: c.ManifestSHA256, ArtifactSHA256: c.ServerHash, InstanceID: "server", ProcessInstanceID: "new-server"}
	first, err := h.app.ServerUpgradeAuthorize(t.Context(), b)
	if err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(time.Second)
	second, err := h.app.ServerUpgradeAuthorize(t.Context(), b)
	if err != nil || !first.ExpiresAt.Equal(second.ExpiresAt) || second.RemainingMillis >= first.RemainingMillis {
		t.Fatalf("grant extended %+v %+v %v", first, second, err)
	}
	b.ProcessInstanceID = "another-server"
	if _, err := h.app.ServerUpgradeAuthorize(t.Context(), b); err == nil {
		t.Fatal("candidate identity changed")
	}
}
func TestUpgradeServerFirstRestorationAndNoDatabaseRollback(t *testing.T) {
	h, c := upgradeHarness(t)
	b := upgradeToRunners(h, c)
	health, err := h.app.ServerUpgradeHealth(t.Context(), b)
	if err != nil || !health.Healthy || !health.NewWorkAccepted || health.RollbackSafe {
		t.Fatalf("server health: %+v %v", health, err)
	}
	upgradeTick(h, ServerObservation{})
	first := upgradeCtl(h, "linux-1")
	other := upgradeCtl(h, "linux-2")
	if first.Mode != protocol.RunnerMaintenance || other.Mode != protocol.RunnerNormal {
		t.Fatal("not one maintenance host", first, other)
	}
	upgradeAck(h, "linux-1", protocol.RunnerReady)
	upgradeTick(h, ServerObservation{})
	upgradeAck(h, "linux-1", protocol.RunnerReady)
	ctl := upgradeCtl(h, "linux-1")
	rb := protocol.UpgradeBinding{UpgradeID: "upgrade-one", ReleaseID: c.Manifest.ReleaseID, ManifestSHA256: c.ManifestSHA256, ArtifactSHA256: strings.Repeat("b", 64), InstanceID: "linux-1", ProcessInstanceID: "new-runner"}
	env := h.env("runner.upgrade_authorize", protocol.Target{Type: "host", ID: "linux-1"}, rb, rev(ctl.Revision))
	op := h.exec(runner("linux-1"), env, AuthorizeRunnerUpgrade{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var grant protocol.UpgradeGrant
	if err := json.Unmarshal(op.Result, &grant); err != nil || !grant.Allowed {
		t.Fatalf("grant %+v %v", grant, err)
	}

	beforeCancel := upgradeRead(h)
	_, cancelErr := h.app.Execute(t.Context(), owner, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: rb.UpgradeID}, map[string]string{"action": "cancel"}, rev(beforeCancel.View.Revision)), ControlUpgrade{})
	if protocol.CodeOf(cancelErr) != protocol.CodeUnknownOutcome {
		t.Fatalf("unknown installation cancellation = %v", cancelErr)
	}
	if after := upgradeRead(h); after.View.Revision != beforeCancel.View.Revision || after.View.Status != "running" || upgradeCtl(h, "linux-1").Mode != protocol.RunnerMaintenance {
		t.Fatal("unsafe cancellation changed maintenance facts")
	}
	h.clock.Advance(time.Second)
	replay := h.exec(runner("linux-1"), h.env("runner.upgrade_authorize", env.Target, rb, rev(ctl.Revision)), AuthorizeRunnerUpgrade{})
	var again protocol.UpgradeGrant
	if replay.Status != protocol.OpSucceeded || json.Unmarshal(replay.Result, &again) != nil || !again.ExpiresAt.Equal(grant.ExpiresAt) || again.RemainingMillis >= grant.RemainingMillis {
		t.Fatalf("runner grant extended: %+v %+v", grant, replay)
	}
	candidate := testRuntime(rb.ProcessInstanceID)
	candidate.JournalVersion = 9
	candidate.Build.BinarySHA256 = rb.ArtifactSHA256
	candidate.Build.Version = "new"
	candidate.Mode = protocol.RunnerReady
	candidate.UpgradeID = rb.UpgradeID
	candidate.AppliedControlRevision = ctl.Revision
	candidate.Capabilities = append(candidate.Capabilities, capabilityUpdater)
	if op := h.versionedHello("linux-1", candidate); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	upgradeTick(h, ServerObservation{})
	v := upgradeTick(h, ServerObservation{})
	if v.Hosts[0].Phase == "restored" {
		t.Fatal("one observation counted twice")
	}
	h.clock.Advance(8 * time.Second)
	upgradeTick(h, ServerObservation{})
	if upgradeRead(h).View.Hosts[0].Phase == "restored" {
		t.Fatal("time alone counted a heartbeat")
	}
	upgradeAck(h, "linux-1", protocol.RunnerReady)
	v = upgradeTick(h, ServerObservation{})
	if upgradeCtl(h, "linux-1").Mode != protocol.RunnerMaintenance {
		t.Fatal("health admitted before commit")
	}
	upgradeCommitAck(h, rb)
	v = upgradeTick(h, ServerObservation{})
	if v.Hosts[0].Phase != "restored" {
		t.Fatal("two real observations not restored", v)
	}
	if upgradeCtl(h, "linux-2").Mode != protocol.RunnerNormal {
		t.Fatal("second maintenance before first acknowledgement")
	}
	upgradeAck(h, "linux-1", protocol.RunnerNormal)
	v = upgradeTick(h, ServerObservation{})
	if v.Hosts[0].Status != "completed" {
		t.Fatal(v)
	}
	health, err = h.app.RunnerUpgradeHealth(t.Context(), runner("linux-1"), rb)
	if err != nil || !health.NewWorkAccepted || health.RollbackSafe {
		t.Fatalf("runner rollback after claims: %+v %v", health, err)
	}
	upgradeAck(h, "linux-1", protocol.RunnerNormal)
	upgradeTick(h, ServerObservation{})
	if upgradeCtl(h, "linux-2").Mode != protocol.RunnerMaintenance {
		t.Fatal("did not advance next host")
	}

	upgradeAck(h, "linux-2", protocol.RunnerReady)
	upgradeTick(h, ServerObservation{})
	upgradeAck(h, "linux-2", protocol.RunnerReady)
	ctl2 := upgradeCtl(h, "linux-2")
	rb2 := rb
	rb2.InstanceID = "linux-2"
	rb2.ProcessInstanceID = "new-runner-2"
	op = h.exec(runner("linux-2"), h.env("runner.upgrade_authorize", protocol.Target{Type: "host", ID: "linux-2"}, rb2, rev(ctl2.Revision)), AuthorizeRunnerUpgrade{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	next := testRuntime(rb2.ProcessInstanceID)
	next.JournalVersion = 9
	next.Capabilities = append(next.Capabilities, capabilityUpdater)
	next.Build.BinarySHA256 = rb2.ArtifactSHA256
	next.Mode = protocol.RunnerReady
	next.UpgradeID = rb2.UpgradeID
	next.AppliedControlRevision = ctl2.Revision
	if op := h.versionedHello("linux-2", next); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	upgradeTick(h, ServerObservation{})
	h.clock.Advance(8 * time.Second)
	upgradeAck(h, "linux-2", protocol.RunnerReady)
	upgradeTick(h, ServerObservation{})
	upgradeCommitAck(h, rb2)
	upgradeTick(h, ServerObservation{})
	upgradeAck(h, "linux-2", protocol.RunnerNormal)
	upgradeTick(h, ServerObservation{})
	if v := upgradeTick(h, ServerObservation{}); v.Status != "completed" || v.Phase != "completed" {
		t.Fatal("fleet did not complete", v)
	}
	upgradeTick(h, ServerObservation{})
	if h.count("message_notices") != 4 {
		t.Fatalf("want start+2 restored+completed exactly once, got %d", h.count("message_notices"))
	}
	if h.count("outbox") != 0 {
		t.Fatal("release notifications woke a model")
	}
}

func TestUpgradeDrainTimeoutResumesMainWithoutDiscardingFacts(t *testing.T) {
	h, c := upgradeHarness(t)
	c.DrainMillis = 1000
	if op := startTestUpgrade(h, "timed-out", c); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	paused, err := h.app.UpgradePausesMain(t.Context())
	if err != nil || !paused {
		t.Fatalf("initial pause=%v %v", paused, err)
	}
	h.clock.Advance(time.Second)
	v := upgradeTick(h, ServerObservation{MainDrained: false})
	if v.Status != "attention" {
		t.Fatal(v)
	}
	paused, err = h.app.UpgradePausesMain(t.Context())
	if err != nil || paused {
		t.Fatalf("drain timeout must resume old main: paused=%v err=%v", paused, err)
	}
	for _, host := range []string{"linux-1", "linux-2"} {
		if upgradeCtl(h, host).Mode != protocol.RunnerNormal {
			t.Fatal("timeout changed runner before server switch")
		}
	}
	if h.count("message_notices") != 2 {
		t.Fatal("start/attention facts missing")
	}
}

func TestUpgradeCancelRestoresOnlyConfirmedUnswitchedRunners(t *testing.T) {
	h, c := upgradeHarness(t)
	upgradeToRunners(h, c)
	upgradeTick(h, ServerObservation{})
	r := upgradeRead(h)
	op := h.exec(owner, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: r.View.UpgradeID}, map[string]string{"action": "cancel"}, rev(r.View.Revision)), ControlUpgrade{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	r = upgradeRead(h)
	if r.View.Status != "cancelled" || r.View.Phase != "cancelled" {
		t.Fatal(r.View)
	}
	if upgradeCtl(h, "linux-1").Mode != protocol.RunnerNormal {
		t.Fatal("uninstalled runner not released")
	}
	for _, host := range []string{"linux-1", "linux-2"} {
		upgradeAck(h, host, protocol.RunnerNormal)
	}
	if op := startTestUpgrade(h, "after-ack", c); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}

func TestUpgradeRetainedHostOfflinePreventsCandidateAuthorization(t *testing.T) {
	h, c := upgradeHarness(t)
	upgradeToRunners(h, c)
	upgradeTick(h, ServerObservation{})
	upgradeAck(h, "linux-1", protocol.RunnerReady)
	upgradeTick(h, ServerObservation{})
	upgradeAck(h, "linux-1", protocol.RunnerReady)
	h.clock.Advance(61 * time.Second)
	upgradeAck(h, "linux-1", protocol.RunnerReady)
	ctl := upgradeCtl(h, "linux-1")
	b := protocol.UpgradeBinding{UpgradeID: "upgrade-one", ReleaseID: c.Manifest.ReleaseID, ManifestSHA256: c.ManifestSHA256, ArtifactSHA256: strings.Repeat("b", 64), InstanceID: "linux-1", ProcessInstanceID: "candidate"}

	env := h.env("runner.upgrade_authorize", protocol.Target{Type: "host", ID: "linux-1"}, b, rev(ctl.Revision))
	op := h.exec(runner("linux-1"), env, AuthorizeRunnerUpgrade{})
	var denied protocol.UpgradeGrant
	if op.Status != protocol.OpSucceeded || json.Unmarshal(op.Result, &denied) != nil || denied.Allowed {
		t.Fatal("authorized while retained host offline", op)
	}
	if r := upgradeRead(h); r.Hosts[0].CandidateInstance != "" || r.Hosts[0].GrantDeadline != 0 {
		t.Fatal("denied authorization changed candidate")
	}
	upgradeAck(h, "linux-2", protocol.RunnerNormal)
	op = h.exec(runner("linux-1"), env, AuthorizeRunnerUpgrade{})
	var allowed protocol.UpgradeGrant
	if op.Status != protocol.OpSucceeded || json.Unmarshal(op.Result, &allowed) != nil || !allowed.Allowed {
		t.Fatal("same request ID did not recover after retained host returned", op)
	}
	h.clock.Advance(61 * time.Second)
	upgradeAck(h, "linux-1", protocol.RunnerReady)
	if v := upgradeTick(h, ServerObservation{}); v.Status != "attention" {
		t.Fatalf("retained-host loss must pause and notify: %+v", v)
	}
	if ctl := upgradeCtl(h, "linux-1"); ctl.Mode != protocol.RunnerMaintenance {
		t.Fatal("unknown candidate gate released", ctl)
	}

}

func TestUpgradeChoosesIdleRunnerBeforeBusyRunner(t *testing.T) {
	h, c := upgradeHarness(t)
	h.createTask(owner, createTaskPayload{Title: "active during rolling upgrade", Spec: testSpec})
	rt := testRuntime("old-linux-1")
	rt.Generation = 1
	rt.Capabilities = append(rt.Capabilities, capabilityUpdater)
	if _, ok := h.claimWith("linux-1", h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{Worker: h.worker("linux-1"), WorkspaceProtocol: 1, Runtime: rt}, nil)); !ok {
		t.Fatal("missing active run")
	}
	if op := startTestUpgrade(h, "idle-first", c); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	// This release has an unchanged server, so only runner ordering is exercised.
	upgradeTick(h, ServerObservation{MainDrained: true, BinarySHA256: c.ServerHash})
	upgradeTick(h, ServerObservation{})
	if upgradeCtl(h, "linux-2").Mode != protocol.RunnerMaintenance || upgradeCtl(h, "linux-1").Mode != protocol.RunnerNormal {
		t.Fatal("idle runner was not selected before occupied runner")
	}
}

func TestUpgradeActiveRunCoverageIsFixedAcrossHeartbeats(t *testing.T) {
	h, c := upgradeHarness(t)
	h.createTask(owner, createTaskPayload{Title: "work continues across server restart", Spec: testSpec})
	rt := testRuntime("old-linux-1")
	rt.Generation = 1
	rt.Capabilities = append(rt.Capabilities, capabilityUpdater)
	a, ok := h.claimWith("linux-1", h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{Worker: h.worker("linux-1"), WorkspaceProtocol: 1, Runtime: rt}, nil))
	if !ok {
		t.Fatal("missing run")
	}
	if op := startTestUpgrade(h, "active-coverage", c); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	upgradeTick(h, ServerObservation{MainDrained: true})
	ctl := upgradeCtl(h, "linux-1")
	g := ctl.Authorization
	if g == nil || len(g.RunIDs) != 1 || g.RunIDs[0] != a.RunID {
		t.Fatalf("run coverage: %+v", g)
	}
	var lease store.LeaseRow
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		lease, err = store.GetLeaseByRun(t.Context(), tx.Q(), a.RunID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if lease.ExpiresAt < g.DeadlineMillis {
		t.Fatal("persisted lease shorter than restart coverage")
	}
	rt.Sequence++
	rt.Mode = protocol.RunnerMaintenance
	rt.AppliedControlRevision = ctl.Revision
	rt.AuthorizationID = g.ID
	rt.UpgradeID = ctl.UpgradeID
	h.clock.Advance(15 * time.Second)
	hb, err := h.app.RuntimeHeartbeat(t.Context(), runner("linux-1"), protocol.HeartbeatPayload{Runtime: rt, Runs: []protocol.LeaseRef{{RunID: a.RunID, LeaseID: a.LeaseID}}})
	if err != nil || len(hb.Runs) != 1 || !hb.Runs[0].Renewed {
		t.Fatalf("active renewal %+v %v", hb, err)
	}
	if after := upgradeCtl(h, "linux-1").Authorization; after.ID != g.ID || after.DeadlineMillis != g.DeadlineMillis || after.RemainingMillis >= g.RemainingMillis {
		t.Fatal("heartbeat extended fixed coverage", after, g)
	}
}

func TestUpgradeExpiredServerGrantCannotBeExtendedByRetry(t *testing.T) {
	h, c := upgradeHarness(t)
	if op := startTestUpgrade(h, "expired-grant", c); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	upgradeTick(h, ServerObservation{MainDrained: true})
	for _, host := range []string{"linux-1", "linux-2"} {
		upgradeAck(h, host, protocol.RunnerMaintenance)
	}
	b := protocol.UpgradeBinding{UpgradeID: "expired-grant", ReleaseID: c.Manifest.ReleaseID, ManifestSHA256: c.ManifestSHA256, ArtifactSHA256: c.ServerHash, InstanceID: "server", ProcessInstanceID: "candidate-server"}
	grant, err := h.app.ServerUpgradeAuthorize(t.Context(), b)
	if err != nil || !grant.Allowed {
		t.Fatalf("grant %+v %v", grant, err)
	}
	h.clock.Advance(301 * time.Second)
	if refreshed := h.app.RefreshUpgradeGrant(grant); refreshed.Allowed || refreshed.RemainingMillis != 0 {
		t.Fatal("expired receipt renewed authority", refreshed)
	}
	v := upgradeTick(h, ServerObservation{})
	if v.Status != "attention" {
		t.Fatal(v)
	}
	op := h.exec(owner, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: b.UpgradeID}, map[string]string{"action": "retry"}, rev(v.Revision)), ControlUpgrade{})
	if op.Status != protocol.OpFailed {
		t.Fatal("retry renewed expired server authority", op)
	}
	if r := upgradeRead(h); r.ServerGrantDeadline != grant.ExpiresAt.UnixMilli() || r.View.Status != "attention" {
		t.Fatal("expired grant facts changed", r)
	}
}

func TestUpgradeRunnerDrainTimeoutReleasesOnlyUnswitchedHost(t *testing.T) {
	h, c := upgradeHarness(t)
	c.DrainMillis = 1000
	upgradeToRunners(h, c)
	upgradeTick(h, ServerObservation{})
	if upgradeCtl(h, "linux-1").Mode != protocol.RunnerMaintenance {
		t.Fatal("host not draining")
	}
	h.clock.Advance(time.Second)
	if v := upgradeTick(h, ServerObservation{}); v.Status != "attention" {
		t.Fatal(v)
	}
	if ctl := upgradeCtl(h, "linux-1"); ctl.Mode != protocol.RunnerNormal || ctl.Upgrade != nil {
		t.Fatal("unswitched host stuck after drain timeout", ctl)
	}
	if r := upgradeRead(h); r.Hosts[0].CandidateInstance != "" {
		t.Fatal("drain timeout invented candidate")
	}
	paused, err := h.app.UpgradePausesMain(t.Context())
	if err != nil || paused {
		t.Fatal("runner timeout paused main", paused, err)
	}
}
