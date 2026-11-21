package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type managerProjectionInput struct {
	run, conversation, stored string
	sequence, received        int64
	event                     managerProjectionEvent
}

type managerProjectionEvent struct {
	ThreadID     string `json:"thread_id"`
	RootTurn     string `json:"root_turn_id"`
	Disconnected bool
	Call         *closedManagerCall
	Native       *managerProjectionItem
}

type managerProjectionItem struct {
	closedManagerItem
	RequestUsage *protocol.TokenUsageSample
	ContextUsage *protocol.AgentContextUsage
}

const (
	managerProjectionOperation = "manager.event.project"
	managerProjectionBudget    = 5 * time.Second
)

var errManagerProjectionUnsupported = errors.New("native event needs its original receipt or live consumer")

func (a *App) projectManagerNativeEvent(ctx context.Context, run string, seq int64) error {
	ctx = store.WithWriteOperation(store.WithWriteEvent(ctx, run, seq), managerProjectionOperation)
	reconcileCtx := ctx
	input, applied, err := a.readManagerProjection(ctx, run, seq)
	if err != nil {
		return err
	}
	if input.conversation == "" || input.conversation == "main" {
		return errManagerProjectionUnsupported
	}
	if applied {
		return nil
	}
	if item := input.event.Native; item != nil {
		stage, cancel := context.WithTimeout(ctx, managerProjectionBudget)
		staged, err := a.store.StageRolloutOriginals(stage, "conversation-rollout:"+input.conversation+":"+input.event.ThreadID, item.Raw)
		if err == nil && item.Tool != nil {
			staged, err = a.store.StageOriginals(staged, "conversation-tools:", []byte(protocol.RedactOwnerToolInput(item.Tool.Name, item.Tool.Input)), []byte(item.Tool.Output))
		}
		if err == nil && item.ItemType == protocol.ManagerItemAgentMessage && item.Kind == protocol.ManagerEventItemCompleted {
			staged, err = a.store.StageOriginals(staged, "conversation-replies:", []byte(item.Text))
		}
		if err != nil {
			cancel()
			return err
		}
		defer cancel()
		ctx = staged
	}
	env := &protocol.Envelope{
		RequestID: protocol.NativeCorrelationID(managerProjectionOperation, run+"/"+strconv.FormatInt(seq, 10)),
		Operation: managerProjectionOperation,
		Target:    protocol.Target{Type: "run", ID: run},
		Payload:   json.RawMessage(`{}`),
	}
	write, cancel := context.WithTimeout(ctx, managerProjectionBudget)
	op, err := a.Execute(write, protocol.Principal{Kind: protocol.PrincipalSystem}, env, projectManagerEvent{app: a, input: input})
	cancel()
	if err == nil && op.Error != nil {
		return op.Error
	}
	if err == nil || reconcileCtx.Err() != nil {
		return err
	}
	// A commit error can occur after the database accepted the write. Only
	// the original row can reconcile it, before any recovery retries.
	read, cancel := context.WithTimeout(reconcileCtx, managerProjectionBudget)
	defer cancel()
	confirmed, readErr := a.managerEventProjected(read, input)
	if readErr == nil && confirmed {
		a.notifyChange()
		return nil
	}
	return errors.Join(err, readErr)
}

// ManagerProjectsHistoricalCallbacks reports whether this original native
// Run is recovered by the database projection worker.
func (a *App) ManagerProjectsHistoricalCallbacks(ctx context.Context, run string) (bool, error) {
	var issue bool
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(ctx, `SELECT m.conversation_id NOT IN ('','main')
 FROM runs r JOIN issue_manager_tasks m USING(task_id) WHERE r.run_id=? AND r.role='issue_manager'`, run).Scan(&issue)
	})
	return issue, err
}

