package store

import (
	"context"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
)

// AgentRecovery retains the chain independently of each native execution.
type AgentRecovery struct {
	protocol.AgentRecoveryView
	RootRequestID, ConversationID, MessageID, ThreadID string
	ActiveRequestID, LastTurnID                        string
}

// RecoveryForRequest also resolves a continuation's stable root.
func RecoveryForRequest(ctx context.Context, q Querier, request string) (AgentRecovery, error) {
	var r AgentRecovery
	err := q.QueryRowContext(ctx, `SELECT root_request_id,conversation_id,message_id,thread_id,status,attempts,next_attempt_at,deadline,active_request_id,last_turn_id,reason FROM agent_recoveries WHERE root_request_id=? OR root_request_id=(SELECT root_request_id FROM agent_recovery_attempts WHERE request_id=?)`, request, request).
		Scan(&r.RootRequestID, &r.ConversationID, &r.MessageID, &r.ThreadID, &r.Status, &r.Attempts, &r.NextAttemptAt, &r.Deadline, &r.ActiveRequestID, &r.LastTurnID, &r.Reason)
	if err != nil {
		return r, notFound(err, "agent recovery")
	}
	return r, nil
}

// MessageRecovery is shared by owner transcript and queue projections.
func MessageRecovery(ctx context.Context, q Querier, message string) (*protocol.AgentRecoveryView, error) {
	var r protocol.AgentRecoveryView
	err := q.QueryRowContext(ctx, `SELECT status,attempts,next_attempt_at,deadline,reason FROM agent_recoveries WHERE message_id=? ORDER BY updated_at DESC LIMIT 1`, message).
		Scan(&r.Status, &r.Attempts, &r.NextAttemptAt, &r.Deadline, &r.Reason)
	if err != nil {
		return nil, notFound(err, fmt.Sprintf("recovery of %s", message))
	}
	return &r, nil
}
