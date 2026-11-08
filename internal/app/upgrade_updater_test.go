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

func selfUpgradeFixture(t *testing.T) (*harness, StartUpgrade) {
	t.Helper()
	h, c := upgradeHarness(t)
	c.ReleaseSet = &release.Set{Format: 2, ReleaseID: c.Manifest.ReleaseID, Commit: strings.Repeat("a", 40), OS: "linux", Arch: "amd64", ManifestSHA256: c.ManifestSHA256, Artifacts: append([]release.Artifact{}, c.Manifest.Artifacts...)}
	for _, role := range []string{"updater", "cli"} {
		c.ReleaseSet.Artifacts = append(c.ReleaseSet.Artifacts, release.Artifact{Role: role, OS: "linux", Arch: "amd64", Path: role, Size: 12, SHA256: strings.Repeat("e", 64)})
	}
	c.ReleaseSetSHA256 = strings.Repeat("f", 64)
	raw, _ := json.Marshal(c.ReleaseSet)
	c.ReleaseSetSize = int64(len(raw))
	return h, c
}
func selfUpgradeHosts(t *testing.T, h *harness, self bool) {
	t.Helper()
	for _, host := range []string{"linux-1", "linux-2"} {
		var row store.HostRuntimeRow
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			var err error
			row, err = store.GetHostRuntime(t.Context(), tx.Q(), host)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		row.Runtime.Sequence++
		row.Runtime.Capabilities = append(row.Runtime.Capabilities, protocol.CapabilityCompleteReleaseSet)
		if self {
			row.Runtime.Capabilities = append(row.Runtime.Capabilities, protocol.CapabilityUpdaterSelfUpdate)
		}
		if op := h.versionedHello(host, &row.Runtime); op.Error != nil {
			t.Fatal(op)
		}
	}
}
func selfUpgradeObservation(row store.UpgradeRow, instance, childLaunch, childHash string) *protocol.UpdaterSelfObservation {
	supervisor := protocol.RecoveryIdentity{PID: 90, StartTicks: 9, SHA256: strings.Repeat("e", 64)}
	child := protocol.RecoveryIdentity{PID: 91, StartTicks: 10, LaunchID: childLaunch, SHA256: childHash}
	return &protocol.UpdaterSelfObservation{InstanceID: instance, Process: supervisor, SupervisedProcess: child, Receipt: &protocol.UpdaterSelfReceipt{RequestID: row.StartRequestID, UpgradeID: row.View.UpgradeID, ReleaseSetSHA256: row.ReleaseSetSHA256, InstanceID: instance, Previous: supervisor, Installed: supervisor, RetainedProcess: child, CompletedAtMillis: row.View.CreatedAt.UnixMilli()}}
}
func TestSelfUpgradeRequiresBothLocalAndRunnerCapability(t *testing.T) {
	h, c := selfUpgradeFixture(t)
	selfUpgradeHosts(t, h, false)
	if op := startTestUpgrade(h, "no-local-capability", c); op.Error == nil {
		t.Fatal("new release silently skipped server updater")
	}
	c.ServerUpdaterSupported = true
	if op := startTestUpgrade(h, "no-runner-capability", c); op.Error == nil {
		t.Fatal("new release silently skipped runner updater")
	}
	selfUpgradeHosts(t, h, true)
	if op := startTestUpgrade(h, "self-first", c); op.Error != nil {
		t.Fatal(op)
	}
	row := upgradeRead(h)
	if row.View.Phase != "server_updater" {
		t.Fatal("services drained before updater stage", row.View)
	}
	observation := ServerObservation{MainDrained: true, BinarySHA256: c.ServerHash, ProcessInstanceID: "original-server"}
	if v := upgradeTick(h, observation); v.Phase != "server_updater" {
		t.Fatal("service hash bypassed updater proof", v)
	}
	observation.Updater = selfUpgradeObservation(row, "server", "original-server", c.ServerHash)
	observation.Updater.Receipt.RequestID = "another-attempt"
	if v := upgradeTick(h, observation); v.Phase != "server_updater" {
		t.Fatal("foreign receipt admitted", v)
	}
	observation.Updater.Receipt.RequestID = row.StartRequestID
	if v := upgradeTick(h, observation); v.Phase != "server_draining" || v.ServerUpdater == nil {
		t.Fatal("verified handoff did not resume original stage", v)
	}
}
func TestSelfUpgradeProofBindsRequestPlatformAndActualProcesses(t *testing.T) {
	h, c := selfUpgradeFixture(t)
	selfUpgradeHosts(t, h, true)
	c.ServerUpdaterSupported = true
	if op := startTestUpgrade(h, "self-proof", c); op.Error != nil {
		t.Fatal(op)
	}
	row := upgradeRead(h)
	for _, kind := range []string{"valid", "request", "set", "target", "running", "service", "instance"} {
		t.Run(kind, func(t *testing.T) {
			o := selfUpgradeObservation(row, "runner", "process", c.Manifest.Artifacts[1].SHA256)
			rt := protocol.RunnerRuntime{InstanceID: "process", Build: protocol.RunnerBuild{PID: 91, BinarySHA256: c.Manifest.Artifacts[1].SHA256}, Updater: o}
			switch kind {
			case "request":
				o.Receipt.RequestID = "other"
			case "set":
				o.Receipt.ReleaseSetSHA256 = strings.Repeat("a", 64)
			case "target":
				o.Receipt.Installed.SHA256 = strings.Repeat("b", 64)
			case "running":
				o.Process.SHA256 = strings.Repeat("c", 64)
			case "service":
				o.SupervisedProcess.PID++
			case "instance":
				o.InstanceID = "other"
			}
			match := runnerSelfMatches(row, store.UpgradeHost{HostID: "runner", TargetHash: c.Manifest.Artifacts[1].SHA256}, rt)
			if match != (kind == "valid") {
				t.Fatal("proof match", kind, match)
			}
		})
	}
}