func (a *App) readManagerProjection(ctx context.Context, run string, seq int64) (managerProjectionInput, bool, error) {
	input := managerProjectionInput{run: run, sequence: seq}
	var applied bool
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		err := tx.Q().QueryRowContext(ctx, `SELECT m.conversation_id,e.body_json,e.received_at,e.applied
 FROM manager_native_events e JOIN runs r USING(run_id) JOIN issue_manager_tasks m USING(task_id)
 WHERE e.run_id=? AND e.seq=? AND r.role='issue_manager'`, run, seq).Scan(&input.conversation, &input.stored, &input.received, &applied)
		if err != nil {
			return err
		}
		raw, err := store.ResolveJSON(ctx, tx.Q(), input.stored)
		if err != nil {
			return err
		}
		return json.Unmarshal([]byte(raw), &input.event)
	})
	return input, applied, err
}

func (a *App) managerEventProjected(ctx context.Context, input managerProjectionInput) (bool, error) {
	var applied bool
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(ctx, `SELECT applied FROM manager_native_events WHERE run_id=? AND seq=? AND body_json=?`,
			input.run, input.sequence, input.stored).Scan(&applied)
	})
	return applied, err
}

type projectManagerEvent struct {
	app   *App
	input managerProjectionInput
}

func (projectManagerEvent) Operation() string { return managerProjectionOperation }
func (projectManagerEvent) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalSystem)
}

func (c projectManagerEvent) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	in := c.input
	var applied bool
	if err := tx.Q().QueryRowContext(ctx, `SELECT applied FROM manager_native_events WHERE run_id=? AND seq=? AND body_json=?`,
		in.run, in.sequence, in.stored).Scan(&applied); err != nil {
		return Outcome{}, err
	}
	if applied {
		return Outcome{NoEffect: true}, nil
	}
	ready, err := managerProjectionRecoveryEligible(ctx, tx.Q(), in.run, in.sequence)
	if err != nil || !ready {
		return Outcome{NoEffect: true}, err
	}
	var first, previous int64
	if err := tx.Q().QueryRowContext(ctx, `SELECT MIN(seq),(SELECT COUNT(*) FROM manager_native_events WHERE run_id=? AND seq<?)
FROM manager_native_events WHERE run_id=? AND applied=0`, in.run, in.sequence, in.run).Scan(&first, &previous); err != nil {
		return Outcome{}, err
	}
	if first != in.sequence {
		return Outcome{NoEffect: true}, nil
	}
	if previous != in.sequence-1 {
		return Outcome{}, protocol.Errorf(protocol.CodeUnknownOutcome, "original native event sequence has a gap")
	}
	out, err := in.project(ctx, c.app, x, tx)
	if err != nil {
		return Outcome{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE manager_native_events SET applied=1 WHERE run_id=? AND seq=? AND applied=0`, in.run, in.sequence); err != nil {
		return Outcome{}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM manager_event_apply_failures WHERE run_id=? AND seq=?`, in.run, in.sequence); err != nil {
		return Outcome{}, err
	}
	out.ProjectionOnly = true
	// When the last original receipt clears, wake the normal scheduler.
	if err := tx.Q().QueryRowContext(ctx, `SELECT NOT EXISTS(SELECT 1 FROM manager_native_events WHERE run_id=? AND applied=0)`, in.run).Scan(&out.Notify); err != nil {
		return Outcome{}, err
	}
	return out, nil
}

