package app

import (
	"context"
	"encoding/json"
	"reflect"

	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// ManagerReplyReplay is one immutable database read boundary, not permission to
// reopen its native process or admit another business execution.
type ManagerReplyReplay struct {
	RunID, LeaseID, ThreadID string
	Selection                hostedconfig.Selection
	Events                   []ManagerReplayEvent
}

func (a *App) LapsedManagerReplyReplay(ctx context.Context, conversation string) (*ManagerReplyReplay, error) {
	return a.managerReplyReplay(ctx, conversation, false)
}

// ClosingManagerReplyReplay also consumes internal repair prefixes from an
// active lease whose original native process is already being closed. It
// never opens that process or treats prefix consumption as a business result.
func (a *App) ClosingManagerReplyReplay(ctx context.Context, conversation string) (*ManagerReplyReplay, error) {
	return a.managerReplyReplay(ctx, conversation, true)
}

func (a *App) managerReplyReplay(ctx context.Context, conversation string, includeActive bool) (*ManagerReplyReplay, error) {
	var replay *ManagerReplyReplay
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		ids, err := store.FollowupIDs(ctx, tx.Q(), `SELECT DISTINCT r.run_id
 FROM issue_manager_tasks m JOIN runs r USING(task_id) JOIN leases l USING(run_id)
 JOIN hosted_executions h ON json_extract(h.private_snapshot,'$.runner_run_id')=r.run_id
 JOIN main_turns t ON t.request_id=h.execution_id JOIN native_turn_receipts_pending p ON p.request_id=t.request_id
 WHERE m.conversation_id=? AND h.conversation_id=m.conversation_id AND r.role='issue_manager'
 AND (l.status='lapsed' OR (? AND l.status='active'
   AND EXISTS(SELECT 1 FROM logical_turn_executions e WHERE e.request_id=t.request_id AND e.repair=1)
   AND 1=(SELECT COUNT(*) FROM hosted_executions original JOIN native_turn_receipts_pending pending ON pending.request_id=original.execution_id
     WHERE original.conversation_id=m.conversation_id AND json_extract(original.private_snapshot,'$.runner_run_id')=r.run_id)))
 AND r.observed_state IN ('starting','running','waiting','interrupted','unknown')
 AND EXISTS(SELECT 1 FROM manager_native_requests n WHERE n.run_id=r.run_id AND n.method='close' AND n.status IN ('pending','started','unknown'))
 AND EXISTS(SELECT 1 FROM manager_native_events e WHERE e.run_id=r.run_id AND e.applied=0)
 ORDER BY r.run_id`, conversation, includeActive)
		if err != nil || len(ids) == 0 {
			return err
		}
		if len(ids) != 1 {
			return protocol.Errorf(protocol.CodeUnknownOutcome, "multiple closing manager responsibilities require reconciliation")
		}
		replay, err = managerReplyReplay(ctx, tx.Q(), conversation, ids[0], includeActive)
		return err
	})
	return replay, err
}