func TestSelfUpgradeRetryRetainsUnresolvedSupervisorPhase(t *testing.T) {
	h, c := selfUpgradeFixture(t)
	selfUpgradeHosts(t, h, true)
	c.ServerUpdaterSupported = true
	if op := startTestUpgrade(h, "retry-self", c); op.Error != nil {
		t.Fatal(op)
	}
	h.clock.Advance(7 * time.Minute)
	v := upgradeTick(h, ServerObservation{})
	if v.Status != "attention" || v.Phase != "server_updater" {
		t.Fatal(v)
	}
	op := h.exec(owner, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: v.UpgradeID}, protocol.UpgradeControlPayload{Action: "retry"}, rev(v.Revision)), ControlUpgrade{})
	if op.Error != nil {
		t.Fatal(op)
	}
	if row := upgradeRead(h); row.View.Phase != "server_updater" {
		t.Fatal("retry bypassed original handoff", row.View)
	}
}

func TestSelfUpgradeCancelWaitsForOriginalHandoffAndAcceptsLateReceipt(t *testing.T) {
	h, c := selfUpgradeFixture(t)
	selfUpgradeHosts(t, h, true)
	c.ServerUpdaterSupported = true
	if op := startTestUpgrade(h, "cancel-self", c); op.Error != nil {
		t.Fatal(op)
	}
	row := upgradeRead(h)
	grant := AuthorizeServerUpdater{}
	if op := h.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, h.env(grant.Operation(), protocol.Target{Type: "upgrade", ID: row.View.UpgradeID}, nil, rev(row.View.Revision)), grant); op.Error != nil {
		t.Fatal(op)
	}
	cancel := func() (protocol.Operation, error) {
		v := upgradeRead(h).View
		return h.app.Execute(t.Context(), owner, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: v.UpgradeID}, protocol.UpgradeControlPayload{Action: "cancel"}, rev(v.Revision)), ControlUpgrade{})
	}
	if op, err := cancel(); protocol.CodeOf(err) != protocol.CodeUnknownOutcome {
		t.Fatal("unconfirmed installer was treated as cancelled", op, err)
	}
	h.clock.Advance(7 * time.Minute)
	if v := upgradeTick(h, ServerObservation{}); v.Status != "attention" {
		t.Fatal(v)
	}
	observation := ServerObservation{BinarySHA256: c.ServerHash, ProcessInstanceID: "original-server"}
	observation.Updater = selfUpgradeObservation(row, "server", "original-server", c.ServerHash)
	v := upgradeTick(h, observation)
	if v.Status != "attention" || v.ServerUpdater == nil || v.Phase != "server_updater" {
		t.Fatal("late receipt resumed dispatch or was discarded", v)
	}
	if op, err := cancel(); err != nil || op.Error != nil || upgradeRead(h).View.Status != "cancelled" {
		t.Fatal("confirmed original handoff did not permit cancellation", op, err)
	}
}