func (in managerProjectionInput) project(ctx context.Context, a *App, x *Exec, tx *store.Tx) (Outcome, error) {
	e := in.event
	if e.Disconnected || e.Call != nil {
		if e.Disconnected && e.Native == nil && e.Call == nil {
			return Outcome{}, requireManagerProjectionStop(ctx, tx.Q(), in)
		}
		if !e.Disconnected && e.Native == nil {
			return Outcome{}, projectManagerCallback(ctx, tx, in)
		}
		return Outcome{}, errManagerProjectionUnsupported
	}
	n := e.Native
	if n == nil || e.ThreadID == "" {
		return Outcome{}, errManagerProjectionUnsupported
	}
	if n.RequestUsage != nil {
		role := "background"
		if n.RequestUsage.ParentThreadID != "" {
			role = "subagent"
		}
		return Outcome{}, recordTokenUsage(ctx, tx, *n.RequestUsage, "conversation:"+in.conversation, role, x.Now.UnixMilli())
	}
	if n.ContextUsage != nil {
		return Outcome{}, in.projectContextUsage(ctx, tx, *n.ContextUsage)
	}
	switch n.Kind {
	case "item_started", protocol.ManagerEventItemCompleted:
		if n.ItemType == "contextCompaction" || n.TurnID == "" || n.ItemID == "" {
			return Outcome{}, errManagerProjectionUnsupported
		}
	case "turn_started", protocol.ManagerEventTurnCompleted, "turn_error", "child_rollout":
		if n.TurnID == "" {
			return Outcome{}, errManagerProjectionUnsupported
		}
	default:
		return Outcome{}, errManagerProjectionUnsupported
	}
	when := in.received
	if when == 0 {
		when = x.Now.UnixMilli()
	}
	if !stableRolloutEvent(e.ThreadID, n.TurnID, n.ItemID, n.Kind) {
		recorded, err := equivalentManagerRollout(ctx, tx.Q(), in)
		if err != nil || recorded {
			return Outcome{}, err
		}
	}
	if err := recordRolloutEvent(ctx, tx, in.conversation, e.ThreadID, n.TurnID, n.ItemID, n.Kind, n.Raw, when); err != nil {
		return Outcome{}, err
	}
	if n.Kind == protocol.ManagerEventItemCompleted && n.ItemType == "userMessage" {
		// This records only an exact matching steer receipt. The original
		// ordinary delivery remains governed by its admitted turn receipt.
		turn := n.TurnID
		if e.RootTurn != "" {
			turn = e.RootTurn
		}
		return Outcome{}, a.consumeIssueOwnerSteer(ctx, tx, in.conversation, e.ThreadID, turn, n.Text, n.ItemID, x.Now.UnixMilli())
	}
	if n.Tool == nil && (n.Kind != protocol.ManagerEventItemCompleted || n.ItemType != protocol.ManagerItemAgentMessage || n.Text == "") {
		return Outcome{}, nil
	}
	return in.projectMessage(ctx, x, tx)
}

func (in managerProjectionInput) projectContextUsage(ctx context.Context, tx *store.Tx, usage protocol.AgentContextUsage) error {
	current, err := store.ContextUsage(ctx, tx.Q(), in.conversation)
	when := in.received
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		return err
	case current.ThreadID == usage.ThreadID:
		if current.Revision >= usage.Revision {
			return nil
		}
		// An earlier receipt can contain a later revision than the live
		// write. Keep the timestamp used to order cross-thread snapshots.
		when = max(when, current.UpdatedAt)
	case current.UpdatedAt > when:
		return nil
	}
	return recordAgentUsage(ctx, tx, in.conversation, usage, when)
}

