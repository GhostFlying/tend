package app

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
)

func TestSelfConfigurationUntargetedCapability(t *testing.T) {
	h, c := configurationUpgradeHarness(t)
	selfUpgradeHosts(t, h, true)
	c.ServerUpdaterSupported = true
	c.ReleaseSet = &release.Set{Format: 2, ReleaseID: c.Manifest.ReleaseID, Commit: strings.Repeat("a", 40), OS: "linux", Arch: "amd64", ManifestSHA256: c.ManifestSHA256, Artifacts: append([]release.Artifact{}, c.Manifest.Artifacts...)}
	for _, role := range []string{"updater", "cli"} {
		c.ReleaseSet.Artifacts = append(c.ReleaseSet.Artifacts, release.Artifact{Role: role, OS: "linux", Arch: "amd64", Path: role, Size: 12, SHA256: strings.Repeat("e", 64)})
	}
	raw, _ := json.Marshal(c.ReleaseSet)
	c.ReleaseSetSize = int64(len(raw))
	c.ReleaseSetSHA256 = strings.Repeat("f", 64)
	rt := upgradeAck(h, "linux-2", protocol.RunnerNormal)
	rt.Sequence++
	rt.Capabilities = slices.DeleteFunc(rt.Capabilities, func(cap string) bool { return cap == protocol.CapabilityUpdaterSelfUpdate })
	if _, err := h.app.RuntimeHeartbeat(t.Context(), runner("linux-2"), protocol.HeartbeatPayload{Runtime: &rt}); err != nil {
		t.Fatal(err)
	}
	if op := startTestUpgrade(h, "config-untargeted-capability", c); op.Error != nil {
		t.Fatalf("untargeted linux-2 blocked linux-1 configuration maintenance: %v", op.Error)
	}
}

func TestSelfLateRunnerEvidence(t *testing.T) {
	for _, kind := range []string{"receipt", "rejection", "other_request"} {
		t.Run(kind, func(t *testing.T) {
			h, _, o := selfUpdaterOnlyReady(t)
			grantSelfRunner(t, h, "linux-1", o)
			h.clock.Advance(7 * time.Minute)
			upgradeAck(h, "linux-1", protocol.RunnerReady)
			upgradeAck(h, "linux-2", protocol.RunnerNormal)
			if v := upgradeTick(h, o); v.Status != "attention" {
				t.Fatal(v)
			}
			before := upgradeRead(h)
			control := upgradeCtl(h, "linux-1")
			if kind == "receipt" {
				selfRuntimeProof(t, h, "linux-1", protocol.RunnerReady)
			} else {
				rt := upgradeAck(h, "linux-1", protocol.RunnerReady)
				rt.Sequence++
				proof := &protocol.UpdaterSelfRejection{RequestID: before.StartRequestID, UpgradeID: before.View.UpgradeID, ReleaseSetSHA256: before.ReleaseSetSHA256, InstanceID: "linux-1", RejectedAtMillis: h.clock.Now().UnixMilli()}
				if kind == "other_request" {
					proof.RequestID = "another-original-request"
				}
				rt.Updater = &protocol.UpdaterSelfObservation{InstanceID: "linux-1", Rejection: proof}
				if _, err := h.app.RuntimeHeartbeat(t.Context(), runner("linux-1"), protocol.HeartbeatPayload{Runtime: &rt}); err != nil {
					t.Fatal(err)
				}
			}
			v := upgradeTick(h, o)
			proof := v.Hosts[0].Updater
			if kind == "other_request" {
				if proof != nil {
					t.Fatal("unrelated proof was attributed to paused batch", proof)
				}
			} else if proof == nil || kind == "receipt" && proof.Receipt == nil || kind == "rejection" && proof.Rejection == nil {
				t.Fatal("late original evidence missing from paused batch owner view", v.Hosts[0])
			}
			after := upgradeRead(h)
			if v.Status != "attention" || v.Phase != before.View.Phase || after.Deadline != before.Deadline || after.View.OverallDeadlineMillis != before.View.OverallDeadlineMillis || !reflect.DeepEqual(upgradeCtl(h, "linux-1"), control) {
				t.Fatal("late proof advanced or renewed paused handoff", v)
			}
		})
	}
}
