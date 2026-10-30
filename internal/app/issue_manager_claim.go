package app

import (
	"context"
	"encoding/json"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/scheduler"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func issueManagerCandidates(ctx context.Context, q store.Querier) ([]scheduler.Candidate, error) {
	return views.IssueManagerCandidates(ctx, q)
}

func claimIssueManager(ctx context.Context, x *Exec, tx *store.Tx, c scheduler.Candidate, r store.RunRow, l store.LeaseRow, pin *protocol.RunExecutionPin) (Outcome, error) {
	if pin == nil || pin.Purpose != "issue_manager" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "manager execution pin is required")
	}
	m, err := store.GetIssueManagerTask(ctx, tx.Q(), r.TaskID)
	if err != nil {
		return Outcome{}, err
	}
	i, err := store.GetCanonicalIssue(ctx, tx.Q(), m.IssueID)
	if err != nil {
		return Outcome{}, err
	}
	var events []Event
	if m.State == "pending" {
		if i.State != protocol.IssueActive {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "new manager responsibility requires an active issue")
		}
		if i.AssignmentEpoch != m.ExpectedEpoch || i.AssignmentState != "active" || (i.Assignee.Kind != protocol.IssueAssigneeOwner && m.HandoffID == "") {
			return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "manager responsibility changed")
		}
		if m.HandoffID != "" {
			blockers, err := hostedHandoffBlockers(ctx, tx.Q(), i.IssueID, i.AssignmentEpoch)
			if err != nil {
				return Outcome{}, err
			}
			if len(blockers) > 0 {
				return Outcome{}, canonicalEndpointError("old manager requires reconciliation", blockers)
			}
			if err = settleFencedHostedCoordination(ctx, x, tx, i.IssueID, i.AssignmentEpoch); err != nil {
				return Outcome{}, err
			}
			if _, err = tx.Exec(ctx, `UPDATE manager_handoffs SET stage='claimed',updated_at=? WHERE handoff_id=?`, x.Now.UnixMilli(), m.HandoffID); err != nil {
				return Outcome{}, err
			}
		}
		next := i
		next.Assignee = protocol.IssueAssignee{Kind: protocol.IssueAssigneeAgent, Ref: pin.Identity.ExecutionEnvironmentID + "/" + pin.Identity.LocalID}
		next.AssignmentEpoch++
		next.Revision++
		next.UpdatedAt = x.Now.UnixMilli()
		out, err := saveCanonicalOwnerDefinition(ctx, x, tx, next, i.Revision, canonicalOwnerIssueSource{}, map[string]any{"manager_task": r.TaskID}, "issue.manager_claimed")
		if err != nil {
			return Outcome{}, err
		}
		events = out.Events
		conversation, err := ensureCanonicalManagerConversation(ctx, tx, next, x.Now.UnixMilli())
		if err != nil {
			return Outcome{}, err
		}
		raw, err := json.Marshal(pin)
		if err != nil {
			return Outcome{}, err
		}
		_, err = tx.Exec(ctx, `UPDATE issue_manager_tasks SET assignment_epoch=?,conversation_id=?,host_id=?,pin_json=?,state='assigned',updated_at=? WHERE task_id=? AND state='pending'`, next.AssignmentEpoch, conversation, r.HostID, string(raw), x.Now.UnixMilli(), r.TaskID)
		if err != nil {
			return Outcome{}, err
		}
		if err = appendBackgroundSource(ctx, x, tx, "work_item:"+i.IssueID, i.IssueID, "", "", "issue.manager_claimed", nil); err != nil {
			return Outcome{}, err
		}
	}
	if !c.Manager.ReceiptOnly {
		_, err = tx.Exec(ctx, `UPDATE tasks SET lifecycle='running',updated_at=? WHERE task_id=?`, x.Now.UnixMilli(), r.TaskID)
	}
	if err != nil {
		return Outcome{}, err
	}
	as, err := buildManagerAssignment(ctx, tx.Q(), r, l, x.LeaseTTL)
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: claimResult{Assignment: as}, Events: append(events, Event{ObjectType: "run", ObjectID: r.RunID, Kind: "run.assigned", Body: map[string]any{"task_id": r.TaskID, "role": r.Role, "host_id": r.HostID}})}, nil
}

func buildManagerAssignment(ctx context.Context, q store.Querier, r store.RunRow, l store.LeaseRow, ttl time.Duration) (protocol.Assignment, error) {
	m, err := store.GetIssueManagerTask(ctx, q, r.TaskID)
	if err != nil {
		return protocol.Assignment{}, err
	}
	a, err := store.GetAction(ctx, q, r.ActionID)
	if err != nil {
		return protocol.Assignment{}, err
	}
	t, err := store.GetTask(ctx, q, r.TaskID)
	if err != nil {
		return protocol.Assignment{}, err
	}
	spec, err := store.GetSpec(ctx, q, r.TaskID, 1)
	if err != nil {
		return protocol.Assignment{}, err
	}
	pin, err := store.GetRunExecution(ctx, q, r.RunID)
	if err != nil {
		return protocol.Assignment{}, err
	}
	var receipt struct {
		ReceiptOnly bool `json:"receipt_only"`
	}
	_ = json.Unmarshal([]byte(a.TriggerJSON), &receipt)
	return protocol.Assignment{Manager: &protocol.IssueManagerAssignment{ReceiptOnly: receipt.ReceiptOnly, IssueID: m.IssueID, CategoryID: m.CategoryID, ConversationID: m.ConversationID, AssignmentEpoch: m.AssignmentEpoch}, Protocol: r.Protocol, Execution: pin, RunID: r.RunID, LeaseID: l.LeaseID, LeaseTTLMillis: ttl.Milliseconds(), TaskID: r.TaskID, ActionID: r.ActionID, Role: r.Role, Desired: desiredFor(r, a, t), Generation: r.Generation, SpecVersion: 1, Spec: json.RawMessage(spec)}, nil
}
