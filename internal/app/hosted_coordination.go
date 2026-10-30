package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// HostedCoordinationCommand is constructed by the actual hosted tool callback.
// It keeps the authenticated principal intact and delegates only after checking
// the persisted native execution, current role and assignment epoch.
type HostedCoordinationCommand struct {
	Command     Command
	ExecutionID string
	EffectSeq   int64
}

func (c HostedCoordinationCommand) Operation() string { return c.Command.Operation() }
func (c HostedCoordinationCommand) Authorize(ctx context.Context, x *Exec, q store.Querier) error {
	if err := allowKinds(x.Principal, protocol.PrincipalMainAgent, protocol.PrincipalBackgroundAgent); err != nil {
		return err
	}
	if c.EffectSeq < 1 {
		return protocol.Errorf(protocol.CodeInvalidRequest, "positive effect_seq is required")
	}
	execution, err := store.GetHostedExecution(ctx, q, c.ExecutionID)
	if err != nil {
		return errNotFound(err, "coordination execution does not exist")
	}
	turn, err := store.GetMainTurn(ctx, q, c.ExecutionID)
	if err != nil {
		return err
	}
	if turn.ThreadID != x.Principal.Thread || (x.Principal.Kind == protocol.PrincipalBackgroundAgent && execution.ConversationID != x.Principal.ConversationID) {
		return protocol.Errorf(protocol.CodeForbidden, "tool caller does not own this native execution")
	}
	if execution.Selection.Purpose == agentconfig.Consultation {
		if x.Principal.Kind != protocol.PrincipalBackgroundAgent || c.Operation() != "conversation.promote" || x.Env.Target.Type != "conversation" || x.Env.Target.ID != execution.ConversationID {
			return protocol.Errorf(protocol.CodeForbidden, "consultation can only delegate its own selected owner request")
		}
	}
	if execution.Selection.Purpose == agentconfig.Main {
		if x.Principal.Kind != protocol.PrincipalMainAgent || !slices.Contains([]string{"conversation.set_issue_associations", "issues.create", "issues.assign", "issues.finish_assignment", "attentions.create", "attentions.update", "attentions.respond", "attentions.confirm", "attentions.withdraw", "tasks.rate", "issues.deadlines_put", "issues.deadlines_cancel", "planner.entries_put"}, c.Operation()) {
			return protocol.Errorf(protocol.CodeForbidden, "the personal planner must delegate issue work, not execute its task planning")
		}
	} else if execution.Selection.Purpose != agentconfig.Consultation {
		if x.Principal.Kind != protocol.PrincipalBackgroundAgent || execution.IssueID == "" {
			return protocol.Errorf(protocol.CodeForbidden, "issue coordination needs its registered conversation")
		}
		current, err := store.GetCanonicalIssue(ctx, q, execution.IssueID)
		if err != nil {
			return err
		}
		if current.AssignmentState != "active" || current.AssignmentEpoch != execution.AssignmentEpoch || current.Assignee.Kind != protocol.IssueAssigneeAgent || current.Assignee.Ref != hostedconfig.IdentityRef(execution.Selection.Identity) {
			return protocol.Errorf(protocol.CodeForbidden, "assignment no longer permits this coordination execution")
		}
		switch c.Operation() {
		case "issues.deadlines_put":
			var p protocol.PutIssueDeadlinePayload
			if err := decodePayload(x.Env, &p); err != nil {
				return err
			}
			if p.IssueID != current.IssueID || p.Nature != "estimate" {
				return protocol.Errorf(protocol.CodeForbidden, "manager proposes only its Issue estimate")
			}
		case "planner.entries_put":
			var p protocol.PutPlannerEntryPayload
			if err := decodePayload(x.Env, &p); err != nil {
				return err
			}
			if p.Entry.IssueID != current.IssueID {
				return protocol.Errorf(protocol.CodeForbidden, "propose next actions only in this Issue")
			}
		case "attentions.create":
			var p protocol.CreateAttentionPayload
			if err := decodePayload(x.Env, &p); err != nil {
				return err
			}
			if p.IssueID != current.IssueID {
				return protocol.Errorf(protocol.CodeForbidden, "raise attention only in this Issue")
			}
		case "attentions.update", "attentions.respond", "attentions.confirm", "attentions.withdraw":
			a, err := store.GetAttention(ctx, q, x.Env.Target.ID)
			if err != nil {
				return err
			}
			if x.Env.Target.Type != "attention" || a.IssueID != current.IssueID {
				return protocol.Errorf(protocol.CodeForbidden, "attention is outside this Issue")
			}
		case "conversation.commit_turn":
			if x.Env.Target.Type != "logical_turn" || x.Env.Target.ID != execution.ID {
				return protocol.Errorf(protocol.CodeForbidden, "commit only this execution")
			}
		case "issues.create":
			var p protocol.CreateIssuePayload
			if err := decodePayload(x.Env, &p); err != nil {
				return err
			}
			if p.ParentIssueID != current.IssueID || p.Plan.Project != current.Plan.Project {
				return protocol.Errorf(protocol.CodeForbidden, "a manager may create only child issues in its inherited project")
			}
		case "issues.set_status", "issues.followups_create", "issues.refresh_delivery", "issues.update", "issues.pause", "issues.resume", "issues.cancel", "issues.finish_cancel", "issues.close", "issues.reopen", "issues.dependencies", "issues.review_completions", "issues.review_sources", "issues.create_task", "issues.review_context", "issues.context_fact":
			if x.Env.Target.Type != "issue" || x.Env.Target.ID != current.IssueID {
				return protocol.Errorf(protocol.CodeForbidden, "tool target is outside this issue")
			}
		case "issues.followups_update", "issues.followups_pause", "issues.followups_resume", "issues.followups_cancel", "issues.followups_trigger":
			rule, err := store.GetFollowup(ctx, q, x.Env.Target.ID)
			if err != nil {
				return err
			}
			if x.Env.Target.Type != "followup" || rule.WorkItemID != current.IssueID {
				return protocol.Errorf(protocol.CodeForbidden, "followup is outside this Issue")
			}
		case protocol.OpRunsSendMessage:
			run, err := store.GetRun(ctx, q, x.Env.Target.ID)
			if err != nil {
				return err
			}
			primary, err := store.GetTaskIssueOwner(ctx, q, run.TaskID)
			if err != nil {
				return err
			}
			if x.Env.Target.Type != "run" || primary != current.IssueID {
				return protocol.Errorf(protocol.CodeForbidden, "run supplement requires this Issue's immutable primary task")
			}
		case "issues.update_task", "tasks.cancel", "tasks.rate", "actions.create":
			primary, err := store.GetTaskIssueOwner(ctx, q, x.Env.Target.ID)
			if err != nil {
				return err
			}
			if x.Env.Target.Type != "task" || primary != current.IssueID {
				return protocol.Errorf(protocol.CodeForbidden, "task control requires this issue's immutable primary")
			}
		default:
			return protocol.Errorf(protocol.CodeForbidden, "operation is unavailable to the issue manager")
		}
	}
	if execution.Selection.Purpose == agentconfig.Main && c.Operation() == "tasks.rate" {
		primary, err := store.GetTaskIssueOwner(ctx, q, x.Env.Target.ID)
		if err != nil {
			return err
		}
		if _, err := store.GetCanonicalIssue(ctx, q, primary); err != nil {
			return err
		}
	}
	if turn.Status != store.TurnStarting && turn.Status != store.TurnStarted {
		// A repeated exact request may read its receipt, but a finished native turn
		// cannot create a fresh effect. Authorize is also run for recorded replays.
		if _, err := store.GetRequest(ctx, q, x.Principal.DedupDomain(), x.Env.RequestID); errors.Is(err, store.ErrNotFound) {
			return protocol.Errorf(protocol.CodeForbidden, "native execution is no longer active")
		} else if err != nil {
			return err
		}
	}
	return nil
}
func (c HostedCoordinationCommand) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	execution, err := store.GetHostedExecution(ctx, tx.Q(), c.ExecutionID)
	if err != nil {
		return Outcome{}, err
	}
	stable, err := store.HostedCoordinationIdentity(ctx, tx.Q(), execution.ID)
	if err != nil {
		return Outcome{}, err
	}
	var logical string
	if execution.Selection.Purpose != agentconfig.Consultation {
		if err := tx.Q().QueryRowContext(ctx, `SELECT logical_turn_id FROM logical_turn_executions WHERE request_id=?`, execution.ID).Scan(&logical); err != nil {
			return Outcome{}, err
		}
	}

	var original string
	err = tx.Q().QueryRowContext(ctx, `SELECT request_id FROM hosted_coordination_effects WHERE execution_id=? AND effect_seq=?`, stable, c.EffectSeq).Scan(&original)
	if err == nil {
		return Outcome{}, duplicateEffect("coordination effect was already applied", map[string]any{"coordination_execution_id": stable, "effect_seq": c.EffectSeq, "original_request_id": original})
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Outcome{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO hosted_coordination_effects(execution_id,effect_seq,request_id) VALUES(?,?,?)`, stable, c.EffectSeq, x.Env.RequestID); err != nil {
		return Outcome{}, err
	}
	if execution.Selection.Purpose == agentconfig.Main && c.Operation() == "issues.create" {
		source := hostedPayloadSource(x.Env.Payload)
		var admitted bool
		if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM logical_turn_executions e JOIN logical_turn_messages m USING(logical_turn_id) WHERE e.request_id=? AND m.message_id=?) OR EXISTS(SELECT 1 FROM main_steers s JOIN main_turns t ON t.thread_id=s.thread_id AND t.turn_id=s.turn_id WHERE t.request_id=? AND s.message_id=? AND s.native_item_id<>'')`, execution.ID, source, execution.ID, source).Scan(&admitted); err != nil {
			return Outcome{}, err
		}
		if !admitted {
			return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "delegate only admitted source material from the current planner turn")
		}
	}
	if execution.IssueID != "" {
		var sources struct {
			SourceDispositions []protocol.IssueSourceDisposition `json:"source_dispositions"`
		}
		if err := json.Unmarshal(x.Env.Payload, &sources); err != nil {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "invalid issue source review")
		}
		for _, source := range sources.SourceDispositions {
			if err := requireHostedSourceAdmitted(ctx, tx.Q(), source.MessageID); err != nil {
				return Outcome{}, err
			}
		}
		if err := requireHostedSourceAdmitted(ctx, tx.Q(), hostedPayloadSource(x.Env.Payload)); err != nil {
			return Outcome{}, err
		}
		if err := hostedSourceInIssueScope(ctx, tx.Q(), execution.IssueID, hostedPayloadSource(x.Env.Payload)); err != nil {
			return Outcome{}, err
		}
	}
	adapted := *x
	adapted.coordinationExecution = &execution
	adapted.coordinationEffectSeq = c.EffectSeq
	adapted.coordinationID = stable
	adapted.coordinationLogicalID = logical
	adapted.executionRequest = execution.ID
	// No owner parent is substituted: the original source remains payload audit
	// material and the actual execution/effect provides the mutation identity.
	out, err := c.Command.Apply(ctx, &adapted, tx)
	if err != nil {
		return Outcome{}, err
	}
	out.NoEffect = false
	return out, nil
}

