package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// SendRunMessage creates a durable supplement; it never changes a task contract.
type SendRunMessage struct{}

func (SendRunMessage) Operation() string { return protocol.OpRunsSendMessage }
func (SendRunMessage) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner, protocol.PrincipalMainAgent)
}

func (SendRunMessage) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	if !utf8.Valid(x.Env.Payload) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "message payload must be UTF-8")
	}
	var p protocol.SendRunMessagePayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if err := validateRunMessageText(p.Text); err != nil {
		return Outcome{}, err
	}
	expected, err := requireRevision(x.Env)
	if err != nil {
		return Outcome{}, err
	}
	rc, err := loadRun(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, errNotFound(err, "target run does not exist")
	}
	if x.Env.Target.Type != "run" || p.TaskID != rc.task.TaskID || p.SpecVersion != rc.action.TargetSpecVersion || p.Generation != rc.run.Generation || expected != rc.task.ControlRevision {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "task, run, spec, generation or control revision does not match")
	}
	if desiredFor(rc.run, rc.action, rc.task) != protocol.DesiredRun || (rc.run.ObservedState != protocol.RunRunning && rc.run.ObservedState != protocol.RunWaiting) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "target run must be running or waiting under managed control")
	}
	if x.coordinationExecution == nil && x.Principal.Kind == protocol.PrincipalMainAgent {
		if p.SourceMessageID == "" || p.CommitSeq < 1 {
			return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "hosted supplements require their source message and commit sequence")
		}
		source, err := store.GetMessage(ctx, tx.Q(), p.SourceMessageID)
		if err != nil {
			return Outcome{}, errNotFound(err, "source message does not exist")
		}
		// Background notices are provenance for already-authorized work, never
		// fabricated owner decisions. Persist the actual author separately.
		if source.Author != "owner" && source.Author != "system" {
			return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "source must be owner input or a ledger notice")
		}
		if err := claimPlanCommit(ctx, x, tx, p.SourceMessageID, p.CommitSeq); err != nil {
			return Outcome{}, err
		}

	} else if p.SourceMessageID != "" {
		if _, err := store.GetMessage(ctx, tx.Q(), p.SourceMessageID); err != nil {
			return Outcome{}, errNotFound(err, "source message does not exist")
		}
	}
	m := protocol.RunMessage{RunMessageDelivery: protocol.RunMessageDelivery{MessageID: x.IDs.New(protocol.PrefixMessage)},
		TaskID: rc.task.TaskID, RunID: rc.run.RunID, ActionID: rc.action.ActionID, SpecVersion: rc.action.TargetSpecVersion,
		Generation: rc.run.Generation, ControlRevision: expected, Text: p.Text, Status: protocol.RunMessageQueued,
		Source: x.Principal, SourceMessageID: p.SourceMessageID, RequestID: x.Env.RequestID, CreatedAt: x.Now.UnixMilli(), UpdatedAt: x.Now.UnixMilli()}
	runtime, err := store.GetHostRuntime(ctx, tx.Q(), rc.run.HostID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return Outcome{}, err
	}
	worker := protocol.WorkerAppServer
	pin, pinErr := store.GetRunExecution(ctx, tx.Q(), rc.run.RunID)
	if pinErr == nil {
		worker = pin.Worker
	} else if !errors.Is(pinErr, store.ErrNotFound) {
		return Outcome{}, pinErr
	} else {
		host, e := store.GetHost(ctx, tx.Q(), rc.run.HostID)
		if e != nil {
			return Outcome{}, e
		}
		if host.Worker != "" {
			worker = host.Worker
		}
	}
	if err != nil || !slices.Contains(runtime.Runtime.Capabilities, protocol.CapabilityRunMessages) {
		m.Status, m.Reason = protocol.RunMessageUnsupported, "runner_unsupported"
	} else if worker != protocol.WorkerAppServer && worker != protocol.WorkerOrca {
		m.Status, m.Reason = protocol.RunMessageUnsupported, "executor_unsupported"
	}
	enabled, err := store.RunMessagesEnabled(ctx, tx.Q(), rc.run.RunID)
	if err != nil {
		return Outcome{}, err
	}
	if m.Status == protocol.RunMessageQueued && !enabled {
		m.Status, m.Reason = protocol.RunMessageUnsupported, "run_not_negotiated"
	}
	if err := tx.InsertRunMessage(ctx, m); err != nil {
		return Outcome{}, err
	}
	if p.SourceMessageID != "" {
		if err := bindSource(ctx, tx, p.SourceMessageID, "run", m.RunID); err != nil {
			return Outcome{}, err
		}
	}
	m, err = store.GetRunMessage(ctx, tx.Q(), m.MessageID)
	return messageOutcome(m), err
}

