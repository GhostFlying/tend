package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// HandleAttention answers or escalates one original problem under its own CAS.
// Agent callers must arrive through a current hosted coordination execution.
type HandleAttention struct{ Escalate bool }

func (c HandleAttention) Operation() string {
	if c.Escalate {
		return "attentions.escalate"
	}
	return "attentions.resolve"
}
func (HandleAttention) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}

func responsibilityRecipient(ctx context.Context, q store.Querier, r *protocol.AttentionResponsibility, issue string) error {
	r.IssueID, r.AssignmentEpoch, r.RecipientRef = issue, 0, ""
	if issue == "" {
		r.RecipientKind = "main"
		return nil
	}
	i, err := store.GetCanonicalIssue(ctx, q, issue)
	if err != nil {
		return err
	}
	r.RecipientKind, r.RecipientRef, r.AssignmentEpoch = string(i.Assignee.Kind), i.Assignee.Ref, i.AssignmentEpoch
	return nil
}

func responsibilityHop(r *protocol.AttentionResponsibility, reason, request string) protocol.AttentionResponsibilityHop {
	return protocol.AttentionResponsibilityHop{IssueID: r.IssueID, RecipientKind: r.RecipientKind, RecipientRef: r.RecipientRef, AssignmentEpoch: r.AssignmentEpoch, Reason: reason, RequestID: request}
}

// Responsibility scope follows current canonical ancestry, not the actor's ref.
func responsibilityAncestor(ctx context.Context, q store.Querier, origin, target string) (bool, error) {
	seen := map[string]bool{}
	for id := origin; id != ""; {
		if seen[id] {
			return false, protocol.Errorf(protocol.CodeInvalidTransition, "Issue hierarchy contains a cycle")
		}
		seen[id] = true
		if id == target {
			return true, nil
		}
		i, err := store.GetCanonicalIssue(ctx, q, id)
		if err != nil {
			return false, err
		}
		id = i.ParentIssueID
	}
	return target == "", nil
}

// A human taking over any already traversed responsibility ends Agent routing.
func humanBeforeRecipient(ctx context.Context, q store.Querier, origin, target string) (string, error) {
	for id := origin; id != "" && id != target; {
		i, err := store.GetCanonicalIssue(ctx, q, id)
		if err != nil {
			return "", err
		}
		if i.Assignee.Kind == protocol.IssueAssigneeOwner {
			return id, nil
		}
		id = i.ParentIssueID
	}
	return "", nil
}

func authorizeAttentionResponsibility(ctx context.Context, q store.Querier, x *Exec, a protocol.Attention) error {
	r := a.Responsibility
	if r == nil {
		return protocol.Errorf(protocol.CodeInvalidTransition, "attention has no delegated responsibility")
	}
	if a.Status != "open" {
		return protocol.Errorf(protocol.CodeInvalidTransition, "the original problem is no longer open")
	}
	origin, err := store.GetCanonicalIssue(ctx, q, r.OriginIssueID)
	if err != nil {
		return err
	}
	if origin.AssignmentEpoch != r.OriginAssignmentEpoch {
		return protocol.Errorf(protocol.CodeStaleRevision, "the originating Issue responsibility changed")
	}
	ancestry, err := store.ResponsibilityAncestry(ctx, q, r.OriginIssueID)
	if err != nil {
		return err
	}
	if !slices.Equal(ancestry, r.Ancestry) {
		return protocol.Errorf(protocol.CodeStaleRevision, "problem hierarchy changed")
	}
	valid, err := responsibilityAncestor(ctx, q, r.OriginIssueID, r.IssueID)
	if err != nil {
		return err
	}
	if !valid {
		return protocol.Errorf(protocol.CodeStaleRevision, "problem ancestry changed")
	}
	if human, err := humanBeforeRecipient(ctx, q, r.OriginIssueID, r.IssueID); err != nil {
		return err
	} else if human != "" {
		return protocol.Errorf(protocol.CodeStaleRevision, "a human owner must handle this original problem")
	}
	if r.IssueID == "" {
		for n := len(r.History) - 1; n >= 0; n-- {
			if r.History[n].IssueID != "" {
				i, e := store.GetCanonicalIssue(ctx, q, r.History[n].IssueID)
				if e != nil {
					return e
				}
				if i.ParentIssueID != "" {
					return protocol.Errorf(protocol.CodeStaleRevision, "a new parent must handle the problem before the top coordinator")
				}
				break
			}
		}
	}
	if r.IssueID != "" {
		i, err := store.GetCanonicalIssue(ctx, q, r.IssueID)
		if err != nil {
			return err
		}
		if (x.Principal.Kind != protocol.PrincipalOwner && (i.State != protocol.IssueActive || i.AssignmentState != "active")) || i.AssignmentEpoch != r.AssignmentEpoch || string(i.Assignee.Kind) != r.RecipientKind || i.Assignee.Ref != r.RecipientRef {
			return protocol.Errorf(protocol.CodeStaleRevision, "problem responsibility is no longer active")
		}
	}
	if x.Principal.Kind == protocol.PrincipalOwner {
		return nil
	}
	e := x.coordinationExecution
	if e == nil {
		return protocol.Errorf(protocol.CodeForbidden, "problem handling requires an admitted coordination execution")
	}
	if r.RecipientKind == "main" && e.Selection.Purpose == agentconfig.Main {
		return nil
	}
	if r.RecipientKind == "agent" && e.Selection.Purpose == agentconfig.IssueManager && e.IssueID == r.IssueID && e.AssignmentEpoch == r.AssignmentEpoch {
		return nil
	}
	return protocol.Errorf(protocol.CodeForbidden, "only the current problem supervisor may handle it")
}

