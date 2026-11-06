package runner

import (
	"context"
	"os"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/updater"
)

// boundedAdmissionProcessMatches separates the immutable installed candidate
// from a later supervised process of the same committed version. A fresh launch
// is accepted only through the configured updater's durable proof and the actual
// current kernel identity; it is never a new permission for original native work.
// Call before taking controlMu: socket inspection must not hold that mutex.
func (r *Runner) boundedAdmissionProcessMatches(ctx context.Context, target protocol.UpgradeBinding) bool {
	if target.ProcessInstanceID == "" || target.ArtifactSHA256 != r.cfg.Build.BinarySHA256 ||
		r.cfg.UpgradeID != "" && r.cfg.UpgradeID != target.UpgradeID {
		return false
	}
	if target.ProcessInstanceID == r.cfg.InstanceID {
		return true
	}
	if r.cfg.UpdaterControlSocket == "" || r.cfg.UpdaterInstanceID == "" || target.InstanceID != r.cfg.UpdaterInstanceID {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	state, err := updater.CallSocket(ctx, r.cfg.UpdaterControlSocket, "status", updater.ControlRequest{})
	if err != nil || state.InstanceID != r.cfg.UpdaterInstanceID {
		return false
	}
	receipt := updater.ProcessReceipt(state)
	if receipt == nil || !receipt.CommittedConfirmed || receipt.RollbackConfirmed || receipt.Phase != "committed" ||
		receipt.Binding != target || receipt.CurrentProcessInstanceID != r.cfg.InstanceID || receipt.CurrentSHA256 != r.cfg.Build.BinarySHA256 {
		return false
	}
	actual, err := (updater.LinuxProcess{}).Inspect(ctx, os.Getpid())
	return err == nil && actual == state.Process && actual.LaunchID == r.cfg.InstanceID && actual.SHA256 == r.cfg.Build.BinarySHA256
}