func validateRunMessageText(text string) error {
	if strings.TrimSpace(text) == "" || len(text) > protocol.MaxRunMessageBytes || !utf8.ValidString(text) || strings.ContainsFunc(text, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' && r != '\r' }) {
		return protocol.Errorf(protocol.CodeInvalidRequest, "text must be nonempty UTF-8, at most 16384 bytes, without terminal control characters")
	}
	return nil
}

func messageOutcome(m protocol.RunMessage) Outcome {
	return Outcome{Result: m, Events: []Event{{ObjectType: "run_message", ObjectID: m.MessageID, Kind: "run.message_" + string(m.Status), Body: map[string]any{"run_id": m.RunID, "task_id": m.TaskID, "status": m.Status, "reason": m.Reason}}}}
}

// ClaimRunMessage serializes native delivery with run control and queue order.
type ClaimRunMessage struct{}

func (ClaimRunMessage) Operation() string { return protocol.OpRunsClaimMessage }
func (ClaimRunMessage) Authorize(ctx context.Context, x *Exec, q store.Querier) error {
	return authorizeMessageRun(ctx, x, q)
}
func authorizeMessageRun(ctx context.Context, x *Exec, q store.Querier) error {
	if x.Env.Target.Type != "run" || x.Principal.Kind != protocol.PrincipalRun || x.Principal.Subject != x.Env.Target.ID {
		return protocol.Errorf(protocol.CodeForbidden, "only the target run may report delivery")
	}
	r, err := store.GetRun(ctx, q, x.Principal.Subject)
	if err != nil {
		return err
	}
	if x.Principal.Role != r.Role {
		return protocol.Errorf(protocol.CodeForbidden, "run role does not match")
	}
	return nil
}
func (ClaimRunMessage) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.RunMessageDelivery
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if p.Receiver == "" || len(p.Receiver) > 128 || len(p.ThreadID) > 128 || len(p.TurnID) > 128 {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "bounded receiver identity is required")
	}
	m, err := store.GetRunMessage(ctx, tx.Q(), p.MessageID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return Outcome{}, err
	}
	if errors.Is(err, store.ErrNotFound) || m.RunID != x.Principal.Subject {
		return Outcome{}, protocol.Errorf(protocol.CodeNotFound, "message does not exist in this run")
	}
	if m.Status != protocol.RunMessageQueued {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "message is already claimed or settled; do not send it again")
	}
	page, err := store.ListRunMessages(ctx, tx.Q(), m.RunID, 0, 1, true)
	if err != nil {
		return Outcome{}, err
	}
	if len(page.Messages) == 0 || page.Messages[0].MessageID != m.MessageID {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "an earlier message is unresolved")
	}
	rc, err := loadRun(ctx, tx.Q(), m.RunID)
	if err != nil {
		return Outcome{}, err
	}
	lease, err := store.GetLeaseByRun(ctx, tx.Q(), m.RunID)
	if err != nil {
		return Outcome{}, err
	}
	if reason := obsoleteMessageReason(m, rc); reason != "" {
		m.Status, m.Reason = protocol.RunMessageFailed, reason
	} else if lease.Status != store.LeaseActive || lease.ExpiresAt <= x.Now.UnixMilli() || (rc.run.ObservedState != protocol.RunRunning && rc.run.ObservedState != protocol.RunWaiting) {
		m.Status, m.Reason = protocol.RunMessageFailed, "run_unavailable"
	} else {
		m.Status = protocol.RunMessageDispatching
		m.RunMessageDelivery = p
	}
	if err := tx.TransitionRunMessage(ctx, m, x.Now.UnixMilli()); err != nil {
		return Outcome{}, err
	}
	m, err = store.GetRunMessage(ctx, tx.Q(), m.MessageID)
	return messageOutcome(m), err
}

