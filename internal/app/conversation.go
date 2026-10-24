package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// The main thread outbox destination and the operation that delivers a
// conversation message to it.
const (
	MainThreadDestination = "main_thread"
	OpHandleMessage       = "main_thread.handle_message"
)

// The main thread's control destination and the operation that stops the
// turn handling a message (D69). Control has a destination of its own so
// it is not queued behind the turn it stops.
const (
	MainThreadControlDestination = "main_thread_control"
	OpInterruptTurn              = "main_thread.interrupt_turn"
)

// InterruptTurn is the payload of a delivery to the control destination.
type InterruptTurn struct {
	MessageID string `json:"message_id"`
}

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
	if (p.Body == "" && len(p.Images) == 0 && len(p.Attachments) == 0) || len(p.Body) > maxMessageBytes || !utf8.ValidString(p.Body) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "body must be non-empty UTF-8 of at most %d bytes", maxMessageBytes)
	}
	images, err := validateConversationImages(p.Images)
	if err != nil {
		return Outcome{}, err
	}
	seenAttachments := make(map[string]bool, len(p.Attachments))
	for _, fileID := range p.Attachments {
		if seenAttachments[fileID] {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "duplicate attachment reference")
		}
		seenAttachments[fileID] = true
		if _, err := store.GetConversationAttachment(ctx, tx.Q(), fileID); err != nil {
			return Outcome{}, errNotFound(err, "attachment does not exist")
		}
	}
	if p.ReplyTo != "" {
		if _, err := store.GetMessage(ctx, tx.Q(), p.ReplyTo); err != nil {
			return Outcome{}, errNotFound(err, "message %s does not exist", p.ReplyTo)
		}
	}
	var choice []byte
	if p.Decision != nil {
		if err := checkChoice(ctx, tx.Q(), *p.Decision); err != nil {
			return Outcome{}, err
		}
		var err error
		if choice, err = json.Marshal(p.Decision); err != nil {
			return Outcome{}, fmt.Errorf("app: encode decision choice: %w", err)
		}
	}
	id := x.IDs.New(protocol.PrefixMessage)
	seq, err := tx.InsertMessage(ctx, store.MessageRow{
		MessageID: id, Author: "owner", ClientID: x.Principal.Client, Body: p.Body, ReplyTo: p.ReplyTo,
		LastSeenSeq: p.LastSeenSeq, RequestID: x.Env.RequestID, CreatedAt: x.Now.UnixMilli(), DecisionJSON: string(choice),
	})
	if err != nil {
		return Outcome{}, err
	}
	for position, image := range images {
		image.ImageID = x.IDs.New(protocol.PrefixFile)
		if err := tx.InsertConversationImage(ctx, id, position, image); err != nil {
			return Outcome{}, err
		}
	}
	for position, fileID := range p.Attachments {
		if err := tx.BindConversationAttachment(ctx, id, len(images)+position, fileID); err != nil {
			return Outcome{}, err
		}
	}
	if p.Decision != nil {
		if err := tx.BindMessage(ctx, id, "decision", p.Decision.DecisionID, "answers"); err != nil {
			return Outcome{}, err
		}
	}
	return Outcome{
		Result:   map[string]any{"message_id": id, "seq": seq},
		Events:   []Event{{ObjectType: "message", ObjectID: id, ObjectVersion: seq, Kind: "conversation.message", Body: map[string]any{"author": "owner", "seq": seq}}},
		Outbox:   []OutboxMessage{{Destination: MainThreadDestination, Operation: OpHandleMessage, Payload: HandleMessage{MessageID: id}}},
		Accepted: true,
	}, nil
}

