package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// The main thread outbox destination and the operation that delivers a
// conversation message to it, and the destination for controlling the
// front thread while a turn runs, with its operation. Control has its own
// destination so it does not queue behind the turn it acts on (v0.1 §6.2).
const (
	MainThreadDestination  = "main_thread"
	OpHandleMessage        = "main_thread.handle_message"
	MainControlDestination = "main_control"
	OpInterruptTurn        = "main_control.interrupt_turn"
)

// HandleMessage is the payload of a delivery to the main thread.
type HandleMessage struct {
	MessageID string `json:"message_id"`
}

const maxMessageBytes = 64 << 10

// SubmitMessage handles conversation.submit_message: the owner says
// something, from any client. The message joins the conversation in the
// order tend received it and is queued for the main thread, which handles
// one message per turn.
type SubmitMessage struct{}

// Operation implements Command.
func (SubmitMessage) Operation() string { return "conversation.submit_message" }

// Authorize implements Command.
func (SubmitMessage) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}

// Apply implements Command.
func (SubmitMessage) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.SubmitMessagePayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if p.Body == "" || len(p.Body) > maxMessageBytes || !utf8.ValidString(p.Body) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "body must be non-empty UTF-8 of at most %d bytes", maxMessageBytes)
	}
	if p.ReplyTo != "" {
		if _, err := store.GetMessage(ctx, tx.Q(), p.ReplyTo); err != nil {
			return Outcome{}, errNotFound(err, "message %s does not exist", p.ReplyTo)
		}
	}
	id := x.IDs.New(protocol.PrefixMessage)
	seq, err := tx.InsertMessage(ctx, store.MessageRow{
		MessageID: id, Author: "owner", ClientID: x.Principal.Client, Body: p.Body, ReplyTo: p.ReplyTo,
		LastSeenSeq: p.LastSeenSeq, RequestID: x.Env.RequestID, CreatedAt: x.Now.UnixMilli(),
	})
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{
		Result:   map[string]any{"message_id": id, "seq": seq},
		Events:   []Event{{ObjectType: "message", ObjectID: id, ObjectVersion: seq, Kind: "conversation.message", Body: map[string]any{"author": "owner", "seq": seq}}},
		Outbox:   []OutboxMessage{{Destination: MainThreadDestination, Operation: OpHandleMessage, Payload: HandleMessage{MessageID: id}}},
		Accepted: true,
	}, nil
}

// InterruptTurn is the payload of a delivery that stops the turn handling
// a message.
type InterruptTurn struct {
	MessageID string `json:"message_id"`
}

// InterruptReply handles control.interrupt_reply: the owner stops the main
// agent's reply to a message. If a turn is handling the message, the front
// thread is told to interrupt it after commit and the operation is
// accepted; the message becomes interrupted when the turn ends. Otherwise
// there is nothing to stop and the operation succeeds without effect.
type InterruptReply struct{}

// Operation implements Command.
func (InterruptReply) Operation() string { return protocol.OpInterruptReply }

// Authorize implements Command.
func (InterruptReply) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}

// Apply implements Command.
func (InterruptReply) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	id := x.Env.Target.ID
	if x.Env.Target.Type != "message" || id == "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "the target must be a message")
	}
	if _, err := store.GetMessage(ctx, tx.Q(), id); err != nil {
		return Outcome{}, errNotFound(err, "message %s does not exist", id)
	}
	turn, err := store.GetMainTurnByMessage(ctx, tx.Q(), id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return Outcome{}, err
	}
	running := err == nil && (turn.Status == store.TurnStarting || turn.Status == store.TurnStarted)
	out := Outcome{Result: protocol.InterruptReplyResult{MessageID: id, Running: running}}
	if running {
		out.Outbox = []OutboxMessage{{Destination: MainControlDestination, Operation: OpInterruptTurn, Payload: InterruptTurn{MessageID: id}}}
		out.Accepted = true
	}
	return out, nil
}

// RecordAgentMessage handles conversation.record_agent_message: the main
// agent said something in a turn. Its request ID is derived from the
// harness item, so recording the same item again has no effect.
type RecordAgentMessage struct{}

type recordAgentMessagePayload struct {
	Body    string `json:"body"`
	ReplyTo string `json:"reply_to"`
	TurnID  string `json:"turn_id"`
	ItemID  string `json:"item_id"`
}

// Operation implements Command.
func (RecordAgentMessage) Operation() string { return "conversation.record_agent_message" }

// Authorize implements Command.
func (RecordAgentMessage) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalMainAgent, protocol.PrincipalSystem)
}

