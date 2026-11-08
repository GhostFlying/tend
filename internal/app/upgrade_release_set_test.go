package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/store"
)

func TestUpgradeCompleteSetRequiresNegotiationAndPinsDownloads(t *testing.T) {
	h, c := upgradeHarness(t)
	s := release.Set{Format: 1, ReleaseID: c.Manifest.ReleaseID, Commit: strings.Repeat("a", 40), OS: "linux", Arch: "amd64", ManifestSHA256: c.ManifestSHA256, Artifacts: append([]release.Artifact{}, c.Manifest.Artifacts...)}
	for _, role := range []string{"updater", "cli"} {
		s.Artifacts = append(s.Artifacts, release.Artifact{Role: role, OS: "linux", Arch: "amd64", Path: role, Size: 12, SHA256: strings.Repeat("e", 64)})
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	c.ReleaseSet, c.ReleaseSetSHA256, c.ReleaseSetSize = &s, hex.EncodeToString(sum[:]), int64(len(raw))
	if op := startTestUpgrade(h, "needs-bootstrap", c); op.Error == nil || op.Error.Code != protocol.CodeInvalidTransition {
		t.Fatal("complete release silently accepted old download capability", op)
	}
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
		if op := h.versionedHello(host, &row.Runtime); op.Error != nil {
			t.Fatal(op)
		}
	}
	upgradeToRunners(h, c)
	row := upgradeRead(h)
	if row.ReleaseSetSHA256 != c.ReleaseSetSHA256 || row.ReleaseSetSize != c.ReleaseSetSize || row.ReleaseSet == nil {
		t.Fatal("ledger dropped complete release identity")
	}
	upgradeTick(h, ServerObservation{})
	for _, host := range []string{"linux-1", "linux-2"} {
		control := upgradeCtl(h, host)
		if control.Mode != protocol.RunnerMaintenance {
			continue
		}
		upgradeAck(h, host, protocol.RunnerReady)
		upgradeTick(h, ServerObservation{})
		control = upgradeCtl(h, host)
		if control.Upgrade == nil || control.Upgrade.ReleaseSet == nil || control.Upgrade.ReleaseSet.SHA256 != c.ReleaseSetSHA256 || control.Upgrade.ReleaseSet.Size != c.ReleaseSetSize {
			t.Fatal("runner target dropped release set", control)
		}
		return
	}
	t.Fatal("no rolling runner selected")
}