func TestSelfUpgradeExpiredUndispatchedBatchCanBeCancelled(t *testing.T) {
	h, c := selfUpgradeFixture(t)
	selfUpgradeHosts(t, h, true)
	c.ServerUpdaterSupported = true
	if op := startTestUpgrade(h, "expired-no-dispatch", c); op.Error != nil {
		t.Fatal(op)
	}
	h.clock.Advance(21 * time.Minute)
	v := upgradeTick(h, ServerObservation{})
	grant := AuthorizeServerUpdater{}
	op := h.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, h.env(grant.Operation(), protocol.Target{Type: "upgrade", ID: v.UpgradeID}, nil, rev(v.Revision)), grant)
	if op.Error == nil || upgradeRead(h).ServerUpdaterAuthorized {
		t.Fatal("expired batch admitted an installer", op)
	}
	op = h.exec(owner, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: v.UpgradeID}, protocol.UpgradeControlPayload{Action: "cancel"}, rev(v.Revision)), ControlUpgrade{})
	if op.Error != nil || upgradeRead(h).View.Status != "cancelled" {
		t.Fatal("known undispatched batch could not be cancelled", op)
	}
	v = upgradeRead(h).View
	op = h.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, h.env(grant.Operation(), protocol.Target{Type: "upgrade", ID: v.UpgradeID}, nil, rev(v.Revision)), grant)
	if op.Error == nil {
		t.Fatal("cancelled batch admitted a stale coordinator", op)
	}
}