// checkChoice accepts a message's choice of a decision option only for an
// open decision as the owner saw it, and only for an option tend does not
// carry out itself: those are answered with decisions.respond (D71).
func checkChoice(ctx context.Context, q store.Querier, c protocol.DecisionChoice) error {
	d, err := store.GetDecision(ctx, q, c.DecisionID)
	if err != nil {
		return errNotFound(err, "decision %s does not exist", c.DecisionID)
	}
	if d.Status != store.DecisionOpen || d.Version != c.Version {
		pe := protocol.Errorf(protocol.CodeStaleRevision, "decision %s changed since version %d", c.DecisionID, c.Version)
		if b, err := json.Marshal(map[string]any{"version": d.Version, "status": d.Status}); err == nil {
			pe.Details = b
		}
		return pe
	}
	var options []string
	if err := json.Unmarshal([]byte(d.OptionsJSON), &options); err != nil {
		return fmt.Errorf("app: decode options of %s: %w", d.DecisionID, err)
	}
	if !slices.Contains(options, c.Option) {
		return protocol.Errorf(protocol.CodeInvalidRequest, "%q is not an option of decision %s", c.Option, c.DecisionID)
	}
	var effects map[string]protocol.DecisionEffect
	if err := json.Unmarshal([]byte(d.EffectsJSON), &effects); err != nil {
		return fmt.Errorf("app: decode effects of %s: %w", d.DecisionID, err)
	}
	if _, ok := effects[c.Option]; ok {
		return protocol.Errorf(protocol.CodeInvalidRequest, "tend carries out %q itself; answer it with decisions.respond", c.Option)
	}
	return nil
}

// InterruptReply handles conversation.interrupt_reply: the owner stops the
// main agent's reply to one of their messages (D69). When the message's
// turn is starting or running, stopping it is queued for the main thread;
// otherwise nothing changes. Either way the result is the message's state.
type InterruptReply struct{}

// Operation implements Command.
func (InterruptReply) Operation() string { return "conversation.interrupt_reply" }

// Authorize implements Command.
func (InterruptReply) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}

// Apply implements Command.
func (InterruptReply) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.InterruptReplyPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	msg, err := store.GetMessage(ctx, tx.Q(), p.MessageID)
	if err != nil {
		return Outcome{}, errNotFound(err, "message %s does not exist", p.MessageID)
	}
	if msg.Author != "owner" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "message %s is not the owner's", p.MessageID)
	}
	item, err := store.GetConversationItem(ctx, tx.Q(), p.MessageID)
	if err != nil {
		return Outcome{}, err
	}
	state := protocol.MessageState(item.State)
	if state == "" {
		state = protocol.MessageQueued
	}
	result := map[string]any{"message_id": p.MessageID, "state": state}
	if _, err := store.GetOpenTurn(ctx, tx.Q(), p.MessageID); errors.Is(err, store.ErrNotFound) {
		return Outcome{Result: result}, nil
	} else if err != nil {
		return Outcome{}, err
	}
	return Outcome{
		Result:   result,
		Outbox:   []OutboxMessage{{Destination: MainThreadControlDestination, Operation: OpInterruptTurn, Payload: InterruptTurn{MessageID: p.MessageID}}},
		Accepted: true,
	}, nil
}

// RecordAgentMessage handles conversation.record_agent_message: the main
// agent said something in a turn. Its request ID is derived from the
// harness item, so recording the same item again has no effect.
type RecordAgentMessage struct{}

