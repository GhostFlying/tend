package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// Startup attempts are local attachment failures, not native or outbox deliveries.
// Their append-only state uses the original input's identity across restarts.
type agentStartupState struct {
	Attempts      int64  `json:"attempts"`
	FirstFailedAt int64  `json:"first_failed_at"`
	NextAttemptAt int64  `json:"next_attempt_at"`
	AttentionID   string `json:"attention_id,omitempty"`
}

func agentStartupHead(ctx context.Context, q store.Querier, conversation string) (store.OutboxMessage, error) {
	return store.HeadOutboxPrioritized(ctx, q, store.ConversationDestination(store.ConversationID(conversation)), OpHandleEvent)
}

func readAgentStartupState(ctx context.Context, q store.Querier, request string) (agentStartupState, error) {
	var state agentStartupState
	var raw string
	err := q.QueryRowContext(ctx, `SELECT body_json FROM events WHERE object_type='agent_startup' AND object_id=? AND kind='conversation.startup_state' ORDER BY event_seq DESC LIMIT 1`, request).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil
	}
	if err == nil {
		err = json.Unmarshal([]byte(raw), &state)
	}
	return state, err
}

func agentStartupAllowed(ctx context.Context, q store.Querier, conversation string, now int64) (bool, error) {
	head, err := agentStartupHead(ctx, q, conversation)
	if errors.Is(err, store.ErrNotFound) {
		return true, nil
	}
	if err != nil || head.Status != store.OutboxPending {
		return err == nil, err
	}
	state, err := readAgentStartupState(ctx, q, head.RequestID)
	if err != nil {
		return false, err
	}
	if state.AttentionID != "" {
		attention, err := store.GetAttention(ctx, q, state.AttentionID)
		return err == nil && attention.Status != "open", err
	}
	return state.NextAttemptAt <= now, nil
}

// AgentStartupAllowed gates only pending input's automatic attachment retries.
// Sending input retains its existing native receipt reconciliation path.
func (a *App) AgentStartupAllowed(ctx context.Context, conversation string) (bool, error) {
	var allowed bool
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		allowed, err = agentStartupAllowed(ctx, tx.Q(), conversation, a.now().UnixMilli())
		return err
	})
	return allowed, err
}

func (a *App) writeAgentStartupState(ctx context.Context, tx *store.Tx, request string, state agentStartupState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = tx.InsertEvent(ctx, store.EventRow{EventID: a.ids.New(protocol.PrefixEvent), Source: "app", ObjectType: "agent_startup", ObjectID: request, Kind: "conversation.startup_state", BodyJSON: string(raw), RequestID: request, CreatedAt: a.now().UnixMilli()})
	return err
}

// AgentStartupSucceeded ends this local failure window without fabricating a turn.
func (a *App) AgentStartupSucceeded(ctx context.Context, conversation string) error {
	return a.store.Write(ctx, func(tx *store.Tx) error {
		head, err := agentStartupHead(ctx, tx.Q(), conversation)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		state, err := readAgentStartupState(ctx, tx.Q(), head.RequestID)
		if err != nil || state.Attempts == 0 {
			return err
		}
		return a.writeAgentStartupState(ctx, tx, head.RequestID, agentStartupState{})
	})
}

func (a *App) deferAgentStartup(ctx context.Context, conversation string) error {
	noticeQueued := false
	err := a.store.Write(ctx, func(tx *store.Tx) error {
		head, err := agentStartupHead(ctx, tx.Q(), conversation)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil || head.Status != store.OutboxPending {
			return err
		}
		state, err := readAgentStartupState(ctx, tx.Q(), head.RequestID)
		if err != nil {
			return err
		}
		now := a.now()
		if state.AttentionID != "" {
			attention, err := store.GetAttention(ctx, tx.Q(), state.AttentionID)
			if err != nil || attention.Status == "open" {
				return err
			}
			state = agentStartupState{}
		}
		if state.NextAttemptAt > now.UnixMilli() {
			return nil
		}
		if state.Attempts == 0 {
			state.FirstFailedAt = now.UnixMilli()
		}
		state.Attempts++
		state.NextAttemptAt = now.Add(30 * time.Second).UnixMilli()
		policy := a.agentRecoveryPolicy
		if state.Attempts >= policy.MaxAttempts || now.UnixMilli()-state.FirstFailedAt >= policy.MaxElapsed.Milliseconds() {
			state.AttentionID, err = a.createAgentStartupAttention(ctx, tx, conversation, head)
			if err != nil {
				return err
			}
			noticeQueued = true
		}
		if err := a.writeAgentStartupState(ctx, tx, head.RequestID, state); err != nil {
			return err
		}
		return tx.TransitionOutbox(ctx, store.OutboxTransition{OutboxID: head.OutboxID, From: head.Status, FromAttempts: head.Attempts, To: head.Status, Attempts: head.Attempts, NextAttemptAt: max(head.NextAttemptAt, state.NextAttemptAt), Now: now.UnixMilli()})
	})
	if err == nil {
		a.notifyChange()
		if noticeQueued && a.notifier != nil {
			a.notifier.Notify()
		}
	}
	return err
}

