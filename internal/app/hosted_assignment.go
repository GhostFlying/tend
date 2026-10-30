package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

// HostedIssueAssignment fences coordination responsibility, not worker runs.
// Start records intent before finish inspects the old executor's actual state.
type HostedIssueAssignment struct{ Finish bool }

func (c HostedIssueAssignment) Operation() string {
	if c.Finish {
		return "issues.finish_assignment"
	}
	return "issues.assign"
}
func (HostedIssueAssignment) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}
func (c HostedIssueAssignment) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	if x.Env.Target.Type != "issue" || x.Env.ParentRequestID != "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "assignment requires its own owner issue request")
	}
	expected, err := requireRevision(x.Env)
	if err != nil {
		return Outcome{}, err
	}
	current, err := store.GetCanonicalIssue(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, err
	}
	if current.Revision != expected {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "issue revision changed")
	}
	next := current
	if c.Finish {
		var p protocol.FinishIssueAssignmentPayload
		if err := decodePayload(x.Env, &p); err != nil {
			return Outcome{}, err
		}
		if p.ExpectedAssignmentEpoch != current.AssignmentEpoch {
			return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "assignment epoch changed")
		}
		if strings.TrimSpace(p.Reason) == "" || current.PendingHandoffID != p.HandoffID || current.PendingAssignee == nil {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "the current handoff and explicit reason are required")
		}
		if current.PendingAssignee.Kind == protocol.IssueAssigneeAgent {
			if err := rejectMigratedHostedAssignment(ctx, tx.Q(), current.IssueID); err != nil {
				return Outcome{}, err
			}
		}
		blockers, err := hostedHandoffBlockers(ctx, tx.Q(), current.IssueID, current.AssignmentEpoch)
		if err != nil {
			return Outcome{}, err
		}
		if len(blockers) > 0 {
			return Outcome{}, canonicalEndpointError("old coordination must be reconciled before enabling the new assignee", blockers)
		}
		if err := settleFencedHostedCoordination(ctx, x, tx, current.IssueID, current.AssignmentEpoch); err != nil {
			return Outcome{}, err
		}
		if err := resolveHostedAssignee(ctx, tx.Q(), x, *current.PendingAssignee); err != nil {
			return Outcome{}, err
		}
		next.IssueControl, err = domain.FinishIssueAssignment(current.IssueControl, p.HandoffID, true)
		if err != nil {
			return Outcome{}, err
		}
		if _, err = tx.Exec(ctx, `UPDATE issue_assignment_handoffs SET status='completed',reconciliation_json=?,updated_at=? WHERE issue_id=? AND handoff_id=? AND status<>'completed'`, "[]", x.Now.UnixMilli(), current.IssueID, p.HandoffID); err != nil {
			return Outcome{}, err
		}
		next.Revision++
		next.UpdatedAt = x.Now.UnixMilli()
		out, err := saveCanonicalOwnerDefinition(ctx, x, tx, next, current.Revision, canonicalOwnerIssueSource{}, p, "issue.assigned")
		if err != nil {
			return Outcome{}, err
		}
		if next.Assignee.Kind == protocol.IssueAssigneeAgent {
			if _, err := ensureCanonicalManagerConversation(ctx, tx, next, x.Now.UnixMilli()); err != nil {
				return Outcome{}, err
			}
		}
		if next.Assignee.Kind == protocol.IssueAssigneeAgent {
			if err := appendBackgroundSource(ctx, x, tx, "work_item:"+next.IssueID, next.IssueID, "", "", "issue.assigned", nil); err != nil {
				return Outcome{}, err
			}
		}
		if next.Assignee.Kind == protocol.IssueAssigneeOwner {
			if err := redirectPendingIssueInputsToOwner(ctx, x, tx, next.IssueID); err != nil {
				return Outcome{}, err
			}
		}
		return out, nil
	}
	var p protocol.IssueAssignmentPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if err := domain.CheckIssueEpoch(current.IssueControl, p.ExpectedAssignmentEpoch); err != nil {
		return Outcome{}, err
	}
	if strings.TrimSpace(p.Reason) == "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "assignment reason is required")
	}
	if p.Assignee.Kind == protocol.IssueAssigneeAgent {
		if err := rejectMigratedHostedAssignment(ctx, tx.Q(), current.IssueID); err != nil {
			return Outcome{}, err
		}
	}
	if err := resolveHostedAssignee(ctx, tx.Q(), x, p.Assignee); err != nil {
		return Outcome{}, err
	}
	if current.Assignee == p.Assignee {
		return Outcome{Result: current}, nil
	}
	handoff := x.IDs.New(protocol.Prefix("handoff"))
	next.IssueControl, err = domain.BeginIssueAssignment(current.IssueControl, p.Assignee, true, handoff)
	if err != nil {
		return Outcome{}, err
	}
	obligations, err := hostedHandoffBlockers(ctx, tx.Q(), current.IssueID, next.AssignmentEpoch)
	if err != nil {
		return Outcome{}, err
	}
	obligationsJSON, err := json.Marshal(obligations)
	if err != nil {
		return Outcome{}, err
	}
	oldJSON, err := json.Marshal(current.Assignee)
	if err != nil {
		return Outcome{}, err
	}
	targetJSON, err := json.Marshal(p.Assignee)
	if err != nil {
		return Outcome{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO issue_assignment_handoffs(handoff_id,issue_id,from_epoch,to_epoch,from_assignee_json,target_assignee_json,status,obligations_json,request_id,reason,created_at,updated_at) VALUES(?,?,?,?,?,?,'fenced',?,?,?,?,?)`, handoff, current.IssueID, current.AssignmentEpoch, next.AssignmentEpoch, string(oldJSON), string(targetJSON), string(obligationsJSON), x.Env.RequestID, p.Reason, x.Now.UnixMilli(), x.Now.UnixMilli())
	if err != nil {
		return Outcome{}, err
	}
	next.Revision++
	next.UpdatedAt = x.Now.UnixMilli()
	return saveCanonicalOwnerDefinition(ctx, x, tx, next, current.Revision, canonicalOwnerIssueSource{}, p, "issue.assignment_fenced")
}

func rejectMigratedHostedAssignment(ctx context.Context, q store.Querier, issue string) error {
	var migrated bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issue_manager_tasks WHERE issue_id=?)`, issue).Scan(&migrated); err != nil {
		return err
	}
	if migrated {
		return protocol.Errorf(protocol.CodeInvalidTransition, "migrated manager responsibilities require a category handoff")
	}
	return nil
}
func resolveHostedAssignee(ctx context.Context, q store.Querier, x *Exec, target protocol.IssueAssignee) error {
	if target.Kind == protocol.IssueAssigneeOwner {
		owner := x.Principal.Subject
		if x.Principal.Kind != protocol.PrincipalOwner {
			// Hosted callbacks retain their actual agent identity. The single
			// owner's identity comes from an authenticated, persisted request,
			// never from an external message author or the binding instance.
			var raw string
			err := q.QueryRowContext(ctx, `SELECT principal_json FROM request_records WHERE domain='owner' ORDER BY created_at DESC, rowid DESC LIMIT 1`).Scan(&raw)
			if errors.Is(err, sql.ErrNoRows) {
				return protocol.Errorf(protocol.CodeInvalidRequest, "authenticated owner identity is unavailable")
			}
			if err != nil {
				return err
			}
			var principal protocol.Principal
			if err := json.Unmarshal([]byte(raw), &principal); err != nil {
				return err
			}
			if principal.Kind != protocol.PrincipalOwner || principal.Subject == "" {
				return protocol.Errorf(protocol.CodeInvalidRequest, "authenticated owner identity is unavailable")
			}
			owner = principal.Subject
		}
		if target.Ref != owner {
			return protocol.Errorf(protocol.CodeInvalidRequest, "owner assignment must name the authenticated owner")
		}
		return nil
	}
	var retired bool
	if err := q.QueryRowContext(ctx, `SELECT finalized FROM manager_runner_cutover WHERE singleton=1`).Scan(&retired); err != nil {
		return err
	}
	if retired {
		return protocol.Errorf(protocol.CodeInvalidTransition, "hosted manager assignment is retired; publish a category task")
	}
	if target.Kind != protocol.IssueAssigneeAgent || x.hostedConfiguration == nil {
		return protocol.Errorf(protocol.CodeInvalidRequest, "agent assignment requires registered hosted definitions")
	}
	env := x.hostedConfiguration.Public().ExecutionEnvironmentID
	prefix := env + "/"
	if !strings.HasPrefix(target.Ref, prefix) {
		return protocol.Errorf(protocol.CodeInvalidRequest, "agent identity is not registered in this execution environment")
	}
	selected, err := x.hostedConfiguration.Select(strings.TrimPrefix(target.Ref, prefix), agentconfig.IssueManager)
	if err != nil {
		return err
	}
	if hostedconfig.IdentityRef(selected.Identity) != target.Ref {
		return protocol.Errorf(protocol.CodeInvalidRequest, "agent identity does not match the registered definition")
	}
	return nil
}
func hostedHandoffBlockers(ctx context.Context, q store.Querier, issue string, epoch int64) ([]protocol.IssueBlocker, error) {
	return views.ManagerHandoffBlockers(ctx, q, issue, epoch)
}