func (c HandleAttention) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	if x.Env.Target.Type != "attention" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "attention target is required")
	}
	a, err := store.GetAttention(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, errNotFound(err, "attention does not exist")
	}
	if err = authorizeAttentionResponsibility(ctx, tx.Q(), x, a); err != nil {
		return Outcome{}, err
	}
	r := a.Responsibility
	revision, err := requireRevision(x.Env)
	if err != nil {
		return Outcome{}, err
	}
	if revision != r.Revision {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "problem responsibility changed")
	}
	var p protocol.HandleAttentionPayload
	if err = decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if p.ExpectedAssignmentEpoch != r.AssignmentEpoch {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "problem assignment epoch changed")
	}
	if strings.TrimSpace(p.Reason) == "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "handling or escalation reason is required")
	}
	if c.Escalate {
		if r.RecipientKind == "owner" {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "a human already owns this problem")
		}
		r.History = append(r.History, responsibilityHop(r, p.Reason, x.Env.RequestID))
		if r.RecipientKind == "main" {
			r.RecipientKind = "owner"
			r.RecipientRef = ""
			r.IssueID = ""
			r.AssignmentEpoch = 0
		} else {
			i, e := store.GetCanonicalIssue(ctx, tx.Q(), r.IssueID)
			if e != nil {
				return Outcome{}, e
			}
			if err = responsibilityRecipient(ctx, tx.Q(), r, i.ParentIssueID); err != nil {
				return Outcome{}, err
			}
		}
		if err = tx.SaveAttentionResponsibility(ctx, a.AttentionID, r, revision, x.Env.RequestID); err != nil {
			return Outcome{}, err
		}
		return Outcome{Result: r, Events: []Event{{ObjectType: "attention", ObjectID: a.AttentionID, Kind: "attention.responsibility_changed", Body: map[string]any{"reason": p.Reason}}}}, nil
	}
	if a.DecisionID != "" {
		d, err := store.GetDecision(ctx, tx.Q(), a.DecisionID)
		if err != nil {
			return Outcome{}, err
		}
		if d.Worker != nil {
			rc, err := loadRun(ctx, tx.Q(), d.Worker.RunID)
			if err != nil {
				return Outcome{}, err
			}
			current, err := store.RunIssueResponsibilityCurrent(ctx, tx.Q(), rc.run.RunID)
			if err != nil {
				return Outcome{}, err
			}
			contract, err := store.TaskOwnerContractCurrent(ctx, tx.Q(), rc.task.TaskID)
			if err != nil {
				return Outcome{}, err
			}
			handoff, err := store.QuestionHandoffCurrent(ctx, tx.Q(), rc.run.RunID)
			if err != nil {
				return Outcome{}, err
			}
			if (desiredFor(rc.run, rc.action, rc.task) == protocol.DesiredStopped && !handoff) || !current || !contract {
				return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "original worker question no longer applies")
			}
			if x.Principal.Kind != protocol.PrincipalOwner {
				var allowed bool
				if err = tx.Q().QueryRowContext(ctx, `SELECT COALESCE((SELECT supervisor_allowed FROM run_question_contracts WHERE run_id=?),0)`, d.Worker.RunID).Scan(&allowed); err != nil {
					return Outcome{}, err
				}
				if !allowed {
					return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "the existing run retains its human-answer contract; escalate this original question through the supervisor chain")
				}
			}
		}
		inner := *x
		env := *x.Env
		inner.Env = &env
		env.Target = protocol.Target{Type: "decision", ID: a.DecisionID}
		env.Payload, err = json.Marshal(protocol.AnswerDecisionPayload{Version: p.Version, Option: p.Option, Text: p.Text, Continuation: p.Continuation})
		if err != nil {
			return Outcome{}, err
		}
		out, e := (AnswerDecision{Responsible: x.Principal.Kind != protocol.PrincipalOwner, TypedContinuation: p.Continuation != nil}).Apply(ctx, &inner, tx)
		if e != nil {
			return Outcome{}, e
		}
		r.History = append(r.History, responsibilityHop(r, p.Reason, x.Env.RequestID))
		if err = tx.SaveAttentionResponsibility(ctx, a.AttentionID, r, revision, x.Env.RequestID); err != nil {
			return Outcome{}, err
		}
		return out, nil
	}
	if a.Kind != "action" || a.ChoiceID != "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "handle the original decision or an action")
	}
	if strings.TrimSpace(p.Text) == "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "action resolution evidence is required")
	}
	a.Status, a.Resolution = "resolved", p.Text
	actor := x.Principal
	a.ResolutionActor = &actor
	if err = saveAttention(ctx, x, tx, &a, a.Revision); err != nil {
		return Outcome{}, err
	}
	r.History = append(r.History, responsibilityHop(r, p.Reason, x.Env.RequestID))
	if err = tx.SaveAttentionResponsibility(ctx, a.AttentionID, r, revision, x.Env.RequestID); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: a, Events: []Event{attentionEvent(a)}}, nil
}

