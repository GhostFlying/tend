package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

const independentReviewRetryLimit = 3

type independentReviewFailure struct {
	task, run, root, artifact, reason string
	role                              protocol.RunRole
	retries                           int
	specVersion                       int64
	assignmentEpoch                   int64
	retryAction                       string
	preparationRun                    string
}

func (f independentReviewFailure) canRetry() bool {
	if f.retryAction != "" || f.retries >= independentReviewRetryLimit {
		return false
	}
	failure := protocol.ParseWorkerFailureReason(f.reason)
	if failure == nil {
		return false
	}
	switch failure.Code {
	case "rate_limit_exceeded", "server_overloaded":
		return true
	case "httpConnectionFailed", "responseStreamConnectionFailed", "responseStreamDisconnected", "responseTooManyFailedAttempts",
		"http_connection_failed", "response_stream_connection_failed", "response_stream_disconnected":
		return failure.HTTPStatus == 0 || failure.HTTPStatus >= 500
	default:
		return false
	}
}

func currentIndependentReviewRun(ctx context.Context, q store.Querier, rc runContext) (bool, independentReviewTarget, error) {
	var target independentReviewTarget
	if rc.task.Lifecycle.Terminal() || rc.task.SpecVersion != rc.action.TargetSpecVersion {
		return false, target, nil
	}
	switch rc.run.Role {
	case protocol.RoleAccept:
		if rc.task.Lifecycle != protocol.TaskAwaitingAcceptance || rc.task.CurrentArtifactID != rc.action.TargetArtifactID {
			return false, target, nil
		}
	case protocol.RoleImplement:
		if rc.task.ImplementGeneration != rc.action.Generation {
			return false, target, nil
		}
	default:
		return false, target, nil
	}
	// A prebound reviewer Task may predate the explicit report target fields.
	target, err := boundIndependentReviewTarget(ctx, q, rc)
	if err != nil {
		return false, target, err
	}
	if rc.run.Role == protocol.RoleImplement && target.key == "" && rc.spec.ReviewArtifactID == "" && rc.spec.ReviewDeliveryID == "" {
		return false, target, nil
	}
	scope, err := store.ResolveTaskIssueScope(ctx, q, rc.task.TaskID)
	if err != nil || !scope.Canonical || scope.PrimaryID == "" {
		return false, target, err
	}
	if target.key == "" {
		i, err := store.GetCanonicalIssue(ctx, q, scope.PrimaryID)
		if err != nil {
			return false, target, err
		}
		// Retained Runs may predate target capture. Preserve their declared
		// reference without assigning a later observation to the old Run.
		target, err = declaredIndependentReviewTarget(ctx, q, i, rc.spec)
		target.head, target.base = "", ""
		return true, target, err
	}
	return true, target, nil
}