func obsoleteMessageReason(m protocol.RunMessage, rc runContext) string {
	if m.TaskID != rc.task.TaskID || m.ActionID != rc.action.ActionID || m.SpecVersion != rc.task.SpecVersion || m.Generation != rc.run.Generation {
		return "target_obsolete"
	}
	if m.ControlRevision != rc.task.ControlRevision || desiredFor(rc.run, rc.action, rc.task) != protocol.DesiredRun {
		return "run_control_changed"
	}
	return ""
}

// ReportRunMessage retains late facts without authorizing another invocation.
type ReportRunMessage struct{}

func (ReportRunMessage) Operation() string { return protocol.OpRunsReportMessage }
func (ReportRunMessage) Authorize(ctx context.Context, x *Exec, q store.Querier) error {
	return authorizeMessageRun(ctx, x, q)
}
func (ReportRunMessage) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.RunMessageReceipt
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if !validMessageReceipt(p.Status, p.Reason) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "receipt status and fixed reason do not match")
	}
	m, err := store.GetRunMessage(ctx, tx.Q(), p.MessageID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return Outcome{}, err
	}
	if errors.Is(err, store.ErrNotFound) || m.RunID != x.Principal.Subject {
		return Outcome{}, protocol.Errorf(protocol.CodeNotFound, "message does not exist in this run")
	}
	if m.RunMessageDelivery != p.RunMessageDelivery || m.Receiver == "" {
		return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "receipt does not match its delivery incarnation")
	}
	if m.Status == p.Status && m.Reason == p.Reason {
		return Outcome{Result: m}, nil
	}
	if m.Status != protocol.RunMessageDispatching && m.Status != protocol.RunMessageUnknown {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "message already has a definite result")
	}
	m.Status, m.Reason = p.Status, p.Reason
	if err := tx.TransitionRunMessage(ctx, m, x.Now.UnixMilli()); err != nil {
		return Outcome{}, err
	}
	m, err = store.GetRunMessage(ctx, tx.Q(), m.MessageID)
	return messageOutcome(m), err
}

func validMessageReceipt(status protocol.RunMessageStatus, reason string) bool {
	switch status {
	case protocol.RunMessageAccepted:
		return reason == "" || reason == "worker_acknowledged"
	case protocol.RunMessageUnsupported:
		return reason == "executor_unsupported" || reason == "native_method_unsupported"
	case protocol.RunMessageFailed:
		return reason == "native_rejected" || reason == "worker_ended" || reason == "run_control_changed"
	case protocol.RunMessageUnknown:
		return reason == "native_outcome_unknown" || reason == "receiver_restarted" || reason == "receipt_identity_mismatch"
	}
	return false
}

// RunMessages scopes all reads to a main-agent control identity or this run.
func (a *App) RunMessages(ctx context.Context, p protocol.Principal, run string, after int64, limit int, pending bool) (protocol.RunMessagePage, error) {
	if p.Validate() != nil {
		return protocol.RunMessagePage{}, protocol.Errorf(protocol.CodeForbidden, "valid control or run identity is required")
	}
	managerRun := p.Kind == protocol.PrincipalRun && p.Role == protocol.RoleIssueManager
	if p.Kind != protocol.PrincipalOwner && p.Kind != protocol.PrincipalBackgroundAgent && p.Kind != protocol.PrincipalMainAgent && !managerRun && (p.Kind != protocol.PrincipalRun || p.Subject != run) {
		return protocol.RunMessagePage{}, protocol.Errorf(protocol.CodeForbidden, "main agent or target run identity is required")
	}
	if after < 0 || limit < 1 || limit > 100 {
		return protocol.RunMessagePage{}, protocol.Errorf(protocol.CodeInvalidRequest, "after must be nonnegative and limit between 1 and 100")
	}
	var out protocol.RunMessagePage
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		rc, err := loadRun(ctx, tx.Q(), run)
		if err != nil {
			return errNotFound(err, "run does not exist")
		}
		if p.Kind == protocol.PrincipalBackgroundAgent || managerRun {
			if err := authorizeRunMessageManagerRead(ctx, tx.Q(), p, rc.task.TaskID, a.now().UnixMilli()); err != nil {
				return err
			}
		}
		if p.Kind == protocol.PrincipalRun && !managerRun && p.Role != rc.run.Role {
			return protocol.Errorf(protocol.CodeForbidden, "run role does not match")
		}
		out, err = store.ListRunMessages(ctx, tx.Q(), run, after, limit, pending)
		return err
	})
	return out, err
}

