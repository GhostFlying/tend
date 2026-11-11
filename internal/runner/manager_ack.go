package runner

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func (r *Runner) supportsManagerAcknowledgements() bool {
	r.controlMu.Lock()
	defer r.controlMu.Unlock()
	return r.runnerProtocol >= 4
}

func (r *Runner) managerClaimPayload(ctx context.Context, as protocol.Assignment, only bool) (*protocol.ManagerNativeClaimPayload, error) {
	if !r.supportsManagerAcknowledgements() {
		return nil, nil
	}
	ids, err := r.cfg.Journal.ManagerReceiptAcknowledgements(ctx, as.RunID)
	if err != nil {
		return nil, err
	}
	return &protocol.ManagerNativeClaimPayload{AcknowledgedIDs: ids, AcknowledgeOnly: only}, nil
}

func (r *Runner) confirmManagerClaim(ctx context.Context, as protocol.Assignment, env *protocol.Envelope) error {
	if !r.supportsManagerAcknowledgements() || len(env.Payload) == 0 {
		return nil
	}
	var payload protocol.ManagerNativeClaimPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		return err
	}
	return r.cfg.Journal.ConfirmManagerAcknowledgements(ctx, as.RunID, payload.AcknowledgedIDs)
}

// This acknowledges receipt delivery, not another command. Uncertainty retains
// compact IDs for a future Hello without retaining a process or execution slot.
func (r *Runner) flushManagerAcknowledgements(ctx context.Context, as protocol.Assignment) error {
	payload, err := r.managerClaimPayload(ctx, as, true)
	if err != nil || payload == nil || len(payload.AcknowledgedIDs) == 0 {
		return err
	}
	env, err := r.newEnvelope(protocol.OpManagerNativeClaim, runTarget(as), payload)
	if err != nil {
		return err
	}
	attempt, cancel := context.WithTimeout(ctx, min(r.cfg.RequestTimeout, time.Second))
	defer cancel()
	op, err := r.sendOnce(attempt, runCredential(as), env)
	if err != nil {
		return err
	}
	if op.Status != protocol.OpSucceeded {
		return errors.New("runner: native acknowledgement awaits peer confirmation")
	}
	return r.confirmManagerClaim(ctx, as, env)
}
func (r *Runner) replayManagerAcknowledgements(ctx context.Context) {
	if !r.supportsManagerAcknowledgements() {
		return
	}
	assignments, err := r.cfg.Journal.ManagerAcknowledgementRuns(ctx)
	if err != nil {
		r.cfg.Log.WarnContext(ctx, "native delivery acknowledgement inventory unavailable", "err", err)
		return
	}
	for _, as := range assignments {
		if err := r.flushManagerAcknowledgements(ctx, as); err != nil {
			r.cfg.Log.WarnContext(ctx, "native delivery acknowledgement remains pending", "run_id", as.RunID, "err", err)
		}
	}
}
