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

func (r *Runner) settledEvents(ctx context.Context) {
	for ctx.Err() == nil {
		runs, err := r.cfg.Journal.SettledEventRuns(ctx)
		if err != nil {
			r.cfg.Log.ErrorContext(ctx, "read pending settled events", "err", err)
		}
		for _, as := range runs {
			if err := r.flushEvents(ctx, as); err != nil && ctx.Err() == nil {
				r.cfg.Log.WarnContext(ctx, "settled event sync pending", "run_id", as.RunID, "err", err)
			}
		}
		if len(runs) > 0 && ctx.Err() == nil {
			if _, err := r.cfg.Journal.PruneSpool(ctx); err != nil {
				r.cfg.Log.WarnContext(ctx, "event spool cleanup pending", "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(r.cfg.RetryDelay):
		}
	}
}
