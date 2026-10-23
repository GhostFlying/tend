package app

import (
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestUpgradeNewBindingClearsConfirmedPreviousCancellation(t *testing.T) {
	for _, serverChange := range []bool{false, true} {
		t.Run(map[bool]string{false: "runner-binding", true: "server-binding"}[serverChange], func(t *testing.T) {
			h, c := upgradeHarness(t)
			if op := startTestUpgrade(h, "old", c); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			old := upgradeRead(h)
			if op := h.exec(owner, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: "old"}, map[string]string{"action": "cancel"}, rev(old.View.Revision)), ControlUpgrade{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			for _, host := range []string{"linux-1", "linux-2"} {
				upgradeAck(h, host, protocol.RunnerNormal)
				if upgradeCtl(h, host).CancelledUpgradeID != "old" {
					t.Fatal("missing previous cancellation")
				}
			}
			if op := startTestUpgrade(h, "new", c); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			// Preserve the completed cancellation until this batch actually binds.
			if upgradeCtl(h, "linux-1").CancelledUpgradeID != "old" {
				t.Fatal("preflight prematurely removed marker")
			}
			serverHash := c.ServerHash
			if serverChange {
				serverHash = strings.Repeat("a", 64)
			}
			upgradeTick(h, ServerObservation{MainDrained: true, BinarySHA256: serverHash})
			if !serverChange {
				upgradeTick(h, ServerObservation{})
			}
			ctl := upgradeCtl(h, "linux-1")
			if ctl.UpgradeID != "new" || ctl.Mode != protocol.RunnerMaintenance || ctl.CancelledUpgradeID != "" {
				t.Fatal("old cancellation leaked into new binding", ctl)
			}
			second := upgradeCtl(h, "linux-2")
			if serverChange {
				if second.UpgradeID != "new" || second.CancelledUpgradeID != "" {
					t.Fatal("fleet binding kept old cancellation", second)
				}
			} else if second.CancelledUpgradeID != "old" || second.UpgradeID != "" {
				t.Fatal("unbound host marker was cleared", second)
			}
		})
	}
}
