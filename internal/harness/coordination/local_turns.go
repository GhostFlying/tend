package coordination

import (
	"context"
	"errors"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

func (d *Driver) forwardNative(ctx context.Context, t harness.Thread, e harness.Event, local bool) error {
	event := Event{ThreadID: t.ID(), Native: &e}
	if !local || e.TurnID == "" {
		return d.emit(ctx, event)
	}
	var err error
	event.RootTurnID, err = d.LocalTurns.Root(ctx, t.ID(), e.TurnID)
	if err != nil {
		return err
	}
	var next, call string
	if e.Kind == harness.EventTurnCompleted && e.TurnStatus == "completed" {
		next, call, err = d.LocalTurns.Begin(ctx, t.ID(), e.TurnID)
		if err != nil {
			return err
		}
	}
	event.Continued = next != ""
	if err = d.emit(ctx, event); err != nil || next == "" {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	input := harness.TurnInput{Text: next, Model: d.Profile.Model, Effort: d.Profile.Effort,
		UserMessageID: protocol.NativeObjectID(protocol.PrefixMessage, t.ID(), e.TurnID+"/"+call)}
	root := event.RootTurnID
	if root == "" {
		root = e.TurnID
	}
	d.mu.Lock()
	if d.pendingLocal == nil {
		d.pendingLocal = map[string]string{}
	}
	d.pendingLocal[t.ID()] = root
	d.mu.Unlock()
	defer func() { d.mu.Lock(); delete(d.pendingLocal, t.ID()); d.mu.Unlock() }()
	if err := harness.PrepareUsageDispatch(ctx, t, d.BeforeUsageDispatch); err != nil {
		return err
	}
	id, err := t.StartTurn(ctx, input)
	if err != nil {
		return errors.Join(harness.ErrRetainedResumePending, err)
	}
	if id == "" {
		return errors.Join(harness.ErrRetainedResumePending, errors.New("local continuation returned no native turn identity"))
	}
	return d.LocalTurns.Started(ctx, t.ID(), e.TurnID, id)
}