// settleFencedHostedCoordination retires only old manager continuation after
// actual execution/control reconciliation. The Issue's source duties remain.
func settleFencedHostedCoordination(ctx context.Context, x *Exec, tx *store.Tx, issue string, epoch int64) error {
	logicals, err := store.FollowupIDs(ctx, tx.Q(), `SELECT DISTINCT e.logical_turn_id FROM logical_turn_executions e JOIN hosted_executions h ON h.execution_id=e.request_id WHERE h.issue_id=? AND h.assignment_epoch<? ORDER BY e.logical_turn_id`, issue, epoch)
	if err != nil {
		return err
	}
	now := x.Now.UnixMilli()
	// A definite native rejection may have returned its ordinary delivery to
	// pending. Retire that old epoch's retry, not any uncertain native attempt;
	// source messages and their Issue review duties remain for the replacement.
	if _, err := tx.Exec(ctx, `UPDATE outbox SET status='failed',updated_at=? WHERE status='pending' AND operation IN ('main_thread.handle_message','main_thread.handle_event') AND request_id IN (SELECT h.execution_id FROM hosted_executions h JOIN main_turns t ON t.request_id=h.execution_id WHERE h.issue_id=? AND h.assignment_epoch<? AND t.status='rejected')`, now, issue, epoch); err != nil {
		return err
	}
	for _, logical := range logicals {
		if _, err := tx.Exec(ctx, `UPDATE logical_turns SET status='stopped',reason='Responsibility handed off after native reconciliation.',updated_at=? WHERE logical_turn_id=? AND status NOT IN ('completed','stopped')`, now, logical); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE outbox SET status='failed',updated_at=? WHERE operation='conversation.repair_turn' AND status='pending' AND request_id IN (SELECT request_id FROM logical_turn_executions WHERE logical_turn_id=?)`, now, logical); err != nil {
			return err
		}
	}
	recoveries, err := store.FollowupIDs(ctx, tx.Q(), `SELECT r.root_request_id FROM agent_recoveries r JOIN hosted_executions h ON h.execution_id=r.root_request_id WHERE h.issue_id=? AND h.assignment_epoch<? AND r.status IN ('waiting','blocked','exhausted') ORDER BY r.root_request_id`, issue, epoch)
	if err != nil {
		return err
	}
	for _, root := range recoveries {
		recovery, err := store.RecoveryForRequest(ctx, tx.Q(), root)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO agent_recovery_stops(message_id,stopped_at) VALUES(?,?) ON CONFLICT DO NOTHING`, recovery.MessageID, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE agent_recoveries SET status='cancelled',reason='Responsibility handed off after native reconciliation.',next_attempt_at=0,updated_at=? WHERE root_request_id=?`, now, root); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE outbox SET status='failed',updated_at=? WHERE operation='conversation.recover_agent' AND request_id=? AND status='pending'`, now, recovery.ActiveRequestID); err != nil {
			return err
		}
	}
	return nil
}

