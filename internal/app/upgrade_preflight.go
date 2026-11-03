package app

import (
	"context"
	"errors"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// CheckUpgradeStart authorizes the fixed candidate before a capability probe can
// execute it. Execute repeats these checks in the admitting transaction. Exact
// recorded requests return their original receipt without another probe.
func (a *App) CheckUpgradeStart(ctx context.Context, env *protocol.Envelope, c StartUpgrade) (*protocol.Operation, error) {
	if err := env.Validate(); err != nil {
		return nil, err
	}
	if env.Operation != c.Operation() {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "upgrade start envelope required")
	}
	hash, err := protocol.PayloadHash(env)
	if err != nil {
		return nil, err
	}
	var recorded *protocol.Operation
	err = a.store.Read(ctx, func(tx *store.ReadTx) error {
		record, err := store.GetRequest(ctx, tx.Q(), "system", env.RequestID)
		if err == nil {
			if record.PayloadHash != hash {
				return protocol.Errorf(protocol.CodeConflictPayload, "original upgrade request input changed")
			}
			value := operationOf(record)
			recorded = &value
			return nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if err := checkUpgradeExecution(ctx, tx.Q(), c.ExecutionRequestID); err != nil {
			return err
		}
		_, err = checkDeliveryUpgradeStart(ctx, &Exec{Env: env, Now: a.now()}, tx.Q(), c)
		return err
	})
	return recorded, err
}

func checkUpgradeExecution(ctx context.Context, q store.Querier, id string) error {
	if id == "" {
		return nil
	}
	execution, err := store.GetHostedExecution(ctx, q, id)
	if err != nil {
		return err
	}
	turn, err := store.GetMainTurn(ctx, q, id)
	if err != nil {
		return err
	}
	if execution.Selection.Purpose != agentconfig.Main || (turn.Status != store.TurnStarting && turn.Status != store.TurnStarted) {
		return protocol.Errorf(protocol.CodeForbidden, "upgrade start requires an active admitted main execution")
	}
	return nil
}

// HasRecordedUpgradeStart is only a replay-presence gate. The controller must
// still compare the full original business Envelope hash. The initiating turn
// is audit provenance; a later turn cannot replace the identity of a retry.
func (a *App) HasRecordedUpgradeStart(ctx context.Context, request string) (bool, error) {
	var found bool
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		r, err := store.GetRequest(ctx, tx.Q(), "system", request)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if r.Operation != "upgrades.start" {
			return nil
		}
		found = true
		return nil
	})
	return found, err
}