// recordIndependentReviewFailure replaces the generic retry decision with the
// named Issue owner's explicit next-step choice. The original Run still ends
// through the same Run, lease, and Task transitions.
func recordIndependentReviewFailure(ctx context.Context, x *Exec, tx *store.Tx, rc runContext, d *domain.Decision, reason string) (*independentReviewFailure, error) {
	current, target, err := currentIndependentReviewRun(ctx, tx.Q(), rc)
	if err != nil || !current {
		return nil, err
	}
	if err := saveIndependentReviewTarget(ctx, tx, rc.run.RunID, target); err != nil {
		return nil, err
	}
	root, retries := rc.run.RunID, 0
	err = tx.Q().QueryRowContext(ctx, `SELECT f.root_run_id,f.retries+1
		FROM independent_review_failures f JOIN plan_actions a ON a.action_id=f.retry_action_id
		WHERE a.task_id=? AND a.kind=? AND a.target_spec_version=? AND COALESCE(a.target_artifact_id,'')=? AND a.generation=?`,
		rc.task.TaskID, rc.action.Kind, rc.action.TargetSpecVersion, rc.action.TargetArtifactID, rc.action.Generation).Scan(&root, &retries)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO independent_review_failures(run_id,task_id,role,artifact_id,reason,root_run_id,retries,issue_assignment_epoch)
		SELECT ?,?,?,?,?,?,?,c.assignment_epoch FROM task_work_item_owners o JOIN issue_controls c ON c.issue_id=o.work_item_id WHERE o.task_id=?`,
		rc.run.RunID, rc.task.TaskID, rc.run.Role, rc.action.TargetArtifactID, reason, root, retries, rc.task.TaskID); err != nil {
		return nil, err
	}
	f, err := pendingIndependentReviewFailure(ctx, tx.Q(), rc.task.TaskID, rc.run.Role, rc.action.TargetArtifactID, rc.action.TargetSpecVersion)
	if err != nil {
		return nil, err
	}
	d.OpenDecisions = nil
	return f, nil
}

func pendingIndependentReviewFailure(ctx context.Context, q store.Querier, task string, role protocol.RunRole, artifact string, specVersion int64) (*independentReviewFailure, error) {
	f := &independentReviewFailure{task: task, role: role, artifact: artifact}
	err := q.QueryRowContext(ctx, `SELECT f.run_id,f.root_run_id,f.reason,f.retries,COALESCE(f.retry_action_id,''),f.issue_assignment_epoch,CASE WHEN f.task_id=? THEN a.target_spec_version ELSE retry.target_spec_version END
		FROM independent_review_failures f JOIN runs r USING(run_id) JOIN plan_actions a USING(action_id)
		JOIN independent_review_failures root ON root.run_id=f.root_run_id
		LEFT JOIN plan_actions retry ON retry.action_id=f.retry_action_id
		WHERE f.role=? AND f.artifact_id=? AND root.resolution_run_id IS NULL
		  AND ((f.task_id=? AND (?=0 OR a.target_spec_version=?))
		    OR (retry.task_id=? AND (?=0 OR retry.target_spec_version=?)))
		ORDER BY r.created_at DESC,r.rowid DESC LIMIT 1`, task, role, artifact, task, specVersion, specVersion, task, specVersion, specVersion).
		Scan(&f.run, &f.root, &f.reason, &f.retries, &f.retryAction, &f.assignmentEpoch, &f.specVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return f, err
}

func authorizeIndependentReviewRetry(ctx context.Context, x *Exec, q store.Querier, task store.TaskRow, f independentReviewFailure) error {
	if !f.canRetry() {
		return protocol.Errorf(protocol.CodeInvalidTransition, "the original independent review cannot be retried")
	}
	id, err := store.GetTaskIssueOwner(ctx, q, task.TaskID)
	if err != nil {
		return err
	}
	i, err := store.GetCanonicalIssue(ctx, q, id)
	if err != nil {
		return err
	}
	if i.AssignmentState != "active" || (i.State != protocol.IssueActive && i.State != protocol.IssuePaused) {
		return protocol.Errorf(protocol.CodeInvalidTransition, "the independent review requires an active Issue responsibility")
	}
	switch i.Assignee.Kind {
	case protocol.IssueAssigneeOwner:
		if p := x.ActualPrincipal(); p.Kind != protocol.PrincipalOwner || p.Subject != i.Assignee.Ref {
			return protocol.Errorf(protocol.CodeForbidden, "only the current Issue owner may choose this review retry")
		}
	case protocol.IssueAssigneeAgent:
		e := x.coordinationExecution
		if e == nil || e.Selection.Purpose != agentconfig.IssueManager || e.IssueID != id || e.AssignmentEpoch != i.AssignmentEpoch ||
			e.Selection.Identity.ExecutionEnvironmentID+"/"+e.Selection.Identity.LocalID != i.Assignee.Ref {
			return protocol.Errorf(protocol.CodeForbidden, "only the current Issue owner may choose this review retry")
		}
	default:
		return protocol.Errorf(protocol.CodeForbidden, "the independent review requires an assigned Issue owner")
	}
	return stoppedTaskRecoveryReady(ctx, q, task.TaskID, true)
}

func recordIndependentReviewFailureNotice(ctx context.Context, x *Exec, tx *store.Tx, f independentReviewFailure) error {
	issueID, err := store.GetTaskIssueOwner(ctx, tx.Q(), f.task)
	if err != nil {
		return err
	}
	i, err := store.GetCanonicalIssue(ctx, tx.Q(), issueID)
	if err != nil {
		return err
	}

	class := "没有可用于安全重试的类型化技术失败证明"
	if failure := protocol.ParseWorkerFailureReason(f.reason); failure != nil {
		class = failure.Code
		if failure.HTTPStatus > 0 {
			class += fmt.Sprintf("（HTTP %d）", failure.HTTPStatus)
		}
	}
	text := fmt.Sprintf("Issue %s 的独立审阅执行失败。审阅 Task %s，原 Run %s，失败原因 %s。已授权重试 %d/%d 次。当前负责人 %s。", issueID, f.task, f.run, class, f.retries, independentReviewRetryLimit, i.Assignee.Ref)
	text += " 查阅原 Run 的终态和对账记录，保留已有审阅报告与 findings。"
	if f.canRetry() {
		text += " 如果原副作用已对账并且仍有审阅需要，请明确决定是否使用 actions_create 为这个 Task 请求下一代执行。若责任交接使原执行契约过期，当前负责人可用 follow_up 指向原 Task，在同一候选上建立接续契约。"
	} else if f.retries == independentReviewRetryLimit {
		runs, err := store.FollowupIDs(ctx, tx.Q(), `SELECT run_id FROM independent_review_failures WHERE root_run_id=? ORDER BY retries`, f.root)
		if err != nil {
			return err
		}
		text += " 重试额度已用完。请检查运行环境和审阅要求，决定如何处置仍未完成的审阅。失败 Run：" + strings.Join(runs, "、") + "。"

	} else {
		text += " 请处置尚未完成的审阅义务。如需用户决定，报告原 Run 的执行事实。"
	}
	id := x.IDs.New(protocol.PrefixMessage)
	owner := i.Assignee.Kind == protocol.IssueAssigneeOwner || f.retries == independentReviewRetryLimit
	conversation := "main"
	if !owner && i.Assignee.Kind == protocol.IssueAssigneeAgent {
		conversation, err = ensureCanonicalManagerConversationForWake(ctx, x, tx, issueID)
		if err != nil {
			return err
		}
	}
	seq, err := tx.InsertMessage(ctx, store.MessageRow{ConversationID: conversation, MessageID: id, Author: "system", Body: text, CreatedAt: x.Now.UnixMilli()})
	if err != nil {
		return err
	}
	for _, b := range []struct{ kind, object string }{{"task", f.task}, {"work_item", issueID}, {"run", f.run}} {
		if err := tx.BindMessage(ctx, id, b.kind, b.object, "notice"); err != nil {
			return err
		}
	}
	event := x.IDs.New(protocol.PrefixEvent)
	data, err := json.Marshal(map[string]any{"run_id": f.run, "reason": protocol.ParseWorkerFailureReason(f.reason), "retry_count": f.retries, "retry_allowed": f.canRetry()})
	if err != nil {
		return err
	}
	if _, err := tx.InsertEvent(ctx, store.EventRow{
		EventID: event, Source: "app", ObjectType: "message", ObjectID: id, ObjectVersion: seq,
		Kind: "conversation.message", BodyJSON: string(data), RequestID: x.Env.RequestID, TraceID: x.TraceID, CreatedAt: x.Now.UnixMilli(),
	}); err != nil {
		return err
	}
	if owner {
		return registerSystemCommunication(ctx, x, tx, id, "independent_review_failure:"+f.run)
	}
	if conversation == "" {
		return nil
	}
	return appendBackgroundSource(ctx, x, tx, "work_item:"+issueID, issueID, id, event, "task.independent_review_failed", data)
}

// A consumed failure authorizes one action. All other action creation paths
// retain the outstanding failure until its owner uses the retry entry point.
func guardIndependentReviewAction(ctx context.Context, q store.Querier, a domain.ActionDraft, retryRun string) error {
	if a.Kind != protocol.ActionImplement && a.Kind != protocol.ActionAccept {
		return nil
	}
	f, err := pendingIndependentReviewFailure(ctx, q, a.TaskID, protocol.RunRole(a.Kind), a.ArtifactID, 0)
	if err != nil || f == nil || f.run == retryRun {
		return err
	}
	if a.Kind == protocol.ActionImplement && a.SpecVersion != f.specVersion {
		_, spec, err := loadTask(ctx, q, a.TaskID)
		if err != nil {
			return err
		}
		target, err := independentReviewTargetForTask(ctx, q, a.TaskID, spec)
		if err != nil {
			return err
		}
		if target.key != "" {
			if allowed, err := canStartIndependentReviewTarget(ctx, q, target, "", ""); err != nil || allowed {
				return err
			}
		}
	}
	return protocol.Errorf(protocol.CodeInvalidTransition, "the current Issue owner must resolve the retained independent review failure before another execution")
}

// settleIndependentReviewRetry records a real business submission from the
// approved generation, including a normal continuation across Run boundaries.
func settleIndependentReviewRetry(ctx context.Context, tx *store.Tx, rc runContext) error {
	_, err := tx.Exec(ctx, `UPDATE independent_review_failures SET resolution_run_id=?
		WHERE resolution_run_id IS NULL AND run_id=(
			SELECT f.root_run_id FROM independent_review_failures f JOIN plan_actions a ON a.action_id=f.retry_action_id
			WHERE a.task_id=? AND a.kind=? AND a.target_spec_version=? AND COALESCE(a.target_artifact_id,'')=? AND a.generation=?)`,
		rc.run.RunID, rc.task.TaskID, rc.action.Kind, rc.action.TargetSpecVersion, rc.action.TargetArtifactID, rc.action.Generation)
	return err
}

// independentReviewRetryResultRound resolves an existing failed binding to an
// actual outcome from its approved retry. All ordinary review qualification
// still uses the frozen candidate, requirements, profile and reviewer.
func independentReviewRetryResultRound(ctx context.Context, q store.Querier, review protocol.SharedDeliveryReview, round protocol.SharedDeliveryReviewRound) (protocol.SharedDeliveryReviewRound, error) {
	if round.ArtifactID != "" || round.ImplementGeneration <= 0 {
		return round, nil
	}
	var run, task string
	var generation, spec int64
	err := q.QueryRowContext(ctx, `SELECT completed.run_id,completed.task_id,completed_action.target_spec_version,completed_action.generation
		FROM independent_review_failures f
		JOIN runs failed ON failed.run_id=f.run_id
		JOIN plan_actions failed_action ON failed_action.action_id=failed.action_id
		JOIN runs completed ON completed.run_id=f.resolution_run_id
		JOIN plan_actions completed_action ON completed_action.action_id=completed.action_id
		WHERE f.root_run_id=f.run_id AND f.task_id=? AND f.role='implement'
		 AND failed_action.target_spec_version=? AND failed_action.generation=?
		 AND (?='' OR failed.run_id=?)`,
		round.TaskID, round.SpecVersion, round.ImplementGeneration, round.RunID, round.RunID).Scan(&run, &task, &spec, &generation)
	if errors.Is(err, sql.ErrNoRows) {
		return round, nil
	}
	if err != nil {
		return round, err
	}
	pin, err := store.GetRunExecution(ctx, q, run)
	if err != nil {
		return round, err
	}
	if round.ExecutionRequirements != nil {
		if err := store.RequireSharedReviewExecutionProfile(round.ExecutionRequirements, pin.Profile); err != nil {
			return round, err
		}
	}
	raw, err := store.GetSpec(ctx, q, task, spec)
	if err != nil {
		return round, err
	}
	var reviewerSpec domain.Spec
	if err := json.Unmarshal([]byte(raw), &reviewerSpec); err != nil {
		return round, err
	}
	if err := validateLinkedSharedReviewer(ctx, q, review, reviewerSpec, round.Reviewer, ""); err != nil {
		return round, err
	}
	round.RunID, round.TaskID, round.SpecVersion, round.ImplementGeneration = run, task, spec, generation
	round.ReviewerProfile = &pin.Profile
	return round, nil
}

// handoffIndependentReviewRetry uses an explicit follow-up of the old Task
// when the new Issue owner needs a fresh execution agreement.
func handoffIndependentReviewRetry(ctx context.Context, x *Exec, q store.Querier, issue protocol.Issue, target independentReviewTarget, relations []protocol.TaskRelation) (*independentReviewFailure, error) {
	if target.key == "" || strings.HasPrefix(target.key, "delivery|") && target.head == "" {
		return nil, nil
	}
	var selected *independentReviewFailure
	for _, relation := range relations {
		if relation.Kind != protocol.RelationFollowUp {
			continue
		}
		source, err := store.GetTask(ctx, q, relation.TaskID)
		if err != nil {
			return nil, err
		}
		primary, err := store.GetTaskIssueOwner(ctx, q, source.TaskID)
		if err != nil {
			return nil, err
		}
		if primary != issue.IssueID {
			continue
		}
		f, err := pendingIndependentReviewFailure(ctx, q, source.TaskID, protocol.RoleImplement, "", source.SpecVersion)
		if err != nil {
			return nil, err
		}
		if f == nil {
			continue
		}
		var original independentReviewTarget
		err = q.QueryRowContext(ctx, `SELECT target_key,head_sha,base_sha FROM independent_review_run_targets WHERE run_id=?`, f.run).
			Scan(&original.key, &original.head, &original.base)
		if errors.Is(err, sql.ErrNoRows) || err == nil && !matchesIndependentReviewRetryTarget(original, target) {
			continue
		}
		if err != nil {
			return nil, err
		}
		current, err := store.TaskOwnerContractCurrent(ctx, q, source.TaskID)
		if err != nil {
			return nil, err
		}
		if current && issue.AssignmentEpoch <= f.assignmentEpoch {
			continue
		}
		if selected != nil {
			return nil, protocol.Errorf(protocol.CodeInvalidRequest, "name one original failed review in the follow-up")
		}
		approval := *f
		if approval.retryAction != "" {
			if _, err := unclaimedIndependentReviewAction(ctx, q, approval.retryAction); err != nil {
				return nil, err
			}
			approval.retryAction = ""
		}
		if err := authorizeIndependentReviewRetry(ctx, x, q, source, approval); err != nil {
			return nil, err
		}
		selected = f
	}
	return selected, nil
}

func qualifyIndependentReviewHandoff(ctx context.Context, q store.Querier, failedRun, replacement string) error {
	original, err := store.GetRun(ctx, q, failedRun)
	if err != nil {
		return err
	}
	action, err := store.GetAction(ctx, q, original.ActionID)
	if err != nil {
		return err
	}
	ref, round, err := boundIndependentReviewRound(ctx, q, original.TaskID, action.TargetSpecVersion, original.Role)
	if err != nil || round == nil {
		return err
	}
	review, err := store.SharedDeliveryReviewForTarget(ctx, q, ref)
	if err != nil {
		return err
	}
	bound, err := bindSharedReviewTask(ctx, q, review, replacement, "", round.ExecutionRequirements)
	if err != nil {
		return err
	}
	if bound.Reviewer != round.Reviewer {
		return protocol.Errorf(protocol.CodeForbidden, "the review retry belongs to the original formal reviewer")
	}
	return nil
}

func repairIndependentReviewPreparation(ctx context.Context, x *Exec, q store.Querier, task store.TaskRow, spec domain.Spec) (*independentReviewFailure, error) {
	if x.Env.Operation != "issues.update_task" || task.BlockReason != protocol.BlockPreparationRequired || !spec.Workspace.IncludeExistingChanges {
		return nil, nil
	}
	f, err := pendingIndependentReviewFailure(ctx, q, task.TaskID, protocol.RoleImplement, "", task.SpecVersion)
	if err != nil || f == nil || f.retryAction == "" {
		return nil, err
	}
	// An immutable preparation failure was admitted only for a starting Run.
	// A second Run in the approved generation precludes transferring the slot.
	err = q.QueryRowContext(ctx, `SELECT r.run_id FROM plan_actions a JOIN runs r USING(action_id)
		JOIN run_preparation_failures p USING(run_id)
		WHERE a.action_id=? AND a.task_id=? AND a.generation=? AND a.status='done' AND r.observed_state='failed'
		 AND json_type(p.failure_json,'$.worker_started')='false'
		 AND NOT EXISTS(SELECT 1 FROM runs other JOIN plan_actions a2 ON a2.action_id=other.action_id
		   WHERE a2.task_id=a.task_id AND a2.kind=a.kind AND a2.target_spec_version=a.target_spec_version
		     AND a2.generation=a.generation AND other.run_id<>r.run_id)`,
		f.retryAction, task.TaskID, task.ImplementGeneration).Scan(&f.preparationRun)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	action, err := store.GetAction(ctx, q, f.retryAction)
	if err != nil {
		return nil, err
	}
	_, original, err := approvedIndependentReviewRetry(ctx, q, action)
	if err != nil {
		return nil, err
	}
	if original.key != "" {
		target, err := independentReviewTargetForTask(ctx, q, task.TaskID, spec)
		if err != nil || !matchesIndependentReviewRetryTarget(original, target) {
			return nil, err
		}
	}
	approval := *f
	approval.retryAction = ""
	if err := authorizeIndependentReviewRetry(ctx, x, q, task, approval); err != nil {
		return nil, err
	}
	return f, nil
}

func consumeIndependentReviewRetry(ctx context.Context, x *Exec, tx *store.Tx, failure *independentReviewFailure, actions []actionResult) error {
	if failure == nil {
		return nil
	}
	if len(actions) != 1 {
		return protocol.Errorf(protocol.CodeInvalidTransition, "an independent review approval requires exactly one execution action")
	}
	if failure.retryAction != "" {
		if failure.preparationRun == "" {
			original, err := unclaimedIndependentReviewAction(ctx, tx.Q(), failure.retryAction)
			if err != nil {
				return err
			}
			if original.Status == protocol.ActionPending {
				if err := tx.SetActionStatus(ctx, original.ActionID, protocol.ActionSuperseded, x.Now.UnixMilli()); err != nil {
					return err
				}
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE plan_actions SET trigger_json=json_set(trigger_json,'$.original_review_retry_action_id',?) WHERE action_id=?`,
			failure.retryAction, actions[0].ActionID); err != nil {
			return err
		}
	}
	result, err := tx.Exec(ctx, `UPDATE independent_review_failures SET retry_action_id=? WHERE run_id=? AND COALESCE(retry_action_id,'')=?`,
		actions[0].ActionID, failure.run, failure.retryAction)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return protocol.Errorf(protocol.CodeStaleRevision, "the original review retry has already been decided")
	}
	return nil
}

func unclaimedIndependentReviewAction(ctx context.Context, q store.Querier, action string) (store.ActionRow, error) {
	var a store.ActionRow
	var err error
	if a, err = store.GetAction(ctx, q, action); err != nil {
		return a, err
	}
	var claimed bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE action_id=?)`, action).Scan(&claimed); err != nil {
		return a, err
	}
	if claimed || (a.Status != protocol.ActionPending && a.Status != protocol.ActionSuperseded && a.Status != protocol.ActionCancelled) {
		return a, protocol.Errorf(protocol.CodeInvalidTransition, "settle the original approved retry before arranging another execution")
	}
	return a, nil
}
