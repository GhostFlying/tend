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
	retryAction                       string
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
	if _, err := tx.Exec(ctx, `INSERT INTO independent_review_failures(run_id,task_id,role,artifact_id,reason,root_run_id,retries)
		VALUES(?,?,?,?,?,?,?)`, rc.run.RunID, rc.task.TaskID, rc.run.Role, rc.action.TargetArtifactID, reason, root, retries); err != nil {
		return nil, err
	}
	f, err := pendingIndependentReviewFailure(ctx, tx.Q(), rc.task.TaskID, rc.run.Role, rc.action.TargetArtifactID)
	if err != nil {
		return nil, err
	}
	d.OpenDecisions = nil
	return f, nil
}

func pendingIndependentReviewFailure(ctx context.Context, q store.Querier, task string, role protocol.RunRole, artifact string) (*independentReviewFailure, error) {
	f := &independentReviewFailure{task: task, role: role, artifact: artifact}
	err := q.QueryRowContext(ctx, `SELECT f.run_id,f.root_run_id,f.reason,f.retries,COALESCE(f.retry_action_id,'')
		FROM independent_review_failures f JOIN runs r USING(run_id)
		JOIN independent_review_failures root ON root.run_id=f.root_run_id
		WHERE f.task_id=? AND f.role=? AND f.artifact_id=? AND root.resolution_run_id IS NULL
		ORDER BY r.created_at DESC,r.rowid DESC LIMIT 1`, task, role, artifact).Scan(&f.run, &f.root, &f.reason, &f.retries, &f.retryAction)
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
		text += " 如果原副作用已对账并且仍有审阅需要，请明确决定是否使用 actions_create 为这个 Task 请求下一代执行。"
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
	f, err := pendingIndependentReviewFailure(ctx, q, a.TaskID, protocol.RunRole(a.Kind), a.ArtifactID)
	if err != nil || f == nil || f.run == retryRun {
		return err
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