type recordAgentMessagePayload struct {
	Body    string                  `json:"body"`
	ReplyTo string                  `json:"reply_to"`
	TurnID  string                  `json:"turn_id"`
	ItemID  string                  `json:"item_id"`
	Status  protocol.AssistantState `json:"status,omitempty"`
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
	status := p.Status
	if status == "" {
		status = protocol.AssistantCompleted
	}
	switch status {
	case protocol.AssistantStreaming, protocol.AssistantCompleted, protocol.AssistantInterrupted, protocol.AssistantFailed, protocol.AssistantUnknown:
	default:
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "unknown assistant status")
	}
	if !utf8.ValidString(p.Body) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "body must be UTF-8")
	}
	var id string
	var seq int64
	var row store.AgentMessageRow
	identified := p.TurnID != "" && p.ItemID != "" && x.Principal.Thread != ""
	if identified {
		var err error
		row, err = store.GetAgentMessage(ctx, tx.Q(), x.Principal.Thread, p.TurnID, p.ItemID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return Outcome{}, err
		}
		if err == nil {
			old, err := store.GetConversationItem(ctx, tx.Q(), row.MessageID)
			if err != nil {
				return Outcome{}, err
			}
			id, seq = old.MessageID, old.Seq
			result := Outcome{Result: map[string]any{"message_id": id, "seq": seq}}
			// Terminal text cannot be revived by a delayed delta. A streaming
			// update is cumulative and must extend the committed prefix; the
			// authoritative completed item may replace it with shorter text.
			if row.Status != string(protocol.AssistantStreaming) || (status == protocol.AssistantStreaming && !strings.HasPrefix(p.Body, old.Body)) || (old.Body == p.Body && row.Status == string(status)) {
				return result, nil
			}
		}
	}
	if id == "" {
		if p.Body == "" {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "body is required")
		}
		id = x.IDs.New(protocol.PrefixMessage)
		var err error
		seq, err = tx.InsertMessage(ctx, store.MessageRow{MessageID: id, Author: "main_agent", Body: p.Body, ReplyTo: p.ReplyTo, RequestID: x.Env.RequestID, CreatedAt: x.Now.UnixMilli()})
		if err != nil {
			return Outcome{}, err
		}
		row = store.AgentMessageRow{MessageID: id, ThreadID: x.Principal.Thread, TurnID: p.TurnID, ItemID: p.ItemID}
	}
	if identified {
		row.Status, row.Revision = string(status), row.Revision+1
		if err := tx.PutAgentMessage(ctx, row, p.Body); err != nil {
			return Outcome{}, err
		}
	}
	return Outcome{
		Result: map[string]any{"message_id": id, "seq": seq},
		Events: []Event{{ObjectType: "message", ObjectID: id, ObjectVersion: seq, Kind: "conversation.message", Body: map[string]any{"author": "main_agent", "seq": seq, "reply_to": p.ReplyTo, "turn_id": p.TurnID}}},
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

// insertToolUseEvent records that a tool use was settled outside a
// request, as a request's record would.
func (a *App) insertToolUseEvent(ctx context.Context, tx *store.Tx, messageID string, status protocol.ToolStatus, now int64) error {
	seq, err := store.GetConversationSeq(ctx, tx.Q(), messageID)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{"author": "main_agent", "seq": seq, "status": status})
	if err != nil {
		return fmt.Errorf("app: encode tool use event: %w", err)
	}
	_, err = tx.InsertEvent(ctx, store.EventRow{
		EventID: a.ids.New(protocol.PrefixEvent), Source: "mainthread", ObjectType: "message", ObjectID: messageID,
		ObjectVersion: seq, Kind: "conversation.tool_use", BodyJSON: string(body), CreatedAt: now,
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
		if m.Status == store.TurnStarting && (errors.Is(err, store.ErrNotFound) || prev.Status == store.TurnRejected) {
			paused, err := upgradePausesMain(ctx, tx.Q())
			if err != nil {
				return err
			}
			if paused {
				return protocol.Errorf(protocol.CodeInvalidTransition, "main thread is draining for server upgrade")
			}
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

// SettleOpenTurns marks a thread's running turns, and the tool uses still
// running in its turns, as interrupted after its harness went away.
func (a *App) SettleOpenTurns(ctx context.Context, threadID string) error {
	now := a.now().UnixMilli()
	settled := 0
	err := a.store.Write(ctx, func(tx *store.Tx) error {
		ids, err := tx.SettleOpenTurns(ctx, threadID, now)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err := a.insertMessageState(ctx, tx, id, protocol.MessageInterrupted, now); err != nil {
				return err
			}
		}
		uses, err := tx.SettleToolUses(ctx, threadID, now)
		if err != nil {
			return err
		}
		for _, id := range uses {
			if err := a.insertToolUseEvent(ctx, tx, id, protocol.ToolInterrupted, now); err != nil {
				return err
			}
		}
		messages, err := tx.SettleAgentMessages(ctx, threadID)
		if err != nil {
			return err
		}
		for _, id := range messages {
			seq, err := store.GetConversationSeq(ctx, tx.Q(), id)
			if err != nil {
				return err
			}
			if _, err := tx.InsertEvent(ctx, store.EventRow{EventID: a.ids.New(protocol.PrefixEvent), Source: "mainthread", ObjectType: "message", ObjectID: id, ObjectVersion: seq, Kind: "conversation.message", BodyJSON: `{}`, CreatedAt: now}); err != nil {
				return err
			}
		}
		settled = len(ids) + len(uses) + len(messages)
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

// maxToolText bounds a tool use's input and output in the conversation;
// the rest is cut off with a note.
const maxToolText = 8 << 10

// RecordToolUse handles conversation.record_tool_use: the main agent
// started or finished using a tool in a turn (D69). The first record of
// an item adds it to the conversation; later ones update it in place.
// Its request ID is derived from the item and the phase, so a repeated
// event records it once.
type RecordToolUse struct{}

type recordToolUsePayload struct {
	TurnID  string              `json:"turn_id"`
	ItemID  string              `json:"item_id"`
	ReplyTo string              `json:"reply_to"`
	Tool    string              `json:"tool"`
	Input   string              `json:"input"`
	Output  string              `json:"output"`
	Status  protocol.ToolStatus `json:"status"`
}

// Operation implements Command.
func (RecordToolUse) Operation() string { return "conversation.record_tool_use" }

// Authorize implements Command.
func (RecordToolUse) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalMainAgent, protocol.PrincipalSystem)
}

// Apply implements Command.
func (RecordToolUse) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p recordToolUsePayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	switch {
	case p.TurnID == "" || p.ItemID == "" || p.Tool == "":
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "turn_id, item_id and tool are required")
	case p.Status != protocol.ToolRunning && p.Status != protocol.ToolCompleted && p.Status != protocol.ToolFailed:
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "unknown tool status %q", p.Status)
	}
	now := x.Now.UnixMilli()
	u, err := store.GetToolUse(ctx, tx.Q(), p.TurnID, p.ItemID)
	var seq int64
	switch {
	case errors.Is(err, store.ErrNotFound):
		u = store.ToolUseRow{MessageID: x.IDs.New(protocol.PrefixMessage), ThreadID: x.Principal.Thread, TurnID: p.TurnID, ItemID: p.ItemID}
		if seq, err = tx.InsertMessage(ctx, store.MessageRow{Kind: store.KindToolUse, MessageID: u.MessageID, Author: "main_agent",
			ReplyTo: p.ReplyTo, RequestID: x.Env.RequestID, CreatedAt: now}); err != nil {
			return Outcome{}, err
		}
	case err != nil:
		return Outcome{}, err
	case u.Status != string(protocol.ToolRunning) && (u.Status != string(protocol.ToolInterrupted) || p.Status == protocol.ToolRunning):
		// A late start does not take back a finished use; a result that
		// arrives after the use was settled as interrupted still counts.
		return Outcome{Result: map[string]any{"message_id": u.MessageID, "status": u.Status}}, nil
	default:
		if seq, err = store.GetConversationSeq(ctx, tx.Q(), u.MessageID); err != nil {
			return Outcome{}, err
		}
	}
	u.Tool, u.Input, u.Output, u.Status, u.UpdatedAt = p.Tool, clipText(p.Input), clipText(p.Output), string(p.Status), now
	if err := tx.PutToolUse(ctx, u); err != nil {
		return Outcome{}, err
	}
	return Outcome{
		Result: map[string]any{"message_id": u.MessageID, "seq": seq, "status": u.Status},
		Events: []Event{{ObjectType: "message", ObjectID: u.MessageID, ObjectVersion: seq, Kind: "conversation.tool_use",
			Body: map[string]any{"author": "main_agent", "seq": seq, "status": u.Status}}},
	}, nil
}

// clipText cuts s to maxToolText bytes on a character boundary and says
// how much was left out.
func clipText(s string) string {
	if len(s) <= maxToolText {
		return s
	}
	cut := maxToolText
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + fmt.Sprintf("\n[truncated: %d of %d bytes shown]", cut, len(s))
}

// TurnOpen reports whether the turn handling a message is starting or
// running.
func (a *App) TurnOpen(ctx context.Context, messageID string) (bool, error) {
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		_, err := store.GetOpenTurn(ctx, tx.Q(), messageID)
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}
