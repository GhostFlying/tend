package app

import (
	"context"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// WithdrawMessage cancels only an owner message that has not been claimed.
type WithdrawMessage struct{}

func (WithdrawMessage) Operation() string { return "conversation.withdraw_message" }
func (WithdrawMessage) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}
func (WithdrawMessage) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.WithdrawMessagePayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if x.Env.Target.Type != "message" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "target must be a message")
	}
	id := x.Env.Target.ID
	c, err := store.MessageControl(ctx, tx.Q(), id)
	if err != nil {
		return Outcome{}, errNotFound(err, "message does not have an owner queue entry")
	}
	result := func() (Outcome, error) {
		return Outcome{Result: protocol.WithdrawMessageResult{MessageID: id, Control: c}}, nil
	}
	if c.WithdrawnAt != 0 {
		return result()
	}
	if x.Env.ExpectedControlRevision == nil || *x.Env.ExpectedControlRevision != c.Revision {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "message queue changed; read it again")
	}
	if err := tx.WithdrawMessage(ctx, id, c.Revision, x.Now.UnixMilli()); err != nil {
		if errors.Is(err, store.ErrStale) {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "message was already claimed or its outcome is unknown")
		}
		return Outcome{}, err
	}
	if err := restoreWithdrawnRecovery(ctx, x, tx, id); err != nil {
		return Outcome{}, err
	}
	c, err = store.MessageControl(ctx, tx.Q(), id)
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Notify: true, Result: protocol.WithdrawMessageResult{MessageID: id, Control: c}, Events: []Event{{ObjectType: "message", ObjectID: id, ObjectVersion: c.Revision, Kind: "conversation.message_state", Body: map[string]any{"state": protocol.MessageInterrupted, "withdrawn": true}}}}, nil
}

func (a *App) MessageWithdrawn(ctx context.Context, id string) (bool, error) {
	var withdrawn bool
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		c, err := store.MessageControl(ctx, tx.Q(), id)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		withdrawn = c.WithdrawnAt != 0
		return err
	})
	return withdrawn, err
}

func (a *App) queueChanged(ctx context.Context, tx *store.Tx, requestID string, now int64) error {
	_, err := tx.InsertEvent(ctx, store.EventRow{EventID: a.ids.New(protocol.PrefixEvent), Source: "mainthread", ObjectType: "conversation", ObjectID: "main", Kind: "conversation.queue_changed", BodyJSON: `{}`, RequestID: requestID, CreatedAt: now})
	return err
}
