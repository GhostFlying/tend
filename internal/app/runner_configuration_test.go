package app

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func configurationUpgradeHarness(t *testing.T) (*harness, StartUpgrade) {
	t.Helper()
	h, c := upgradeHarness(t)
	c.CurrentServerHash = c.ServerHash
	c.Manifest.Artifacts[1].SHA256 = strings.Repeat("a", 64)
	c.RunnerConfigurations = map[string]protocol.RunnerConfiguration{"linux-1": {PreviousSHA256: strings.Repeat("e", 64), SHA256: strings.Repeat("f", 64), Providers: []string{"github", "codebase"}}}
	rt := upgradeAck(h, "linux-1", protocol.RunnerNormal)
	rt.Sequence++
	rt.Capabilities = append(rt.Capabilities, protocol.CapabilityRunnerConfiguration)
	if _, err := h.app.RuntimeHeartbeat(t.Context(), runner("linux-1"), protocol.HeartbeatPayload{Runtime: &rt}); err != nil {
		t.Fatal(err)
	}
	return h, c
}

func TestConfigurationMaintenanceAdmissionAndHealth(t *testing.T) {
	h, c := configurationUpgradeHarness(t)
	if op := startTestUpgrade(h, "config-one", c); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if upgradeRead(h).View.Phase != "runners" || upgradeCtl(h, "linux-1").Mode != protocol.RunnerMaintenance || upgradeCtl(h, "linux-2").Mode != protocol.RunnerNormal {
		t.Fatal("configuration did not fence only its target")
	}
	upgradeAck(h, "linux-1", protocol.RunnerReady)
	upgradeTick(h, ServerObservation{})
	rt := upgradeAck(h, "linux-1", protocol.RunnerReady)
	rt.Sequence++
	rt.ConfigurationSHA256 = c.RunnerConfigurations["linux-1"].PreviousSHA256
	if _, err := h.app.RuntimeHeartbeat(t.Context(), runner("linux-1"), protocol.HeartbeatPayload{Runtime: &rt}); err != nil {
		t.Fatal(err)
	}
	ctl := upgradeCtl(h, "linux-1")
	binding := protocol.UpgradeBinding{UpgradeID: "config-one", InstanceID: "linux-1", ProcessInstanceID: "configured-runner", ReleaseID: c.Manifest.ReleaseID, ManifestSHA256: c.ManifestSHA256, ArtifactSHA256: c.Manifest.Artifacts[1].SHA256}
	op := h.exec(runner("linux-1"), h.env("runner.upgrade_authorize", protocol.Target{Type: "host", ID: "linux-1"}, binding, rev(ctl.Revision)), AuthorizeRunnerUpgrade{})
	var grant protocol.UpgradeGrant
	if op.Status != protocol.OpSucceeded || json.Unmarshal(op.Result, &grant) != nil || !grant.Allowed {
		t.Fatal("configuration stop was not authorized", op)
	}
	candidate := rt
	candidate.InstanceID = binding.ProcessInstanceID
	candidate.Generation++
	candidate.Sequence = 1
	candidate.ConfigurationSHA256 = c.RunnerConfigurations["linux-1"].SHA256
	if op := h.versionedHello("linux-1", &candidate); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	// Missing providers must not become healthy merely because config bytes match.
	for range 2 {
		h.clock.Advance(8 * time.Second)
		upgradeAck(h, "linux-1", protocol.RunnerReady)
		upgradeAck(h, "linux-2", protocol.RunnerNormal)
		upgradeTick(h, ServerObservation{})
	}
	health, err := h.app.RunnerUpgradeHealth(t.Context(), runner("linux-1"), binding)
	if err != nil || health.Healthy || !health.RollbackSafe {
		t.Fatal("missing provider must retain fenced rollback", health, err)
	}
	rt = upgradeAck(h, "linux-1", protocol.RunnerReady)
	rt.Sequence++
	rt.Capabilities = append(rt.Capabilities, protocol.CapabilityDeliveryGitHub, protocol.CapabilityDeliveryCodebase)
	if _, err := h.app.RuntimeHeartbeat(t.Context(), runner("linux-1"), protocol.HeartbeatPayload{Runtime: &rt}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		h.clock.Advance(8 * time.Second)
		upgradeAck(h, "linux-1", protocol.RunnerReady)
		upgradeAck(h, "linux-2", protocol.RunnerNormal)
		upgradeTick(h, ServerObservation{})
	}
	health, err = h.app.RunnerUpgradeHealth(t.Context(), runner("linux-1"), binding)
	if err != nil || !health.Healthy {
		t.Fatal("configured candidate not healthy", health, err)
	}
	if upgradeCtl(h, "linux-1").Mode != protocol.RunnerMaintenance {
		t.Fatal("health enabled claims before durable commit")
	}
	upgradeCommitAck(h, binding)
	upgradeTick(h, ServerObservation{})
	if upgradeCtl(h, "linux-1").Upgrade == nil {
		t.Fatal("configuration identity lost before normal acknowledgement")
	}
	upgradeAck(h, "linux-1", protocol.RunnerNormal)
	upgradeTick(h, ServerObservation{})
	v := upgradeTick(h, ServerObservation{})
	if v.Status != "completed" {
		t.Fatal(v)
	}
}

func TestConfigurationMaintenanceRejectsUnnegotiatedOrChangedBinaries(t *testing.T) {
	for _, kind := range []string{"server", "runner", "unsupported", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			h, c := configurationUpgradeHarness(t)
			switch kind {
			case "server":
				c.CurrentServerHash = strings.Repeat("b", 64)
			case "runner":
				c.Manifest.Artifacts[1].SHA256 = strings.Repeat("b", 64)
			case "unsupported":
				c.RunnerConfigurations["linux-2"] = c.RunnerConfigurations["linux-1"]
				delete(c.RunnerConfigurations, "linux-1")
			case "unknown":
				c.RunnerConfigurations["missing"] = c.RunnerConfigurations["linux-1"]
				delete(c.RunnerConfigurations, "linux-1")
			}
			op := startTestUpgrade(h, "bad-config", c)
			if op.Status == protocol.OpSucceeded {
				t.Fatal("invalid configuration accepted")
			}
			if upgradeCtl(h, "linux-1").Mode != protocol.RunnerNormal || upgradeCtl(h, "linux-2").Mode != protocol.RunnerNormal {
				t.Fatal("rejection changed admission")
			}
		})
	}
}

func TestConfigurationHashIsRequiredAlongsideProviders(t *testing.T) {
	h := store.UpgradeHost{Configuration: &protocol.RunnerConfiguration{SHA256: strings.Repeat("a", 64), Providers: []string{"github"}}}
	rt := protocol.RunnerRuntime{Capabilities: []string{protocol.CapabilityDeliveryGitHub}}
	if runnerConfigurationMatches(h, rt) {
		t.Fatal("capability alone proved configuration")
	}
}