// settleObsoleteRunMessages records definitive no-send for queued messages when
// their frozen control scope ends. Claimed delivery remains unknown until proof.
func settleObsoleteRunMessages(ctx context.Context, x *Exec, tx *store.Tx, out *Outcome) error {
	if !slices.ContainsFunc(out.Events, func(e Event) bool { return e.ObjectType == "task" || e.ObjectType == "run" }) {
		return nil
	}
	seen := map[string]bool{}
	var ids []string
	for _, event := range out.Events {
		field := ""
		switch event.ObjectType {
		case "task":
			field = "task_id"
		case "run":
			field = "run_id"
		default:
			continue
		}
		rows, err := tx.Q().QueryContext(ctx, `SELECT message_id FROM run_messages WHERE status IN ('queued','dispatching') AND `+field+`=?`, event.ObjectID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			if !seen[id] {
				ids = append(ids, id)
				seen[id] = true
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		m, err := store.GetRunMessage(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		rc, err := loadRun(ctx, tx.Q(), m.RunID)
		if err != nil {
			return err
		}
		if reason := obsoleteMessageReason(m, rc); reason != "" {
			m.Status, m.Reason = protocol.RunMessageFailed, reason
			if m.Receiver != "" {
				m.Status, m.Reason = protocol.RunMessageUnknown, "native_outcome_unknown"
			}
			if err := tx.TransitionRunMessage(ctx, m, x.Now.UnixMilli()); err != nil {
				return err
			}
			out.Events = append(out.Events, messageOutcome(m).Events...)
		}
	}
	return nil
}

func authorizeRunMessageManagerRead(ctx context.Context, q store.Querier, p protocol.Principal, task string, now int64) error {
	conversation, err := store.AgentConversation(ctx, q, p.ConversationID)
	if err != nil {
		return err
	}
	primary, err := store.GetTaskIssueOwner(ctx, q, task)
	if errors.Is(err, store.ErrNotFound) {
		return protocol.Errorf(protocol.CodeForbidden, "run messages are outside this manager's primary Issue")
	}
	if err != nil {
		return err
	}
	if primary == "" || primary != conversation.WorkItemID {
		return protocol.Errorf(protocol.CodeForbidden, "run messages are outside this manager's primary Issue")
	}
	issue, err := store.GetCanonicalIssue(ctx, q, primary)
	if err != nil {
		return err
	}
	var executionID string
	err = q.QueryRowContext(ctx, `SELECT h.execution_id FROM hosted_executions h JOIN main_turns t ON t.request_id=h.execution_id WHERE h.conversation_id=? AND h.issue_id=? AND h.assignment_epoch=? AND t.thread_id=? AND t.status IN ('starting','started') ORDER BY t.updated_at DESC LIMIT 1`, p.ConversationID, primary, issue.AssignmentEpoch, p.Thread).Scan(&executionID)
	if err != nil {
		return protocol.Errorf(protocol.CodeForbidden, "manager has no current admitted execution for this run")
	}
	execution, err := store.GetHostedExecution(ctx, q, executionID)
	if err != nil {
		return err
	}
	if p.Kind == protocol.PrincipalRun {
		if !execution.Selection.RunnerManaged {
			return protocol.Errorf(protocol.CodeForbidden, "run cannot impersonate a hosted coordinator")
		}
		if err := authorizeRunnerCoordination(ctx, q, p, execution, false, now); err != nil {
			return err
		}
	}
	if issue.AssignmentState != "active" || issue.Assignee.Kind != protocol.IssueAssigneeAgent || issue.Assignee.Ref != hostedconfig.IdentityRef(execution.Selection.Identity) {
		return protocol.Errorf(protocol.CodeForbidden, "manager assignment has changed")
	}
	return nil
}
