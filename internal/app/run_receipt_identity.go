package app

import (
	"context"
	"encoding/json"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// committedRunReceipt authorizes only a retry of an actual physical fact. The
// original Run and lease remain the credential scope; request history is absent.
func committedRunReceipt(ctx context.Context, q store.Querier, run string, env *protocol.Envelope) bool {
	switch env.Operation {
	case protocol.OpRunsYieldQuestion:
		var p protocol.YieldQuestionPayload
		h, err := store.GetQuestionHandoff(ctx, q, run)
		return json.Unmarshal(env.Payload, &p) == nil && err == nil && h != nil && h.Stopped && h.DecisionID == p.DecisionID && h.ThreadID == p.ThreadID
	case protocol.OpRunsYieldPause:
		var p protocol.YieldPausePayload
		if json.Unmarshal(env.Payload, &p) != nil {
			return false
		}
		var expected protocol.YieldPausePayload
		var stopped bool
		err := q.QueryRowContext(ctx, `SELECT request_id,issue_id,issue_revision,control_revision,thread_id,stopped FROM run_pause_handoffs WHERE run_id=?`, run).Scan(&expected.RequestID, &expected.IssueID, &expected.IssueRevision, &expected.ControlRevision, &expected.ThreadID, &stopped)
		return err == nil && stopped && expected == p
	case protocol.OpRunsYieldExternalWait:
		var p protocol.YieldExternalWaitPayload
		h, err := store.GetExternalWait(ctx, q, run)
		return json.Unmarshal(env.Payload, &p) == nil && err == nil && h != nil && h.Stopped && h.WaitID == p.WaitID && h.ThreadID == p.ThreadID
	case protocol.OpRunsRecordSessionStop:
		var p protocol.SessionStopPayload
		var thread string
		err := q.QueryRowContext(ctx, `SELECT thread_id FROM run_session_stops WHERE run_id=?`, run).Scan(&thread)
		return json.Unmarshal(env.Payload, &p) == nil && err == nil && thread == p.ThreadID
	}
	return false
}
