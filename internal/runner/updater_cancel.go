package runner

import (
	"context"
	"time"

	"github.com/GhostFlying/tend/internal/updater"
)

// maybeCancelStaged reconciles a durable, explicit batch cancellation. Temporary
// normal controls during attention/retry never imply cancellation of a release.
func (r *Runner) maybeCancelStaged(ctx context.Context) {
	if r.cfg.UpdaterControlSocket == "" || r.cfg.UpdaterInstanceID == "" {
		return
	}
	r.controlMu.Lock()
	id := r.control.CancelledUpgradeID
	done := r.cancelledUpdaterID == id
	r.controlMu.Unlock()
	if id == "" || done || !r.updaterMu.TryLock() {
		return
	}
	r.wg.Go(func() {
		defer r.updaterMu.Unlock()
		ctx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		state, err := updater.CallSocket(ctx, r.cfg.UpdaterControlSocket, "status", updater.ControlRequest{})
		if err != nil || state.InstanceID != r.cfg.UpdaterInstanceID {
			return
		}

		complete := confirmedTerminal(state, id, r.cfg.UpdaterInstanceID)
		if !complete {
			op := state.Operation
			if op != nil && op.ID == id && (op.Kind != "upgrade" || op.Binding.InstanceID != r.cfg.UpdaterInstanceID || op.Binding.UpgradeID != id || op.Phase != "staged") {
				return
			}
			// Even an absent operation may still have an old /upgrade POST in flight.
			// Explicit cancellation durably fences that ID before we acknowledge it.
			state, err = updater.CallSocket(ctx, r.cfg.UpdaterControlSocket, "cancel", updater.ControlRequest{ID: id, ExpectedPhase: "staged"})
			if err != nil {
				return
			}
			complete = confirmedTerminal(state, id, r.cfg.UpdaterInstanceID)
		}

		if complete {
			r.controlMu.Lock()
			if r.control.CancelledUpgradeID == id {
				r.cancelledUpdaterID = id
			}
			r.controlMu.Unlock()
		}
	})
}

func confirmedTerminal(state updater.State, id, instance string) bool {
	receipt, ok := state.Terminals[id]
	if !ok || receipt.Binding.UpgradeID != id || receipt.Binding.InstanceID != instance {
		return false
	}
	switch receipt.Phase {
	case "cancelled", "committed", "rolled_back":
		return true
	}
	return false
}