func (a *App) createAgentStartupAttention(ctx context.Context, tx *store.Tx, conversation string, head store.OutboxMessage) (string, error) {
	var input HandleMessage
	if err := json.Unmarshal([]byte(head.PayloadJSON), &input); err != nil {
		return "", fmt.Errorf("app: decode startup input: %w", err)
	}
	x := &Exec{IDs: a.ids, Now: a.now(), Env: &protocol.Envelope{RequestID: head.RequestID}, Principal: protocol.Principal{Kind: protocol.PrincipalSystem}}
	attention := protocol.Attention{AttentionID: a.ids.New(protocol.Prefix("attn")), Kind: "action", Status: "open", Title: "Agent 启动持续失败，原输入尚未接收；请核对执行配置与启动故障，保留原输入及未知执行事实。", SourceMessageID: input.MessageID, RaisedBy: x.Principal, CreatedAt: x.Now.UnixMilli()}
	if store.ConversationID(conversation) != "main" {
		c, err := store.AgentConversation(ctx, tx.Q(), conversation)
		if err != nil {
			return "", err
		}
		issue, err := store.GetCanonicalIssue(ctx, tx.Q(), c.WorkItemID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return "", err
		}
		if err == nil {
			attention.IssueID, attention.BlocksClose = issue.IssueID, true
			r := &protocol.AttentionResponsibility{OriginIssueID: issue.IssueID, OriginAssignmentEpoch: issue.AssignmentEpoch}
			if err := responsibilityRecipient(ctx, tx.Q(), r, issue.IssueID); err != nil {
				return "", err
			}
			r.History = append(r.History, responsibilityHop(r, "responsible Agent startup exhausted its technical recovery budget before receiving the original input", head.RequestID))
			if err := responsibilityRecipient(ctx, tx.Q(), r, issue.ParentIssueID); err != nil {
				return "", err
			}
			attention.Responsibility = r
		}
	}
	if err := saveAttention(ctx, x, tx, &attention, 0); err != nil {
		return "", err
	}
	event := attentionEvent(attention)
	body, err := json.Marshal(event.Body)
	if err != nil {
		return "", err
	}
	eventID := a.ids.New(protocol.PrefixEvent)
	if _, err := tx.InsertEvent(ctx, store.EventRow{EventID: eventID, Source: "app", ObjectType: event.ObjectType, ObjectID: event.ObjectID, ObjectVersion: event.ObjectVersion, Kind: event.Kind, BodyJSON: string(body), RequestID: head.RequestID, CreatedAt: x.Now.UnixMilli()}); err != nil {
		return "", err
	}
	if attention.Responsibility != nil {
		if err := tx.SaveAttentionResponsibility(ctx, attention.AttentionID, attention.Responsibility, 0, head.RequestID); err != nil {
			return "", err
		}
		if err := recordResponsibilityWake(ctx, x, tx, attention, eventID); err != nil {
			return "", err
		}
	} else {
		// The main runtime itself is unavailable. Publish to the owner without
		// adding another input that depends on opening that same runtime.
		message := a.ids.New(protocol.PrefixMessage)
		if _, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: message, Author: "system", Body: attention.Title + "\nSource message: " + input.MessageID, CreatedAt: x.Now.UnixMilli()}); err != nil {
			return "", err
		}
		if err := tx.BindMessage(ctx, message, "attention", attention.AttentionID, "notice"); err != nil {
			return "", err
		}
		if err := registerSystemCommunication(ctx, x, tx, message, "agent_startup:"+attention.AttentionID); err != nil {
			return "", err
		}
		if err := syncMessagingInteractions(ctx, x, tx, []Event{event}); err != nil {
			return "", err
		}
	}
	return attention.AttentionID, nil
}
