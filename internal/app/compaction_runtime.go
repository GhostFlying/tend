package app

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func (a *App) compactionRuntimeSelection(ctx context.Context, q store.Querier, conversation, operation, thread string) (hostedconfig.Selection, bool, error) {
	unknown := func() (hostedconfig.Selection, bool, error) {
		return hostedconfig.Selection{}, false, protocol.Errorf(protocol.CodeUnknownOutcome, "original compact runtime identity is unavailable or ambiguous; reconcile first")
	}
	if thread == "" {
		return unknown()
	}
	pin, err := store.GetCompactionRuntime(ctx, q, operation)
	if err == nil {
		if pin.ThreadID != thread {
			return unknown()
		}
		if pin.Selection == nil {
			return hostedconfig.Selection{}, false, nil
		}
		return *pin.Selection, true, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return hostedconfig.Selection{}, false, err
	}
	// Old compactions have no pin. Only a snapshot admitted on the exact native
	// thread in this conversation proves its binding; current definitions do not.
	rows, err := q.QueryContext(ctx, `SELECT h.private_snapshot FROM hosted_executions h JOIN main_turns t ON t.request_id=h.execution_id WHERE h.conversation_id=? AND t.thread_id=? ORDER BY t.created_at DESC,t.request_id DESC`, store.ConversationID(conversation), thread)
	if err != nil {
		return hostedconfig.Selection{}, false, err
	}
	var original *hostedconfig.Selection
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return hostedconfig.Selection{}, false, err
		}
		var candidate hostedconfig.Selection
		if err := json.Unmarshal([]byte(raw), &candidate); err != nil {
			rows.Close()
			return hostedconfig.Selection{}, false, err
		}
		if original != nil && original.BindingIdentity() != candidate.BindingIdentity() {
			rows.Close()
			return unknown()
		}
		if original == nil {
			original = &candidate
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return hostedconfig.Selection{}, false, err
	}
	if original != nil {
		return *original, true, nil
	}
	var registered bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hosted_configuration_versions)`).Scan(&registered); err != nil {
		return hostedconfig.Selection{}, false, err
	}
	if a.hostedConfiguration != nil || registered {
		return unknown()
	}
	return hostedconfig.Selection{}, false, nil
}

// UncertainCompactThread retains the actual native thread even if its current
// binding record has moved. It never authorizes starting a replacement.
func (a *App) UncertainCompactThread(ctx context.Context, conversation string) (string, bool, error) {
	var thread string
	var found bool
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		// Preserve the same snapshot's native-turn priority. A compact must not
		// redirect a session selected to reconcile another original execution.
		_, _, attachment, err := a.hostedRuntimeStartupSelection(ctx, tx.Q(), conversation)
		if err != nil || attachment.compactThread == "" {
			return err
		}
		ids, err := store.FollowupIDs(ctx, tx.Q(), `SELECT DISTINCT thread_id FROM agent_compactions WHERE conversation_id=? AND status IN ('starting','running','unknown')`, store.ConversationID(conversation))
		if err != nil || len(ids) == 0 {
			return err
		}
		found = true
		if len(ids) != 1 || ids[0] == "" {
			return protocol.Errorf(protocol.CodeUnknownOutcome, "original compact thread is unavailable or ambiguous; reconcile first")
		}
		thread = ids[0]
		return nil
	})
	return thread, found, err
}
