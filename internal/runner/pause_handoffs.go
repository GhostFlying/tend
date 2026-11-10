package runner

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

type pauseYield struct{}

func (*pauseYield) Error() string { return "yield execution for Issue pause" }

func (r *Runner) finishPause(ctx context.Context, as protocol.Assignment) error {
	p, err := r.cfg.Journal.Pause(ctx, as.RunID)
	if err != nil {
		return err
	}
	if p == nil {
		return errors.New("runner: pause identity unavailable")
	}
	saved, err := r.cfg.Journal.Run(ctx, as.RunID)
	if err != nil {
		return err
	}
	if !saved.StoppedConfirmed {
		return &completionPending{errors.New("runner: pause awaits physical stop proof")}
	}
	env, err := r.newEnvelope(protocol.OpRunsYieldPause, runTarget(as), protocol.YieldPausePayload{PauseHandoff: *p, ThreadID: saved.Thread})
	if err != nil {
		return err
	}
	if err := r.cfg.Journal.Finish(ctx, as.RunID, journal.PhaseReleasing, journal.Request{RunID: as.RunID, Envelope: *env}); err != nil {
		return err
	}
	return r.reportPause(ctx, as)
}

// reportPause retains every original receipt until it has its own ACK. A pause
// never runs workspace completion/cleanup and never replaces unknown work.
func (r *Runner) reportPause(ctx context.Context, as protocol.Assignment) error {
	requests, err := r.cfg.Journal.Requests(ctx)
	if err != nil {
		return err
	}
	var pause *journal.Request
	for i := range requests {
		if requests[i].RunID == as.RunID && requests[i].Envelope.Operation == protocol.OpRunsYieldPause {
			pause = &requests[i]
			break
		}
	}
	if pause == nil {
		return errors.New("runner: original pause receipt unavailable")
	}
	if as.Manager == nil {
		if err := r.flushEvents(ctx, as); err != nil {
			return &completionPending{err}
		}
	}
	op, err := r.send(ctx, runCredential(as), &pause.Envelope)
	if err != nil || op.Status != protocol.OpSucceeded {
		return &completionPending{errors.Join(err, errors.New("runner: pause receipt awaits settlement"))}
	}
	if as.Manager != nil {
		if err := r.flushManagerEvents(ctx, as); err != nil {
			return &completionPending{err}
		}
		for _, p := range requests {
			if p.RunID != as.RunID || p.Envelope.Operation != protocol.OpManagerNativeReply {
				continue
			}
			if err := r.sendManagerReceipt(ctx, as, &p.Envelope); err != nil {
				return &completionPending{err}
			}
			if err := r.cfg.Journal.DeleteRequest(ctx, p.Envelope.RequestID); err != nil {
				return err
			}
		}
	}
	if err := r.recoverRunMessages(ctx, as); err != nil {
		return &completionPending{err}
	}
	return r.finishJournal(ctx, as.RunID)
}

func (r *Runner) pauseReceiptReady(ctx context.Context, run journal.Run) (bool, error) {
	if !run.Assignment.PauseHandoffs || run.Phase != journal.PhaseReleasing || !run.StoppedConfirmed || run.Held {
		return false, nil
	}
	p, err := r.cfg.Journal.Pause(ctx, run.Assignment.RunID)
	if err != nil || p == nil {
		return false, err
	}
	reqs, err := r.cfg.Journal.Requests(ctx)
	if err != nil {
		return false, err
	}
	for _, req := range reqs {
		if req.RunID != run.Assignment.RunID || req.Envelope.Operation != protocol.OpRunsYieldPause {
			continue
		}
		var payload protocol.YieldPausePayload
		if json.Unmarshal(req.Envelope.Payload, &payload) != nil {
			return false, nil
		}
		return payload.PauseHandoff == *p && payload.ThreadID == run.Thread, nil
	}
	return false, nil
}