// Older attachments may have committed a rollout row before application.
// A non-item kind can have multiple distinct records on the same turn.
func equivalentManagerRollout(ctx context.Context, q store.Querier, in managerProjectionInput) (bool, error) {
	e, n := in.event, in.event.Native
	rows, err := q.QueryContext(ctx, `SELECT data_json FROM agent_rollout_events WHERE conversation_id=?
 AND thread_id=? AND COALESCE(turn_id,'')=? AND COALESCE(item_id,'')=? AND kind=?`,
		in.conversation, e.ThreadID, n.TurnID, n.ItemID, n.Kind)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	data := n.Raw
	if len(data) == 0 {
		data = json.RawMessage(`{}`)
	}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return false, err
		}
		raw, err := store.ResolveJSON(ctx, q, raw)
		if err != nil {
			return false, err
		}
		if store.SemanticJSONEqual(raw, string(data)) {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (in managerProjectionInput) projectMessage(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	e, n := in.event, in.event.Native
	root := n.TurnID
	if e.RootTurn != "" {
		root = e.RootTurn
	}
	source, repair, err := in.originalSource(ctx, tx.Q(), root)
	if err != nil || repair {
		return Outcome{}, err
	}
	projection := *x
	projection.Principal = protocol.Principal{Kind: protocol.PrincipalMainAgent, Thread: e.ThreadID}
	if n.Tool != nil {
		p := recordToolUsePayload{TurnID: n.TurnID, ItemID: n.ItemID, ReplyTo: source, Tool: n.Tool.Name, Input: n.Tool.Input, Output: n.Tool.Output, Status: n.Tool.Status}
		command := RecordToolUse{DeliveryTurn: root}
		stillRunning := p.Status == protocol.ToolRunning
		if stillRunning {
			// A native item_completed can still describe a backgrounded
			// command. Retain its observed output under the same stop proof.
			_, stoppedThrough, stopped, err := store.ManagerNativeClose(ctx, tx.Q(), in.run)
			if err != nil {
				return Outcome{}, err
			}
			command.interruptRunning = stopped && stoppedThrough >= in.sequence
		}
		out, err := applyManagerProjectionMessage(ctx, &projection, tx, p, command)
		if err != nil || !stillRunning || command.interruptRunning {
			return out, err
		}
		// Background tool records settle as interrupted when published.
		// Remember any already visible, running tool in the same transaction
		// that applies the original running observation.
		_, err = tx.Exec(ctx, `INSERT INTO manager_projected_running_tools(run_id,seq,message_id)
 SELECT ?,?,message_id FROM conversation_tool_uses
 WHERE thread_id=? AND turn_id=? AND item_id=? AND status='running'`,
			in.run, in.sequence, e.ThreadID, n.TurnID, n.ItemID)
		return out, err
	}
	p := recordAgentMessagePayload{TurnID: n.TurnID, ItemID: n.ItemID, ReplyTo: source, Body: n.Text, Status: protocol.AssistantCompleted}
	return applyManagerProjectionMessage(ctx, &projection, tx, p, RecordAgentMessage{DeliveryTurn: root, Reconciled: true})
}

func applyManagerProjectionMessage(ctx context.Context, x *Exec, tx *store.Tx, payload any, command Command) (Outcome, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return Outcome{}, err
	}
	env := *x.Env
	env.Payload = raw
	x.Env = &env
	return command.Apply(ctx, x, tx)
}

