package app

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// ObserveNativeStart persists the trusted runtime identity of an admitted
// execution before callbacks arriving ahead of the turn/start response run.
// Late observations never revive a terminal or uncertain execution.
func (a *App) ObserveNativeStart(ctx context.Context, request, thread, turn string) error {
	if request == "" || thread == "" || turn == "" {
		return nil
	}
	return a.handoffWrite(ctx, func(tx *store.Tx) error {
		rec, err := store.GetMainTurn(ctx, tx.Q(), request)
		if err != nil {
			return err
		}
		if rec.Status != store.TurnStarting || rec.ThreadID != thread || (rec.TurnID != "" && rec.TurnID != turn) {
			return nil
		}
		rec.TurnID, rec.Status = turn, store.TurnStarted
		if err := tx.PutMainTurn(ctx, rec, a.now().UnixMilli()); err != nil {
			return err
		}
		return a.insertMessageState(ctx, tx, rec.MessageID, protocol.MessageProcessing, a.now().UnixMilli())
	})
}