func initializeAttentionResponsibility(ctx context.Context, x *Exec, tx *store.Tx, a *protocol.Attention) error {
	if a.Responsibility != nil || a.IssueID == "" || a.Kind == "information" || a.Status != "open" {
		return nil
	}
	r := &protocol.AttentionResponsibility{OriginIssueID: a.IssueID, History: []protocol.AttentionResponsibilityHop{}}
	if err := responsibilityRecipient(ctx, tx.Q(), r, a.IssueID); err != nil {
		return err
	}
	r.OriginAssignmentEpoch = r.AssignmentEpoch
	// Owner execution questions go directly to the parent Issue owner.
	ownerExecution := false
	if x.Principal.Kind == protocol.PrincipalRun {
		if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM run_issue_responsibilities r JOIN run_question_contracts c USING(run_id) WHERE r.run_id=? AND r.issue_id=? AND c.handoff_allowed=1)`, x.Principal.Subject, a.IssueID).Scan(&ownerExecution); err != nil {
			return err
		}
	}
	if ownerExecution || (x.coordinationExecution != nil && x.coordinationExecution.Selection.Purpose == agentconfig.IssueManager && x.coordinationExecution.IssueID == a.IssueID) {
		r.History = append(r.History, responsibilityHop(r, "responsible Agent requested supervisor assistance", x.Env.RequestID))
		i, err := store.GetCanonicalIssue(ctx, tx.Q(), a.IssueID)
		if err != nil {
			return err
		}
		if err = responsibilityRecipient(ctx, tx.Q(), r, i.ParentIssueID); err != nil {
			return err
		}
	}
	if err := routeBlockedLegacyQuestion(ctx, x, tx, a, r); err != nil {
		return err
	}
	if err := tx.SaveAttentionResponsibility(ctx, a.AttentionID, r, 0, x.Env.RequestID); err != nil {
		return err
	}
	a.Responsibility = r
	return nil
}

// Old executions cannot yield their only slot. Escalate the factual capacity
// blocker along the same ancestry instead of hiding an unreachable question or
// inventing additional capacity. The hosted main coordinator remains reachable.
func routeBlockedLegacyQuestion(ctx context.Context, x *Exec, tx *store.Tx, a *protocol.Attention, r *protocol.AttentionResponsibility) error {
	if a.DecisionID == "" {
		return nil
	}
	d, err := store.GetDecision(ctx, tx.Q(), a.DecisionID)
	if err != nil || d.Worker == nil {
		return err
	}
	var yielding bool
	if err := tx.Q().QueryRowContext(ctx, `SELECT handoff_allowed FROM run_question_contracts WHERE run_id=?`, d.Worker.RunID).Scan(&yielding); err != nil {
		return err
	}
	if yielding {
		return nil
	}
	source, err := store.GetRun(ctx, tx.Q(), d.Worker.RunID)
	if err != nil {
		return err
	}
	host, err := store.GetHost(ctx, tx.Q(), source.HostID)
	if err != nil {
		return err
	}
	if host.Slots < 1 {
		return nil
	}
	var blocked int64
	if err := tx.Q().QueryRowContext(ctx, `SELECT COUNT(*) FROM runs r JOIN run_question_contracts c USING(run_id) WHERE r.host_id=? AND c.handoff_allowed=0 AND r.observed_state IN ('starting','running','waiting','interrupted','unknown') AND EXISTS(SELECT 1 FROM worker_questions w JOIN decisions d USING(decision_id) WHERE w.run_id=r.run_id AND d.status='open')`, source.HostID).Scan(&blocked); err != nil {
		return err
	}
	if blocked < host.Slots {
		return nil
	}
	seen := map[string]bool{}
	for r.RecipientKind == "agent" {
		if seen[r.IssueID] {
			return protocol.Errorf(protocol.CodeInvalidTransition, "Issue ancestry contains a cycle")
		}
		seen[r.IssueID] = true
		manager, err := store.CurrentIssueManagerTask(ctx, tx.Q(), r.IssueID)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if manager.HostID != source.HostID {
			return nil
		}
		issue, err := store.GetCanonicalIssue(ctx, tx.Q(), r.IssueID)
		if err != nil {
			return err
		}
		r.History = append(r.History, responsibilityHop(r, "legacy questions retain all runner slots; submit the original question to the upper responsibility", x.Env.RequestID))
		if err := responsibilityRecipient(ctx, tx.Q(), r, issue.ParentIssueID); err != nil {
			return err
		}
	}
	return nil
}

// recordResponsibilityWake uses the existing persistent background source path.
// Internal problems never become human communication publications.
func recordResponsibilityWake(ctx context.Context, x *Exec, tx *store.Tx, a protocol.Attention, event string) error {
	r := a.Responsibility
	if r == nil {
		return nil
	}
	if r.RecipientKind != "owner" {
		if err := cancelPendingProblemNotices(ctx, x, tx, a); err != nil {
			return err
		}
	}
	message := x.IDs.New(protocol.PrefixMessage)
	if a.DecisionID != "" {
		d, err := store.GetDecision(ctx, tx.Q(), a.DecisionID)
		if err != nil {
			return err
		}
		if d.Worker != nil && d.AskedMessageID != "" {
			var exists bool
			if err = tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_messages WHERE message_id=?)`, d.AskedMessageID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				message = d.AskedMessageID
			}
		}
	}
	conversation := "main"
	if r.RecipientKind == "agent" {
		var err error
		conversation, err = ensureCanonicalManagerConversationForWake(ctx, x, tx, r.IssueID)
		if err != nil {
			return err
		}
	}
	body := fmt.Sprintf("责任问题 %s：%s\n\n请读取原问题及 responsibility。能在既有授权内处理时使用 attentions_resolve；无法处理时使用 attentions_escalate，说明已尝试方法与阻塞。不要把内部问题直接通知用户或新建任务绕过原问题。", a.AttentionID, a.Title)
	if r.RecipientKind == "owner" {
		body = "需要你处理：" + a.Title
		if len(r.History) > 0 {
			body += "\n\n处理经过：" + r.History[len(r.History)-1].Reason
		}
	}

	if _, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: message, ConversationID: conversation, Author: "system", Body: body, CreatedAt: x.Now.UnixMilli()}); err != nil {
		return err
	}
	if event == "" {
		event = x.IDs.New(protocol.PrefixEvent)
		if _, err := tx.InsertEvent(ctx, store.EventRow{EventID: event, Source: "app", ObjectType: "message", ObjectID: message, Kind: "attention.responsibility_changed", BodyJSON: `{}`, RequestID: x.Env.RequestID, TraceID: x.TraceID, CreatedAt: x.Now.UnixMilli()}); err != nil {
			return err
		}
	}
	for _, b := range []struct{ kind, id string }{{"attention", a.AttentionID}, {"decision", a.DecisionID}, {"task", a.TaskID}} {
		if b.id != "" {
			if err := tx.BindMessage(ctx, message, b.kind, b.id, "responsibility"); err != nil {
				return err
			}
		}
	}
	if r.RecipientKind == "owner" {
		if err := registerSystemCommunication(ctx, x, tx, message, "responsibility_escalated:"+a.AttentionID); err != nil {
			return err
		}
	} else {
		group, issue := "responsibility:"+a.AttentionID, ""
		if r.RecipientKind == "agent" {
			group, issue = "work_item:"+r.IssueID, r.IssueID
		}
		if err := appendBackgroundSource(ctx, x, tx, group, issue, message, event, "attention.responsibility_changed", nil); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE attention_responsibilities SET notified_revision=revision WHERE attention_id=? AND revision=?`, a.AttentionID, r.Revision); err != nil {
		return err
	}
	return syncMessagingInteractions(ctx, x, tx, []Event{attentionEvent(a)})
}

// Reconcile old pending problems and assignment changes without replacing the
// question, the original worker answer channel, or any unknown business action.
func reconcileAttentionResponsibilities(ctx context.Context, x *Exec, tx *store.Tx) error {
	ids, err := store.FollowupIDs(ctx, tx.Q(), `SELECT r.attention_id FROM attention_responsibilities r JOIN attentions a USING(attention_id) LEFT JOIN issue_controls c ON c.issue_id=r.issue_id JOIN issue_controls origin ON origin.issue_id=r.origin_issue_id WHERE a.status='open' AND (COALESCE(json_extract(r.body_json,'$.ancestry'),'[]')<>(WITH RECURSIVE chain(id,parent,epoch,depth) AS (SELECT issue_id,parent_issue_id,assignment_epoch,0 FROM issue_controls WHERE issue_id=r.origin_issue_id UNION ALL SELECT c.issue_id,c.parent_issue_id,c.assignment_epoch,chain.depth+1 FROM issue_controls c JOIN chain ON c.issue_id=chain.parent) SELECT json_group_array(json_object('issue_id',id,'assignment_epoch',epoch)) FROM (SELECT id,epoch FROM chain ORDER BY depth)) OR EXISTS(WITH RECURSIVE path(id,parent,kind) AS (SELECT issue_id,parent_issue_id,assignee_kind FROM issue_controls WHERE issue_id=r.origin_issue_id UNION ALL SELECT c.issue_id,c.parent_issue_id,c.assignee_kind FROM issue_controls c JOIN path ON c.issue_id=path.parent WHERE path.id<>COALESCE(r.issue_id,'')) SELECT 1 FROM path WHERE id<>COALESCE(r.issue_id,'') AND kind='owner') OR COALESCE(json_extract(r.body_json,'$.origin_assignment_epoch'),0)<>origin.assignment_epoch OR (r.notified_revision<r.revision AND (r.recipient_kind='owner' OR r.issue_id IS NULL OR (c.state='active' AND c.assignment_state='active'))) OR (r.issue_id IS NOT NULL AND (r.assignment_epoch<>c.assignment_epoch OR r.recipient_kind<>c.assignee_kind OR r.recipient_ref<>c.assignee_ref)) OR (r.issue_id IS NOT NULL AND NOT EXISTS(WITH RECURSIVE chain(id,parent) AS (SELECT issue_id,parent_issue_id FROM issue_controls WHERE issue_id=r.origin_issue_id UNION ALL SELECT c.issue_id,c.parent_issue_id FROM issue_controls c JOIN chain ON c.issue_id=chain.parent) SELECT 1 FROM chain WHERE id=r.issue_id)) OR (r.issue_id IS NULL AND EXISTS(SELECT 1 FROM issue_controls root WHERE root.issue_id=(SELECT json_extract(value,'$.issue_id') FROM json_each(r.body_json,'$.history') WHERE COALESCE(json_extract(value,'$.issue_id'),'')<>'' ORDER BY key DESC LIMIT 1) AND root.parent_issue_id IS NOT NULL))) ORDER BY r.attention_id LIMIT 100`)
	if err != nil {
		return err
	}
	for _, id := range ids {
		a, err := store.GetAttention(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		r := a.Responsibility
		origin, err := store.GetCanonicalIssue(ctx, tx.Q(), r.OriginIssueID)
		if err != nil {
			return err
		}
		originChanged := r.OriginAssignmentEpoch != origin.AssignmentEpoch
		ancestry, err := store.ResponsibilityAncestry(ctx, tx.Q(), r.OriginIssueID)
		if err != nil {
			return err
		}
		hierarchyChanged := !slices.Equal(ancestry, r.Ancestry)
		valid, err := responsibilityAncestor(ctx, tx.Q(), r.OriginIssueID, r.IssueID)
		if err != nil {
			return err
		}
		if valid && r.IssueID == "" {
			for n := len(r.History) - 1; n >= 0; n-- {
				if r.History[n].IssueID == "" {
					continue
				}
				i, e := store.GetCanonicalIssue(ctx, tx.Q(), r.History[n].IssueID)
				if e != nil {
					return e
				}
				valid = i.ParentIssueID == ""
				break
			}
		}
		human := ""
		if valid {
			human, err = humanBeforeRecipient(ctx, tx.Q(), r.OriginIssueID, r.IssueID)
			if err != nil {
				return err
			}
		}
		if !valid || originChanged || hierarchyChanged || human != "" {
			old := r.Revision
			reason := "Issue hierarchy changed; re-evaluate from the current responsibility"
			if originChanged {
				reason = "Originating Issue owner changed; re-evaluate from the new responsibility"
			}
			r.History = append(r.History, responsibilityHop(r, reason, x.Env.RequestID))
			r.OriginAssignmentEpoch = origin.AssignmentEpoch
			target := r.OriginIssueID
			if valid && hierarchyChanged && !originChanged {
				// Revisit the first changed responsibility already reached by
				// this escalation. A pending handoff must retain that boundary
				// until the replacement (including a human) becomes active.
				for index, ancestor := range ancestry {
					target = ancestor.IssueID
					if index >= len(r.Ancestry) || ancestor != r.Ancestry[index] || target == r.IssueID {
						break
					}
				}
			}
			if human != "" {
				target = human
			}
			if err = responsibilityRecipient(ctx, tx.Q(), r, target); err != nil {
				return err
			}
			if err = tx.SaveAttentionResponsibility(ctx, id, r, old, x.Env.RequestID); err != nil {
				return err
			}
		}
		if r.IssueID != "" {
			i, err := store.GetCanonicalIssue(ctx, tx.Q(), r.IssueID)
			if err != nil {
				return err
			}
			if r.AssignmentEpoch != i.AssignmentEpoch || r.RecipientKind != string(i.Assignee.Kind) || r.RecipientRef != i.Assignee.Ref {
				old := r.Revision
				r.History = append(r.History, responsibilityHop(r, "Issue responsibility changed", x.Env.RequestID))
				if err = responsibilityRecipient(ctx, tx.Q(), r, r.IssueID); err != nil {
					return err
				}
				if err = tx.SaveAttentionResponsibility(ctx, id, r, old, x.Env.RequestID); err != nil {
					return err
				}
			}
			if r.RecipientKind != "owner" && (i.State != protocol.IssueActive || i.AssignmentState != "active") {
				continue
			}
		}
		oldRevision, hops := r.Revision, len(r.History)
		if err := routeBlockedLegacyQuestion(ctx, x, tx, &a, r); err != nil {
			return err
		}
		if len(r.History) != hops {
			if err := tx.SaveAttentionResponsibility(ctx, id, r, oldRevision, x.Env.RequestID); err != nil {
				return err
			}
		}
		if err = recordResponsibilityWake(ctx, x, tx, a, ""); err != nil {
			return err
		}
	}
	return nil
}

func attentionForDecision(ctx context.Context, q store.Querier, id string) (protocol.Attention, bool, error) {
	var attention string
	err := q.QueryRowContext(ctx, `SELECT attention_id FROM attentions WHERE decision_id=?`, id).Scan(&attention)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.Attention{}, false, nil
	}
	if err != nil {
		return protocol.Attention{}, false, err
	}
	a, err := store.GetAttention(ctx, q, attention)
	return a, err == nil, err
}

func recordInternalProblemAnswer(ctx context.Context, x *Exec, tx *store.Tx, a protocol.Attention, d store.DecisionRow, event string) error {
	return recordResponsibilityResolution(ctx, x, tx, a, json.RawMessage(d.AnswerJSON), event, "decision.answered")
}

func recordResponsibilityResolution(ctx context.Context, x *Exec, tx *store.Tx, a protocol.Attention, answer json.RawMessage, event, kind string) error {
	issue := a.Responsibility.OriginIssueID
	i, err := store.GetCanonicalIssue(ctx, tx.Q(), issue)
	if err != nil {
		return err
	}
	message := x.IDs.New(protocol.PrefixMessage)
	conversation := "main"
	if i.Assignee.Kind == protocol.IssueAssigneeAgent {
		conversation, err = ensureCanonicalManagerConversation(ctx, tx, i, x.Now.UnixMilli())
		if err != nil {
			return err
		}
	}
	if _, err = tx.InsertMessage(ctx, store.MessageRow{MessageID: message, ConversationID: conversation, Author: "system", Body: "负责人已处理原问题 " + a.AttentionID + "：" + string(answer), CreatedAt: x.Now.UnixMilli()}); err != nil {
		return err
	}
	if err = tx.BindMessage(ctx, message, "attention", a.AttentionID, "answer"); err != nil {
		return err
	}
	if i.Assignee.Kind == protocol.IssueAssigneeAgent {
		return appendBackgroundSource(ctx, x, tx, "work_item:"+issue, issue, message, event, kind, answer)
	}
	if x.Principal.Kind == protocol.PrincipalMainAgent {
		return nil
	}
	return appendBackgroundSource(ctx, x, tx, "attention:"+a.AttentionID, "", message, event, kind, answer)
}

// Upgrade and assignment changes retire only notices that certainly have not
// been sent. In-flight and unknown receipts must still be reconciled.
func cancelPendingProblemNotices(ctx context.Context, x *Exec, tx *store.Tx, a protocol.Attention) error {
	ids, err := store.FollowupIDs(ctx, tx.Q(), `SELECT DISTINCT d.message_id FROM messaging_deliveries d JOIN conversation_bindings b ON b.message_id=d.message_id JOIN outbox o ON o.outbox_id=d.delivery_id WHERE o.status='pending' AND ((b.object_type='attention' AND b.object_id=?) OR (b.object_type='decision' AND b.object_id=?))`, a.AttentionID, a.DecisionID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err = tx.Exec(ctx, `UPDATE messaging_deliveries SET state='cancelled',reason='internal_responsibility',updated_at=? WHERE message_id=? AND delivery_id IN (SELECT outbox_id FROM outbox WHERE status='pending')`, x.Now.UnixMilli(), id); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE outbox SET status='cancelled',updated_at=? WHERE status='pending' AND outbox_id IN (SELECT delivery_id FROM messaging_deliveries WHERE message_id=? AND state='cancelled')`, x.Now.UnixMilli(), id); err != nil {
			return err
		}
		if err = communicationEvent(ctx, x, tx, id, "messaging.delivery"); err != nil {
			return err
		}
	}
	return nil
}