func ensureCanonicalManagerConversation(ctx context.Context, tx *store.Tx, issue protocol.Issue, now int64) (string, error) {
	conversation := "issue:" + issue.IssueID
	// Model/effort columns belong only to the legacy settings compatibility path.
	// Named manager selection always resolves the concrete assignment instead.
	_, err := tx.Exec(ctx, `INSERT INTO agent_conversations(conversation_id,work_item_id,model,effort,created_at) VALUES(?,?,'','',?) ON CONFLICT(conversation_id) DO NOTHING`, conversation, issue.IssueID, now)
	return conversation, err
}

func ensureCanonicalManagerConversationForWake(ctx context.Context, x *Exec, tx *store.Tx, item string) (string, error) {
	issue, err := store.GetCanonicalIssue(ctx, tx.Q(), item)
	if err != nil {
		return "", err
	}
	if issue.Assignee.Kind != protocol.IssueAssigneeAgent {
		return "", nil
	}
	// Completion facts and pending wakes retain the concrete responsibility
	// even when its runtime is unavailable. Native admission checks selection.
	return ensureCanonicalManagerConversation(ctx, tx, issue, x.Now.UnixMilli())
}

// Child endpoints wake only the parent responsibility. A shared source's main
// turn is never mistaken for the child's execution owner.
func recordCanonicalParentResult(ctx context.Context, x *Exec, tx *store.Tx, event Event, eventID string) error {
	child, err := store.GetCanonicalIssue(ctx, tx.Q(), event.ObjectID)
	if err != nil {
		return err
	}
	if child.ParentIssueID == "" {
		return nil
	}
	conversation, err := ensureCanonicalManagerConversationForWake(ctx, x, tx, child.ParentIssueID)
	if err != nil || conversation == "" {
		return err
	}
	message := x.IDs.New(protocol.PrefixMessage)
	body := "Child Issue " + child.IssueID + " reached " + string(child.State) + ". Read its exact current result and review the parent's remaining scope; this does not automatically close the parent or authorize new work."
	if _, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: message, ConversationID: conversation, Author: "system", Body: body, CreatedAt: x.Now.UnixMilli()}); err != nil {
		return err
	}
	return appendBackgroundSource(ctx, x, tx, "work_item:"+child.ParentIssueID, child.ParentIssueID, message, eventID, "issue.child_result", nil)
}