func TestSelfUpgradeDefiniteRejectionAllowsCancelAfterDeadline(t *testing.T) {
	h, c := selfUpgradeFixture(t)
	selfUpgradeHosts(t, h, true)
	c.ServerUpdaterSupported = true
	if op := startTestUpgrade(h, "rejected-handoff", c); op.Error != nil {
		t.Fatal(op)
	}
	r := upgradeRead(h)
	grant := AuthorizeServerUpdater{}
	if op := h.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, h.env(grant.Operation(), protocol.Target{Type: "upgrade", ID: r.View.UpgradeID}, nil, rev(r.View.Revision)), grant); op.Error != nil {
		t.Fatal(op)
	}
	o := ServerObservation{Updater: &protocol.UpdaterSelfObservation{InstanceID: "server", Rejection: &protocol.UpdaterSelfRejection{RequestID: "foreign", UpgradeID: r.View.UpgradeID, ReleaseSetSHA256: r.ReleaseSetSHA256, InstanceID: "server", RejectedAtMillis: h.clock.Now().UnixMilli()}}}
	if v := upgradeTick(h, o); v.ServerUpdater != nil {
		t.Fatal("foreign rejection changed the original attempt", v)
	}
	h.clock.Advance(21 * time.Minute)
	o.Updater.Rejection.RequestID = r.StartRequestID
	v := upgradeTick(h, o)
	if v.Status != "attention" || v.ServerUpdater == nil || v.ServerUpdater.Rejection == nil {
		t.Fatal("late definitive rejection was lost", v)
	}
	op, err := h.app.Execute(t.Context(), owner, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: v.UpgradeID}, protocol.UpgradeControlPayload{Action: "cancel"}, rev(v.Revision)), ControlUpgrade{})
	if err != nil || op.Error != nil || upgradeRead(h).View.Status != "cancelled" {
		t.Fatal("known rejected attempt remained uncancellable", op, err)
	}
}
func TestSelfUpgradeConfigurationKeepsUntargetedHostsOutsideProofScope(t *testing.T) {
	h, c := configurationUpgradeHarness(t)
	selfUpgradeHosts(t, h, true)
	c.ServerUpdaterSupported = true
	configuration := c.RunnerConfigurations["linux-1"]
	c.RunnerConfigurations = map[string]protocol.RunnerConfiguration{"linux-2": configuration}
	rt := upgradeAck(h, "linux-2", protocol.RunnerNormal)
	rt.Sequence++
	rt.Capabilities = append(rt.Capabilities, protocol.CapabilityRunnerConfiguration)
	if _, err := h.app.RuntimeHeartbeat(t.Context(), runner("linux-2"), protocol.HeartbeatPayload{Runtime: &rt}); err != nil {
		t.Fatal(err)
	}
	c.ReleaseSet = &release.Set{Format: 2, ReleaseID: c.Manifest.ReleaseID, Commit: strings.Repeat("a", 40), OS: "linux", Arch: "amd64", ManifestSHA256: c.ManifestSHA256, Artifacts: append([]release.Artifact{}, c.Manifest.Artifacts...)}
	for _, role := range []string{"updater", "cli"} {
		c.ReleaseSet.Artifacts = append(c.ReleaseSet.Artifacts, release.Artifact{Role: role, OS: "linux", Arch: "amd64", Path: role, Size: 12, SHA256: strings.Repeat("e", 64)})
	}
	raw, _ := json.Marshal(c.ReleaseSet)
	c.ReleaseSetSize = int64(len(raw))
	c.ReleaseSetSHA256 = strings.Repeat("f", 64)
	if op := startTestUpgrade(h, "config-self", c); op.Error != nil {
		t.Fatal(op)
	}
	row := upgradeRead(h)
	if row.View.Phase != "server_updater" || !row.SupervisorThenRunners {
		t.Fatal("configuration lost its supervisor prerequisite", row.View)
	}
	upgradeAck(h, "linux-2", protocol.RunnerReady)
	observation := ServerObservation{BinarySHA256: c.ServerHash, ProcessInstanceID: "server"}
	observation.Updater = selfUpgradeObservation(row, "server", "server", c.ServerHash)
	if v := upgradeTick(h, observation); v.Phase != "runners" {
		t.Fatal("configuration unnecessarily drained server", v)
	}
	upgradeTick(h, observation)
	ctl := upgradeCtl(h, "linux-2")
	if ctl.Upgrade == nil || ctl.Upgrade.Updater == nil || ctl.Upgrade.Configuration == nil {
		t.Fatal("untargeted host blocked configuration maintenance", ctl)
	}
	if untouched := upgradeCtl(h, "linux-1"); untouched.Mode != protocol.RunnerNormal || untouched.Upgrade != nil {
		t.Fatal("configuration changed an untargeted host", untouched)
	}
	grantSelfRunner(t, h, "linux-2", observation)
	h.clock.Advance(7 * time.Minute)
	upgradeAck(h, "linux-2", protocol.RunnerReady)
	v := upgradeTick(h, observation)
	ctl = upgradeCtl(h, "linux-2")
	if v.Status != "attention" || ctl.Upgrade == nil || ctl.Upgrade.Updater == nil || ctl.Mode != protocol.RunnerMaintenance {
		t.Fatal("timeout discarded an unconfirmed independent installer", v, ctl)
	}
	op, err := h.app.Execute(t.Context(), owner, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: v.UpgradeID}, protocol.UpgradeControlPayload{Action: "cancel"}, rev(v.Revision)), ControlUpgrade{})
	if protocol.CodeOf(err) != protocol.CodeUnknownOutcome {
		t.Fatal("pending runner supervisor was treated as cancelled", op, err)
	}
}
