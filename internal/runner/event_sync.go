package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

// sendEvent returns success only for a committed original. Intermediate
// receipts are resumable byte positions, never permission to retire a row.
func (r *Runner) sendEvent(ctx context.Context, as protocol.Assignment, env *protocol.Envelope) error {
	if !as.EventSync || len(env.Payload) <= maxBatchBody {
		op, err := r.sendOnce(ctx, runCredential(as), env)
		if err != nil {
			return err
		}
		if op.Status != protocol.OpSucceeded {
			return fmt.Errorf("event receipt pending: %s", op.Status)
		}
		return nil
	}
	sum := sha256.Sum256(env.Payload)
	part := protocol.RunEventPart{Operation: env.Operation, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(env.Payload))}
	for part.Offset < part.Size {
		end := min(part.Offset+protocol.RunEventPartBytes, part.Size)
		part.Data = env.Payload[part.Offset:end]
		piece := *env
		piece.Operation = protocol.OpRunnerEventPart
		var err error
		piece.Payload, err = json.Marshal(part)
		if err != nil {
			return err
		}
		op, err := r.sendOnce(ctx, runCredential(as), &piece)
		if err != nil {
			return err
		}
		var receipt protocol.RunEventPartReceipt
		if err = json.Unmarshal(op.Result, &receipt); err != nil {
			return err
		}
		if op.Status != protocol.OpSucceeded || receipt.SHA256 != part.SHA256 || receipt.Next < end || receipt.Next > part.Size || receipt.Stored != (receipt.Next == part.Size) {
			return fmt.Errorf("inconsistent event part receipt at %d", part.Offset)
		}
		part.Offset = receipt.Next
	}
	return nil
}

func (r *Runner) wakeEventSync() {
	select {
	case r.eventWake <- struct{}{}:
	default:
	}
}

func (r *Runner) syncJournalEvents(ctx context.Context) {
	var prune <-chan time.Time
	pruneSpool := func() {
		prune = nil
		if _, err := r.cfg.Journal.PruneSpool(ctx); err != nil && ctx.Err() == nil {
			r.cfg.Log.WarnContext(ctx, "prune acknowledged run event originals", "err", err)
			prune = time.After(time.Minute)
		}
	}
	for ctx.Err() == nil {
		select {
		case <-prune:
			pruneSpool()
		default:
		}
		runs, err := r.cfg.Journal.PendingEventRuns(ctx)
		if err != nil {
			r.cfg.Log.ErrorContext(ctx, "read pending run events", "err", err)
		}
		progress := false
		for _, as := range runs {
			sent, err := r.flushEventBatch(ctx, as)
			progress = progress || sent
			if err != nil && ctx.Err() == nil {
				r.cfg.Log.WarnContext(ctx, "run event sync pending", "run_id", as.RunID, "err", err)
			}
		}
		if progress {
			if prune == nil {
				prune = time.After(time.Second)
			}
			continue
		}
		delay := r.cfg.EventFlush
		if len(runs) > 0 {
			delay = min(delay, r.cfg.RetryDelay)
		}
		select {
		case <-ctx.Done():
			return
		case <-prune:
			pruneSpool()
		case <-r.eventWake:
		case <-time.After(delay):
		}
	}
}
