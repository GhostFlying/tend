package app

import (
	"context"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func (a *App) nativeTaskPredecessor(ctx context.Context, run string) (string, error) {
	var parent string
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		r, err := store.GetRun(ctx, tx.Q(), run)
		if err != nil {
			return err
		}
		as := protocol.Assignment{Protocol: r.Protocol, RunID: r.RunID, TaskID: r.TaskID, Role: r.Role}
		if err := store.AssignmentSessionContinuation(ctx, tx.Q(), &as); err != nil {
			return err
		}
		if as.SessionContinuation != nil {
			parent = as.SessionContinuation.RunID
			return nil
		}
		if err := store.AssignmentExternalWait(ctx, tx.Q(), &as); err != nil {
			return err
		}
		if as.ExternalWaitContinuation != nil && as.ExternalWaitContinuation.ThreadID != "" {
			parent = as.ExternalWaitContinuation.RunID
		}
		return nil
	})
	return parent, err
}