func managerReplyReplay(ctx context.Context, q store.Querier, conversation, run string, includeActive bool) (*ManagerReplyReplay, error) {
	r, err := store.GetRun(ctx, q, run)
	if err != nil {
		return nil, err
	}
	l, err := store.GetLeaseByRun(ctx, q, run)
	if err != nil {
		return nil, err
	}
	m, err := store.GetIssueManagerTask(ctx, q, r.TaskID)
	if err != nil {
		return nil, err
	}
	pin, err := store.GetRunExecution(ctx, q, run)
	if err != nil {
		return nil, err
	}
	active := l.Status == store.LeaseActive && includeActive
	if (!active && l.Status != store.LeaseLapsed) || l.HostID != r.HostID || m.HostID != r.HostID || m.ConversationID != conversation || m.Pin == nil || !reflect.DeepEqual(m.Pin, pin) || pin.Validate(r.HostID) != nil {
		return nil, protocol.Errorf(protocol.CodeUnknownOutcome, "closing manager original execution identity differs")
	}
	if active {
		closing, err := managerRunClosing(ctx, q, run)
		if err != nil {
			return nil, err
		}
		if !closing {
			return nil, protocol.Errorf(protocol.CodeUnknownOutcome, "active repair prefix requires its original closing responsibility")
		}
	}
	requests, err := store.FollowupIDs(ctx, q, `SELECT h.execution_id FROM hosted_executions h JOIN main_turns t ON t.request_id=h.execution_id JOIN native_turn_receipts_pending p ON p.request_id=t.request_id WHERE h.conversation_id=? AND json_extract(h.private_snapshot,'$.runner_run_id')=? ORDER BY h.execution_id`, conversation, run)
	if err != nil {
		return nil, err
	}
	if active && len(requests) != 1 {
		return nil, protocol.Errorf(protocol.CodeUnknownOutcome, "active closing repair has ambiguous original receipts")
	}
	replay := &ManagerReplyReplay{RunID: run, LeaseID: l.LeaseID}
	turns := map[string]bool{}
	for _, request := range requests {
		e, err := store.GetHostedExecution(ctx, q, request)
		if err != nil {
			return nil, err
		}
		var thread, turn string
		if err := q.QueryRowContext(ctx, `SELECT COALESCE(thread_id,''),COALESCE(turn_id,'') FROM main_turns WHERE request_id=?`, request).Scan(&thread, &turn); err != nil {
			return nil, err
		}
		if active {
			var repair bool
			if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM logical_turn_executions WHERE request_id=? AND repair=1)`, request).Scan(&repair); err != nil {
				return nil, err
			}
			if !repair {
				return nil, protocol.Errorf(protocol.CodeUnknownOutcome, "active closing prefix requires its original internal repair execution")
			}
		}
		s := e.Selection
		if thread == "" || turn == "" || replay.ThreadID != "" && replay.ThreadID != thread || e.IssueID != m.IssueID || e.AssignmentEpoch != m.AssignmentEpoch || s.IssueID != m.IssueID || s.AssignmentEpoch != m.AssignmentEpoch || s.Purpose != "issue_manager" || !s.RunnerManaged || s.RunnerRunID != run || s.Identity != pin.Identity || s.ConfigurationHash != pin.PublicSHA256 || s.Definition.LocalID != pin.Identity.LocalID || s.Definition.RuntimeID != pin.Runtime.RuntimeID || s.Definition.Revision != pin.AgentRevision || s.Definition.Model != pin.Profile.Model || s.Definition.Effort != pin.Profile.Effort || s.Runtime.ID != pin.Runtime.RuntimeID || s.Runtime.Revision != pin.Runtime.Revision {
			return nil, protocol.Errorf(protocol.CodeUnknownOutcome, "lapsed manager original receipt binding differs")
		}
		if replay.ThreadID != "" && !reflect.DeepEqual(replay.Selection, s) {
			return nil, protocol.Errorf(protocol.CodeUnknownOutcome, "lapsed manager has ambiguous frozen execution metadata")
		}
		replay.ThreadID, replay.Selection = thread, s
		turns[turn] = true
	}
	if replay.ThreadID == "" {
		return nil, protocol.Errorf(protocol.CodeUnknownOutcome, "lapsed manager has no original business receipt")
	}
	var resume bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM manager_native_requests WHERE run_id=? AND method IN ('start','resume') AND status='completed' AND COALESCE(json_extract(result_json,'$.error'),'')='' AND json_extract(result_json,'$.result.ID')=?)`, run, replay.ThreadID).Scan(&resume); err != nil {
		return nil, err
	}
	if !resume {
		return nil, protocol.Errorf(protocol.CodeUnknownOutcome, "lapsed manager has no confirmed original thread metadata")
	}
	// Include already applied fragments of each pending item. Replaying only
	// its remaining suffix could replace a previously saved streaming prefix.
	rows, err := q.QueryContext(ctx, `SELECT e.rowid,e.seq,e.body_json FROM manager_native_events e WHERE e.run_id=? AND (e.applied=0 OR (json_extract(e.body_json,'$.native.Kind')='agent_message_delta' AND EXISTS(SELECT 1 FROM manager_native_events p WHERE p.run_id=e.run_id AND p.applied=0 AND p.seq>=e.seq AND json_extract(p.body_json,'$.thread_id')=json_extract(e.body_json,'$.thread_id') AND json_extract(p.body_json,'$.native.TurnID')=json_extract(e.body_json,'$.native.TurnID') AND json_extract(p.body_json,'$.native.ItemID')=json_extract(e.body_json,'$.native.ItemID')))) ORDER BY e.seq`, run)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var event ManagerReplayEvent
		var raw string
		if err := rows.Scan(&event.Cursor, &event.Sequence, &raw); err != nil {
			return nil, err
		}
		event.RunID, event.Body = run, json.RawMessage(raw)
		var original struct {
			Disconnected bool
			ThreadID     string          `json:"thread_id"`
			Call         json.RawMessage `json:"call"`
			Native       *struct{ Kind, TurnID, ItemID string }
		}
		if err := json.Unmarshal(event.Body, &original); err != nil {
			return nil, err
		}
		if original.Disconnected || len(original.Call) != 0 && string(original.Call) != "null" || original.ThreadID != replay.ThreadID || original.Native == nil || original.Native.Kind != "agent_message_delta" || original.Native.ItemID == "" || !turns[original.Native.TurnID] {
			return nil, protocol.Errorf(protocol.CodeUnknownOutcome, "lapsed manager prefix requires another original receipt handler")
		}
		replay.Events = append(replay.Events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	replay.Selection.ReceiptOnly = true
	return replay, nil
}