func hostedSourceInIssueScope(ctx context.Context, q store.Querier, issueID, message string) error {
	if message == "" {
		return nil
	}
	for id := issueID; id != ""; {
		var allowed bool
		err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_messages m JOIN agent_conversations c ON c.conversation_id=m.conversation_id WHERE m.message_id=? AND c.work_item_id=?) OR EXISTS(SELECT 1 FROM message_work_items WHERE message_id=? AND work_item_id=? AND active=1) OR EXISTS(SELECT 1 FROM issue_control_revisions WHERE source_message_id=? AND issue_id=?) OR EXISTS(SELECT 1 FROM message_context_obligations WHERE message_id=? AND work_item_id=?)`, message, id, message, id, message, id, message, id).Scan(&allowed)
		if err != nil {
			return err
		}
		if allowed {
			return nil
		}
		current, err := store.GetCanonicalIssue(ctx, q, id)
		if err != nil {
			return err
		}
		id = current.ParentIssueID
	}
	return protocol.Errorf(protocol.CodeForbidden, "source material is outside this issue's inherited scope")
}

// Keep raw owner-input projections at the existing boundary, never manufacture
// a second task agreement representation for manager calls.
func hostedPayloadSource(raw json.RawMessage) string {
	var p protocol.IssueSourceReference
	_ = json.Unmarshal(raw, &p)
	return p.SourceMessageID
}

// Manager closure consumes verified facts from the existing evidence resolver,
// never re-labeling its own command as an owner decision.
func canonicalEndpointEvidence(ctx context.Context, x *Exec, q store.Querier, issue protocol.Issue, evidence []protocol.IssueEvidence) error {
	if x.coordinationExecution == nil {
		return validateCanonicalOwnerEndpointEvidence(evidence)
	}
	w := protocol.WorkItem{WorkItemID: issue.IssueID, IssuePlan: issue.Plan}
	return validateHostedIssueEvidence(ctx, x, q, w, nil, evidence)
}
func validateHostedIssueEvidence(ctx context.Context, x *Exec, q store.Querier, w protocol.WorkItem, previous *protocol.WorkItem, evidence []protocol.IssueEvidence) error {
	for _, e := range evidence {
		if _, saved := savedIssueEvidence(previous, e); saved {
			continue
		}
		if e.Kind == protocol.WorkItemEvidenceOwnerDecision {
			if e.Reference == "current_request" {
				return protocol.Errorf(protocol.CodeForbidden, "manager coordination is not an owner decision")
			}
			attention, err := resolvedIssueAttentionDecision(ctx, q, w.WorkItemID, e.Reference)
			if err != nil {
				return err
			}
			if attention {
				continue
			}
			if protocol.ValidID(protocol.Prefix("attn"), e.Reference) {
				return protocol.Errorf(protocol.CodeInvalidRequest, "owner_decision attention evidence must be a resolved decision or action for this issue")
			}
			var answered bool
			if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM decisions d JOIN task_work_item_owners o ON o.task_id=d.task_id WHERE d.decision_id=? AND d.status='answered' AND o.work_item_id=?) OR EXISTS(SELECT 1 FROM work_item_choices WHERE choice_id=? AND work_item_id=? AND status='answered')`, e.Reference, w.WorkItemID, e.Reference, w.WorkItemID).Scan(&answered); err != nil {
				return err
			}
			if !answered {
				if err := requireHostedSourceAdmitted(ctx, q, e.Reference); err != nil {
					return err
				}
				var withdrawn bool
				if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_message_controls WHERE message_id=? AND withdrawn_at<>0)`, e.Reference).Scan(&withdrawn); err != nil {
					return err
				}
				if withdrawn {
					return protocol.Errorf(protocol.CodeForbidden, "withdrawn owner material is not completion evidence")
				}
				if err := hostedSourceInIssueScope(ctx, q, w.WorkItemID, e.Reference); err != nil {
					return err
				}
			}
		}
	}
	return validateIssueEvidenceReferences(ctx, x, q, w, evidence, previous, "evidence")
}

func requireHostedSourceAdmitted(ctx context.Context, q store.Querier, message string) error {
	if message == "" {
		return nil
	}
	admitted, err := store.OwnerInputAdmitted(ctx, q, message)
	if err != nil {
		return err
	}
	if !admitted {
		return protocol.Errorf(protocol.CodeForbidden, "source owner input is still queued")
	}
	return nil
}