// Reparent changes inherited authority/context. Unlike assignment, it requires
// actual responsible turns and claimed deliveries in this subtree to settle,
// including context injection before the first native admission.
func hostedReparentBlockers(ctx context.Context, q store.Querier, issue string) ([]protocol.IssueBlocker, error) {
	ids, err := store.FollowupIDs(ctx, q, `WITH RECURSIVE scope(id) AS (
 SELECT ? UNION ALL SELECT c.issue_id FROM issue_controls c JOIN scope s ON c.parent_issue_id=s.id)
 SELECT h.execution_id FROM hosted_executions h JOIN scope s ON s.id=h.issue_id JOIN main_turns t ON t.request_id=h.execution_id
 WHERE t.status IN ('starting','started','unknown') OR EXISTS(SELECT 1 FROM native_turn_receipts_pending p WHERE p.request_id=t.request_id)
 OR EXISTS(SELECT 1 FROM agent_recoveries r WHERE r.root_request_id=h.execution_id AND r.status IN ('starting','running','unknown'))
 OR EXISTS(SELECT 1 FROM logical_turn_executions original
 JOIN logical_turn_executions e ON e.logical_turn_id=original.logical_turn_id
 JOIN outbox o ON o.request_id=e.request_id
 WHERE original.request_id=h.execution_id
 AND o.operation IN ('main_thread.handle_message','main_thread.handle_event','conversation.repair_turn','conversation.recover_agent')
 AND o.status IN ('sending','unknown'))
 UNION
 SELECT o.request_id FROM scope s JOIN agent_conversations c ON c.work_item_id=s.id
 JOIN outbox o ON o.destination='background_thread:'||c.conversation_id
 WHERE o.operation IN ('main_thread.handle_message','main_thread.handle_event')
 AND o.status IN ('sending','unknown') ORDER BY 1`, issue)
	if err != nil {
		return nil, err
	}
	out := []protocol.IssueBlocker{}
	for _, id := range ids {
		out = append(out, protocol.IssueBlocker{Code: "manager_execution", ObjectID: id})
	}
	return out, nil
}