// Apply implements Command.
func (RecordAgentMessage) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p recordAgentMessagePayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if p.Body == "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "body is required")
	}
	id := x.IDs.New(protocol.PrefixMessage)
	seq, err := tx.InsertMessage(ctx, store.MessageRow{
		MessageID: id, Author: "main_agent", Body: p.Body, ReplyTo: p.ReplyTo, RequestID: x.Env.RequestID, CreatedAt: x.Now.UnixMilli(),
	})
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{
		Result: map[string]any{"message_id": id, "seq": seq},
		Events: []Event{{ObjectType: "message", ObjectID: id, ObjectVersion: seq, Kind: "conversation.message",
			Body: map[string]any{"author": "main_agent", "seq": seq, "reply_to": p.ReplyTo, "turn_id": p.TurnID}}},
	}, nil
}

// bindSource ties the conversation message a planning write came from to
// the object it touched, so later context recalls what the discussion was
// about. Writes that name no message, or a message tend does not have,
// bind nothing.
func bindSource(ctx context.Context, tx *store.Tx, messageID, objectType, objectID string) error {
	if messageID == "" || objectID == "" {
		return nil
	}
	if _, err := store.GetMessage(ctx, tx.Q(), messageID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	return tx.BindMessage(ctx, messageID, objectType, objectID, "resolved")
}

// The main thread's bookkeeping. Like the dispatcher's, these are system
// maintenance writes keyed by their own identities.

// MainThread returns the front thread of a harness instance.
func (a *App) MainThread(ctx context.Context, instance string) (string, bool, error) {
	var id string
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		id, err = store.GetMainThread(ctx, tx.Q(), instance)
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		return "", false, nil
	}
	return id, err == nil, err
}

// SetMainThread records the front thread of a harness instance.
func (a *App) SetMainThread(ctx context.Context, instance, threadID string) error {
	return a.store.Write(ctx, func(tx *store.Tx) error { return tx.PutMainThread(ctx, instance, threadID, a.now().UnixMilli()) })
}

// MainTurn returns the turn of a delivery to the main thread.
func (a *App) MainTurn(ctx context.Context, requestID string) (store.MainTurn, bool, error) {
	var m store.MainTurn
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		m, err = store.GetMainTurn(ctx, tx.Q(), requestID)
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		return m, false, nil
	}
	return m, err == nil, err
}

// turnStates are the message states the owner sees for main turn states. A
// turn that is starting or was rejected leaves its message queued.
var turnStates = map[string]protocol.MessageState{
	store.TurnStarted:     protocol.MessageProcessing,
	store.TurnCompleted:   protocol.MessageReplied,
	store.TurnInterrupted: protocol.MessageInterrupted,
	store.TurnFailed:      protocol.MessageFailed,
}

// insertMessageState records that an owner message reached a new state.
func (a *App) insertMessageState(ctx context.Context, tx *store.Tx, messageID string, state protocol.MessageState, now int64) error {
	body, err := json.Marshal(map[string]any{"state": state})
	if err != nil {
		return fmt.Errorf("app: encode message state: %w", err)
	}
	_, err = tx.InsertEvent(ctx, store.EventRow{
		EventID: a.ids.New(protocol.PrefixEvent), Source: "mainthread", ObjectType: "message", ObjectID: messageID,
		Kind: "conversation.message_state", BodyJSON: string(body), CreatedAt: now,
	})
	return err
}

// PutMainTurn records the progress of a delivery's turn, and the state its
// message reached when that changes.
func (a *App) PutMainTurn(ctx context.Context, m store.MainTurn) error {
	now := a.now().UnixMilli()
	err := a.store.Write(ctx, func(tx *store.Tx) error {
		prev, err := store.GetMainTurn(ctx, tx.Q(), m.RequestID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if err := tx.PutMainTurn(ctx, m, now); err != nil {
			return err
		}
		if state, ok := turnStates[m.Status]; ok && prev.Status != m.Status {
			return a.insertMessageState(ctx, tx, m.MessageID, state, now)
		}
		return nil
	})
	if err == nil {
		a.notifyChange()
	}
	return err
}

// TurnDelivery returns the delivery a harness turn handled.
func (a *App) TurnDelivery(ctx context.Context, threadID, turnID string) (store.MainTurn, bool, error) {
	var m store.MainTurn
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		m, err = store.GetMainTurnByTurn(ctx, tx.Q(), threadID, turnID)
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		return m, false, nil
	}
	return m, err == nil, err
}

// SettleOpenTurns marks a thread's running turns as interrupted after its
// harness went away.
func (a *App) SettleOpenTurns(ctx context.Context, threadID string) error {
	now := a.now().UnixMilli()
	settled := 0
	err := a.store.Write(ctx, func(tx *store.Tx) error {
		ids, err := tx.SettleOpenTurns(ctx, threadID, now)
		if err != nil {
			return err
		}
		settled = len(ids)
		for _, id := range ids {
			if err := a.insertMessageState(ctx, tx, id, protocol.MessageInterrupted, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil && settled > 0 {
		a.notifyChange()
	}
	return err
}

// Message returns a conversation message.
func (a *App) Message(ctx context.Context, messageID string) (store.MessageView, error) {
	var m store.MessageView
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		m, err = store.GetMessage(ctx, tx.Q(), messageID)
		return err
	})
	return m, err
}