func (in managerProjectionInput) originalSource(ctx context.Context, q store.Querier, root string) (string, bool, error) {
	e, n := in.event, in.event.Native
	rows, err := q.QueryContext(ctx, `SELECT t.message_id,COALESCE(l.repair,0) FROM main_turns t
 JOIN hosted_executions h ON h.execution_id=t.request_id LEFT JOIN logical_turn_executions l ON l.request_id=t.request_id
 WHERE t.thread_id=? AND t.turn_id=? AND h.conversation_id=? AND json_extract(h.private_snapshot,'$.runner_run_id')=? LIMIT 2`, e.ThreadID, root, in.conversation, in.run)
	if err != nil {
		return "", false, err
	}
	var delivery string
	var repair bool
	if rows.Next() {
		err = rows.Scan(&delivery, &repair)
		if err == nil && rows.Next() {
			err = protocol.Errorf(protocol.CodeUnknownOutcome, "native event has multiple original deliveries")
		}
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil || repair {
		return "", repair, err
	}
	var source string
	err = q.QueryRowContext(ctx, `SELECT COALESCE(
 (SELECT message_id FROM main_tool_sources WHERE native_key=?),
 (SELECT NULLIF(m.reply_to,'') FROM conversation_messages m JOIN conversation_tool_uses u USING(message_id)
  WHERE u.thread_id=? AND u.turn_id=? AND u.item_id=? AND (m.conversation_id=? OR m.reply_to=?)),
 (SELECT NULLIF(m.reply_to,'') FROM conversation_messages m JOIN conversation_agent_messages u USING(message_id)
  WHERE u.thread_id=? AND u.turn_id=? AND u.item_id=? AND (m.conversation_id=? OR m.reply_to=?)),
 (SELECT b.message_id FROM background_replies r JOIN background_batches b USING(batch_id) JOIN conversation_messages m USING(message_id)
  WHERE r.thread_id=? AND r.turn_id=? AND r.item_id=? AND m.conversation_id=?),
 ?)`, e.ThreadID+"/"+n.TurnID+"/"+n.ItemID, e.ThreadID, n.TurnID, n.ItemID, in.conversation, delivery,
		e.ThreadID, n.TurnID, n.ItemID, in.conversation, delivery, e.ThreadID, n.TurnID, n.ItemID, in.conversation, delivery).Scan(&source)
	if err != nil {
		return "", false, err
	}
	if source == "" {
		return "", false, protocol.Errorf(protocol.CodeUnknownOutcome, "native item original message source is unavailable")
	}
	// A recorded source can be an owner message shared from Main. The
	// original delivery or stable native item binding proves attribution.
	return source, false, nil
}

func projectManagerCallback(ctx context.Context, tx *store.Tx, in managerProjectionInput) error {
	call := in.event.Call
	if call.ThreadID == "" || call.ThreadID != in.event.ThreadID || call.TurnID == "" || call.CallID == "" {
		return errManagerProjectionUnsupported
	}
	var delivery, status string
	err := tx.Q().QueryRowContext(ctx, `SELECT native_delivery_id,native_delivery_status FROM conversation_tool_uses
 WHERE native_run_id=? AND thread_id=? AND turn_id=? AND item_id=? AND native_delivery_id<>''`,
		in.run, call.ThreadID, call.TurnID, call.CallID).Scan(&delivery, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return errManagerProjectionUnsupported
	}
	if err != nil {
		return err
	}
	switch status {
	case "delivered", "rejected", store.NativeDeliveryRejectedCurrentTask:
		return nil
	case "produced":
		// Older stops may have saved the rejection without promoting the
		// bound delivery fact. Recover only from its retained original receipt.
		if err := requireManagerProjectionStop(ctx, tx.Q(), in); err != nil {
			return err
		}
		if rejected, err := reconcileRejectedManagerToolDelivery(ctx, tx, in.run, delivery); err != nil || rejected {
			return err
		}
	}
	return errManagerProjectionUnsupported
}

func requireManagerProjectionStop(ctx context.Context, q store.Querier, in managerProjectionInput) error {
	id, sequence, stopped, err := store.ManagerNativeClose(ctx, q, in.run)
	if err == nil && (!stopped || id == "" || sequence < in.sequence) {
		return errManagerProjectionUnsupported
	}
	return err
}

func settleManagerProjectedTools(ctx context.Context, x *Exec, tx *store.Tx, run string) error {
	_, through, stopped, err := store.ManagerNativeClose(ctx, tx.Q(), run)
	if err != nil || !stopped {
		return err
	}
	now := x.Now.UnixMilli()
	ids, err := store.FollowupIDs(ctx, tx.Q(), `UPDATE conversation_tool_uses SET status='interrupted',updated_at=?
 WHERE status='running' AND message_id IN (
  SELECT message_id FROM manager_projected_running_tools WHERE run_id=? AND seq<=?
 ) RETURNING message_id`, now, run, through)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := insertToolUseEvent(ctx, tx, x.IDs, id, protocol.ToolInterrupted, now); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `DELETE FROM manager_projected_running_tools WHERE run_id=? AND seq<=?`, run, through)
	return err
}

// A claimed close can need the unapplied tail before it can commit its
// physical receipt. Recovering local facts in this window releases no lease
// or authorization. The original close and stop guards still own those facts.
const managerProjectionRecoveryRun = `m.conversation_id NOT IN ('','main') AND r.role='issue_manager' AND (
 (r.native_closed_at>0 AND r.native_close_activity=r.native_activity AND e.seq<=r.native_close_sequence)
 OR EXISTS(SELECT 1 FROM manager_native_requests c WHERE c.run_id=r.run_id AND c.method='close' AND c.status IN ('started','unknown'))
)`

func managerProjectionRecoveryEligible(ctx context.Context, q store.Querier, run string, seq int64) (bool, error) {
	var eligible bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM manager_native_events e JOIN runs r USING(run_id)
 JOIN issue_manager_tasks m USING(task_id)
 WHERE e.run_id=? AND e.seq=? AND `+managerProjectionRecoveryRun+`)`, run, seq).Scan(&eligible)
	return eligible, err
}
