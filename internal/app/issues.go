package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// CreateIssue starts an open responsibility without automatically creating work.
type CreateIssue struct{}

func (CreateIssue) Operation() string { return "work_items.create" }
func (CreateIssue) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, planners...)
}
func (CreateIssue) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.CreateWorkItemPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	project, err := resolveProject(ctx, tx.Q(), p.Project, x.Principal)
	if err != nil {
		return Outcome{}, err
	}
	p.Project = project.Project
	if p.Stages == nil {
		p.Stages = []protocol.IssueStage{}
	}
	if err := domain.ValidateIssue(p.IssuePlan, nil); err != nil {
		return Outcome{}, err
	}
	if p.InitialContext != nil {
		if err := validateInitialReviews(ctx, x, tx, *p.InitialContext); err != nil {
			return Outcome{}, err
		}
	}
	w := protocol.WorkItem{IssuePlan: p.IssuePlan, WorkItemID: x.IDs.New(protocol.PrefixWorkItem), Revision: 1, Status: "open", CreatedAt: x.Now.UnixMilli(), UpdatedAt: x.Now.UnixMilli()}
	if err := validateIssueReferences(ctx, x, tx.Q(), w, nil); err != nil {
		return Outcome{}, err
	}
	if err := claimIssueSource(ctx, x, tx, w.WorkItemID, p.SourceMessageID, p.CommitSeq); err != nil {
		return Outcome{}, err
	}
	freezeIssueCurrentEvidence(&w, x.Env.RequestID)
	out, err := saveIssue(ctx, tx, w, 0, "work_item.created", p)
	if err != nil {
		return Outcome{}, err
	}
	if _, err = ensureAgentConversation(ctx, x, tx, w.WorkItemID); err != nil {
		return Outcome{}, err
	}
	if p.InitialContext != nil {
		if err := initializeWorkItemContext(ctx, x, tx, w.WorkItemID, *p.InitialContext); err != nil {
			return Outcome{}, err
		}
	}
	return out, nil
}

// UpdateIssue revises a plan while preserving execution agreements and history.
type UpdateIssue struct{}

func (UpdateIssue) Operation() string { return "work_items.update" }
func (UpdateIssue) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, planners...)
}
func (UpdateIssue) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.UpdateWorkItemPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	w, err := issueForWrite(ctx, x, tx.Q())
	if err != nil {
		return Outcome{}, err
	}
	if w.Status == "closed" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "reopen the work item before revising it")
	}
	project, err := resolveProject(ctx, tx.Q(), p.Project, x.Principal)
	if err != nil {
		return Outcome{}, err
	}
	p.Project = project.Project
	if p.Stages == nil {
		p.Stages = []protocol.IssueStage{}
	}
	if err := domain.ValidateIssue(p.IssuePlan, nil); err != nil {
		return Outcome{}, err
	}
	candidate := w
	candidate.IssuePlan = p.IssuePlan
	if err := validateIssueReferences(ctx, x, tx.Q(), candidate, &w); err != nil {
		return Outcome{}, err
	}
	p.IssuePlan = candidate.IssuePlan
	if err := domain.ValidateIssue(p.IssuePlan, &w.IssuePlan); err != nil {
		return Outcome{}, err
	}
	if err := domain.ValidateIssueUpdateStatus(p.Status); err != nil {
		return Outcome{}, err
	}
	status := protocol.WorkItemStatus(p.Status)
	if status == "" {
		status = w.Status
	}
	if err := validateIssueDispositions(ctx, tx.Q(), w, p.IssuePlan, p.TaskDispositions); err != nil {
		return Outcome{}, err
	}
	old := w.Revision
	changed := domain.IssuePlanChanged(w.IssuePlan, p.IssuePlan) || status != w.Status || len(p.CompletionReviews) > 0
	if err := claimIssueSource(ctx, x, tx, w.WorkItemID, p.SourceMessageID, p.CommitSeq); err != nil {
		return Outcome{}, err
	}
	if !changed {
		return Outcome{Result: w, NoEffect: true}, nil
	}
	w.IssuePlan = p.IssuePlan
	w.Status = status
	w.Revision++
	w.UpdatedAt = x.Now.UnixMilli()
	freezeIssueCurrentEvidence(&w, x.Env.RequestID)
	if err := acknowledgeCompletionReviews(ctx, tx, w, p.CompletionReviews); err != nil {
		return Outcome{}, err
	}
	return saveIssue(ctx, tx, w, old, "work_item.updated", p)
}

// CloseIssue records an explicit endpoint only after all execution is settled.
type CloseIssue struct{}

func (CloseIssue) Operation() string { return "work_items.close" }
func (CloseIssue) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, planners...)
}
func (CloseIssue) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.CloseWorkItemPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	w, err := issueForWrite(ctx, x, tx.Q())
	if err != nil {
		return Outcome{}, err
	}
	if w.Status == "closed" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "work item is already closed")
	}
	if err := domain.ValidateIssueClosure(w, p.Closure); err != nil {
		return Outcome{}, err
	}
	if err := issueCloseGuard(ctx, tx.Q(), w.WorkItemID); err != nil {
		return Outcome{}, err
	}
	if err := validateIssueEvidenceReferences(ctx, x, tx.Q(), w, p.Closure.Evidence, &w, "closure.evidence"); err != nil {
		return Outcome{}, err
	}
	if err := claimIssueSource(ctx, x, tx, w.WorkItemID, p.SourceMessageID, p.CommitSeq); err != nil {
		return Outcome{}, err
	}
	old := w.Revision
	w.Revision++
	w.Status = "closed"
	w.Closure = &p.Closure
	w.UpdatedAt = x.Now.UnixMilli()
	freezeIssueCurrentEvidence(&w, x.Env.RequestID)
	if err := cancelDeliveryQueries(ctx, tx, w); err != nil {
		return Outcome{}, err
	}
	return saveIssue(ctx, tx, w, old, "work_item.closed", p)
}

// ReopenIssue resumes responsibility without changing any old task state.
type ReopenIssue struct{}

func (ReopenIssue) Operation() string { return "work_items.reopen" }
func (ReopenIssue) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, planners...)
}
func (ReopenIssue) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.ReopenWorkItemPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	w, err := issueForWrite(ctx, x, tx.Q())
	if err != nil {
		return Outcome{}, err
	}
	if w.Status != "closed" || strings.TrimSpace(p.Reason) == "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "reopening requires a closed work item and reason")
	}
	if err := claimIssueSource(ctx, x, tx, w.WorkItemID, p.SourceMessageID, p.CommitSeq); err != nil {
		return Outcome{}, err
	}
	old := w.Revision
	w.Revision++
	w.Status = "open"
	w.Closure = nil
	w.UpdatedAt = x.Now.UnixMilli()
	return saveIssue(ctx, tx, w, old, "work_item.reopened", p)
}

// LinkIssueTask associates an existing task, including a shared publishing task.
type LinkIssueTask struct{}

func (LinkIssueTask) Operation() string { return "work_items.link_task" }
func (LinkIssueTask) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, planners...)
}
func (LinkIssueTask) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.LinkWorkItemTaskPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if err := store.RejectCanonicalLegacyIssueWrite(ctx, tx.Q(), x.Env.Target.ID); err != nil {
		return Outcome{}, err
	}
	revision, err := requireRevision(x.Env)
	if err != nil {
		return Outcome{}, err
	}
	if err := claimIssueSource(ctx, x, tx, x.Env.Target.ID, p.SourceMessageID, p.CommitSeq); err != nil {
		return Outcome{}, err
	}
	w, events, err := BindIssueTask(ctx, x, tx, x.Env.Target.ID, revision, p.TaskID, p.StageIDs)
	return Outcome{Result: w, Events: events, NoEffect: len(events) == 0}, err
}

// BindIssueTask is also called from task creation inside the same transaction.
func BindIssueTask(ctx context.Context, x *Exec, tx *store.Tx, itemID string, revision int64, taskID string, stageIDs []string) (protocol.WorkItem, []Event, error) {
	w, err := store.GetIssue(ctx, tx.Q(), itemID)
	if err != nil {
		return w, nil, errNotFound(err, "work item does not exist")
	}
	if err := store.RejectCanonicalLegacyIssueWrite(ctx, tx.Q(), w.WorkItemID); err != nil {
		return w, nil, err
	}
	if w.Revision != revision {
		return w, nil, protocol.Errorf(protocol.CodeStaleRevision, "work item revision changed")
	}
	if w.Status == "closed" {
		return w, nil, protocol.Errorf(protocol.CodeInvalidTransition, "reopen work item before linking a task")
	}
	if len(stageIDs) == 0 {
		return w, nil, missingIssueStageIDs(w.WorkItemID, stageIDs)
	}
	task, spec, err := loadTask(ctx, tx.Q(), taskID)
	if err != nil {
		return w, nil, errNotFound(err, "task does not exist")
	}
	changed := false
	for _, id := range stageIDs {
		found := false
		for i := range w.Stages {
			if w.Stages[i].StageID != id {
				continue
			}
			found = true
			if !slices.Contains(w.Stages[i].TaskIDs, taskID) {
				if !task.Lifecycle.Terminal() && !domain.IssueStageAcceptsWork(w.IssuePlan, id) {
					return w, nil, protocol.Errorf(protocol.CodeInvalidTransition, "new work requires an unfinished stage")
				}
				w.Stages[i].TaskIDs = append(w.Stages[i].TaskIDs, taskID)
				changed = true
			}
		}
		if !found {
			return w, nil, protocol.Errorf(protocol.CodeInvalidRequest, "stage %s does not exist", id)
		}
	}
	if !changed {
		return w, nil, nil
	}
	if w.DefaultWorkspaceTaskID == "" && len(spec.Workspace.Mounts) > 0 {
		owner, err := store.GetTaskIssueOwner(ctx, tx.Q(), taskID)
		if err != nil {
			return w, nil, err
		}
		if owner == w.WorkItemID {
			w.DefaultWorkspaceTaskID = taskID
		}
	}
	w.Revision++
	w.UpdatedAt = x.Now.UnixMilli()
	out, err := saveIssue(ctx, tx, w, revision, "work_item.task_linked", map[string]any{"task_id": taskID, "stage_ids": stageIDs})
	return w, out.Events, err
}
func issueForWrite(ctx context.Context, x *Exec, q store.Querier) (protocol.WorkItem, error) {
	revision, err := requireRevision(x.Env)
	if err != nil {
		return protocol.WorkItem{}, err
	}
	w, err := store.GetIssue(ctx, q, x.Env.Target.ID)
	if err != nil {
		return w, errNotFound(err, "work item does not exist")
	}
	if err := store.RejectCanonicalLegacyIssueWrite(ctx, q, w.WorkItemID); err != nil {
		return w, err
	}
	if revision != w.Revision {
		return w, protocol.Errorf(protocol.CodeStaleRevision, "work item revision changed")
	}
	return w, nil
}
func saveIssue(ctx context.Context, tx *store.Tx, w protocol.WorkItem, expected int64, kind string, reason any) (Outcome, error) {
	if err := validateIssueTaskDependencies(ctx, tx.Q(), w); err != nil {
		return Outcome{}, err
	}
	if err := tx.SaveIssue(ctx, w, expected, reason); err != nil {
		return Outcome{}, err
	}
	if err := tx.EnsureCompletionReviews(ctx, w.WorkItemID); err != nil {
		return Outcome{}, err
	}
	if err := settleCompletionReviews(ctx, tx, w); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: w, Events: []Event{{ObjectType: "work_item", ObjectID: w.WorkItemID, ObjectVersion: w.Revision, Kind: kind, Body: map[string]any{"revision": w.Revision, "status": w.Status}}}}, nil
}
func claimIssueSource(ctx context.Context, x *Exec, tx *store.Tx, id, source string, seq int64) error {
	if err := claimPlanCommit(ctx, x, tx, source, seq); err != nil {
		return err
	}
	return bindSource(ctx, tx, source, "work_item", id)
}

func validateIssueReferences(ctx context.Context, x *Exec, q store.Querier, w protocol.WorkItem, previous *protocol.WorkItem) error {
	for i, s := range w.Stages {
		for _, id := range s.TaskIDs {
			task, err := store.GetTask(ctx, q, id)
			if err != nil {
				return errNotFound(err, "stage task does not exist")
			}
			existed := false
			if previous != nil {
				for _, stage := range previous.Stages {
					if stage.StageID == s.StageID && slices.Contains(stage.TaskIDs, id) {
						existed = true
					}
				}
			}
			if !existed && !task.Lifecycle.Terminal() && !domain.IssueStageAcceptsWork(w.IssuePlan, s.StageID) {
				return protocol.Errorf(protocol.CodeInvalidTransition, "new work requires an unfinished stage and containing groups")
			}

		}
		if err := validateIssueEvidenceReferences(ctx, x, q, w, s.Evidence, previous, fmt.Sprintf("stages[%d].evidence", i)); err != nil {
			return err
		}
	}
	for i, f := range w.FollowOns {
		if err := validateIssueEvidenceReferences(ctx, x, q, w, f.Evidence, previous, fmt.Sprintf("follow_ons[%d].evidence", i)); err != nil {
			return err
		}
	}
	if w.DefaultWorkspaceTaskID != "" {
		if previous == nil || w.DefaultWorkspaceTaskID != previous.DefaultWorkspaceTaskID {
			if err := checkDefaultWorkspaceOwner(ctx, q, w.WorkItemID, w.DefaultWorkspaceTaskID); err != nil {
				return err
			}
		}
		_, spec, err := loadTask(ctx, q, w.DefaultWorkspaceTaskID)
		if err != nil {
			return errNotFound(err, "default workspace source must reference an existing task")
		}
		if len(spec.Workspace.Mounts) == 0 {
			return protocol.Errorf(protocol.CodeInvalidRequest, "default workspace source must be a repository task")
		}
		resource, err := store.GetWorkspaceByTask(ctx, q, w.DefaultWorkspaceTaskID)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if resource.Status == store.WorkspaceRemoved || resource.Status == store.WorkspaceRemoving {
			return protocol.Errorf(protocol.CodeInvalidTransition, "default workspace source is being removed")
		}
	}
	return nil
}

func validateIssueEvidenceReferences(ctx context.Context, x *Exec, q store.Querier, w protocol.WorkItem, evidence []protocol.IssueEvidence, previous *protocol.WorkItem, field string) error {
	if len(evidence) == 0 {
		return nil
	}
	if err := domain.ValidateIssueEvidenceAt(evidence, field); err != nil {
		return err
	}
	for i, e := range evidence {
		if old, ok := savedIssueEvidence(previous, e); ok {
			evidence[i] = old
			continue
		}
		evidence[i].Observation = nil
		evidence[i].Upgrade = nil
		var valid bool
		switch e.Kind {
		case "owner_decision":
			if e.Reference == "current_request" && x.Principal.Kind == protocol.PrincipalOwner {
				valid = true
				break
			}
			if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_messages WHERE message_id=? AND author='owner') OR EXISTS(SELECT 1 FROM decisions WHERE decision_id=? AND status='answered') OR EXISTS(SELECT 1 FROM request_records WHERE request_id=? AND domain='owner' AND status='succeeded' AND operation LIKE 'work_items.%') OR EXISTS(SELECT 1 FROM work_item_choices WHERE choice_id=? AND work_item_id=? AND status='answered')`, e.Reference, e.Reference, e.Reference, e.Reference, w.WorkItemID).Scan(&valid); err != nil {
				return err
			}
		case "acceptance":
			var acceptedTask string
			err := q.QueryRowContext(ctx, `SELECT task_id FROM acceptances WHERE acceptance_id=? AND verdict='passed'`, e.Reference).Scan(&acceptedTask)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			for _, stage := range w.Stages {
				if slices.Contains(stage.TaskIDs, acceptedTask) {
					valid = true
				}
			}
			var canonical bool
			if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issue_controls WHERE issue_id=?)`, w.WorkItemID).Scan(&canonical); err != nil {
				return err
			}
			if canonical {
				if err := q.QueryRowContext(ctx, `WITH RECURSIVE descendants(id) AS (SELECT ? UNION ALL SELECT c.issue_id FROM issue_controls c JOIN descendants d ON c.parent_issue_id=d.id) SELECT EXISTS(SELECT 1 FROM task_work_item_owners o JOIN descendants d ON d.id=o.work_item_id JOIN tasks t ON t.task_id=o.task_id JOIN acceptances a ON a.task_id=t.task_id WHERE a.acceptance_id=? AND a.verdict='passed' AND t.lifecycle='completed' AND a.spec_version=t.spec_version AND a.artifact_id=t.current_artifact_id)`, w.WorkItemID, e.Reference).Scan(&valid); err != nil {
					return err
				}
			}

		case "upgrade":
			snapshot, err := workItemUpgradeEvidence(ctx, x, q, e.Reference)
			if err != nil {
				return err
			}
			evidence[i].Upgrade = snapshot
			valid = true
		case "delivery":
			// External references are verified by the observation subsystem. A plan
			// reference alone is not proof that the provider was queried successfully.
			row, err := store.GetDeliveryObservation(ctx, q, w.WorkItemID, e.Reference)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
			if err == nil && row.Observation.CheckedAt > 0 && row.Observation.State != "" {
				valid = true
				snapshot := row.Observation
				evidence[i].Observation = &snapshot
			}

		}
		if !valid {
			if e.Kind == "acceptance" {
				return protocol.Errorf(protocol.CodeInvalidRequest, "%s[%d].reference must be the acceptance_id of a passed acceptance for a linked task; read tasks_read.acceptances or artifacts_read.acceptances (artifact_id is not acceptance_id)", field, i)
			}
			return protocol.Errorf(protocol.CodeInvalidRequest, "evidence reference %s has no matching verified fact", e.Reference)
		}
	}
	return nil
}

func validateIssueDispositions(ctx context.Context, q store.Querier, item protocol.WorkItem, next protocol.IssuePlan, dispositions []protocol.IssueTaskDisposition) error {
	affected := map[string]bool{}
	ids, err := store.IssueTaskIDs(ctx, q, item.WorkItemID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if domain.IssueTaskPlanChanged(item.IssuePlan, next, id) {
			affected[id] = true
		}
	}

	byTask := map[string]protocol.IssueTaskDisposition{}
	dispositionIndices := map[string]int{}
	for i, d := range dispositions {
		if _, ok := byTask[d.TaskID]; ok {
			return protocol.Errorf(protocol.CodeInvalidRequest, "duplicate task disposition")
		}
		byTask[d.TaskID] = d
		dispositionIndices[d.TaskID] = i
	}
	liveRunQuery := `SELECT EXISTS(SELECT 1 FROM runs WHERE task_id=? AND observed_state NOT IN ('finished','failed','interrupted'))`
	var canonical bool
	if len(affected) > 0 {
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issue_controls WHERE issue_id=?)`, item.WorkItemID).Scan(&canonical); err != nil {
			return err
		}
		if canonical {
			liveRunQuery = `SELECT EXISTS(SELECT 1 FROM runs WHERE task_id=? AND observed_state NOT IN ('finished','failed'))`
		}
	}
	for id := range affected {
		task, err := store.GetTask(ctx, q, id)
		if err != nil {
			return err
		}
		if domain.IssueTaskRetired(item.IssuePlan, next, id) {
			var live bool
			if err := q.QueryRowContext(ctx, liveRunQuery, id).Scan(&live); err != nil {
				return err
			}
			if live {
				return protocol.Errorf(protocol.CodeInvalidTransition, "confirm task runs stopped before retiring or cancelling their stage")
			}
		}
		if task.Lifecycle.Terminal() {
			continue
		}
		d, ok := byTask[id]
		if ok {
			if err := domain.ValidateIssueDispositionAction(d.Action, fmt.Sprintf("task_dispositions[%d].action", dispositionIndices[id])); err != nil {
				return err
			}
		}
		if !ok || d.Reason == "" || d.SpecVersion != task.SpecVersion {
			return protocol.Errorf(protocol.CodeInvalidRequest, "plan change requires current spec disposition for active task %s", id)
		}
		if domain.IssueTaskRetired(item.IssuePlan, next, id) && d.Action != "stopped" {
			return protocol.Errorf(protocol.CodeInvalidTransition, "stop active task agreements before retiring or cancelling their stage")
		}
		if d.Action == "updated" {
			revision, err := issueTaskAgreementRevision(ctx, q, item, id)
			if err != nil {
				return err
			}
			var updated bool
			if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE object_type='task' AND object_id=? AND kind='task.spec_updated' AND json_extract(body_json,'$.spec_version')=? AND event_seq > COALESCE((SELECT MAX(event_seq) FROM events WHERE object_type IN ('work_item','issue') AND object_id=? AND object_version=?),0))`, id, task.SpecVersion, item.WorkItemID, revision).Scan(&updated); err != nil {
				return err
			}
			if !updated {
				return protocol.Errorf(protocol.CodeInvalidTransition, "update the task agreement explicitly before marking it updated")
			}
		}
		if d.Action == "stopped" {
			var live bool
			if err := q.QueryRowContext(ctx, liveRunQuery, id).Scan(&live); err != nil {
				return err
			}
			if task.ControlMode != protocol.ControlPaused || live {
				return protocol.Errorf(protocol.CodeInvalidTransition, "pause the task and confirm all runs stopped before marking it stopped")
			}
		}
	}
	return nil
}
func issueCloseGuard(ctx context.Context, q store.Querier, id string) error {
	// A canonical task referenced by a legacy plan is evidence, not that
	// plan's execution responsibility. Reuse the same membership as reads.
	members, err := store.IssueTaskIDs(ctx, q, id)
	if err != nil {
		return err
	}
	membership, err := json.Marshal(members)
	if err != nil {
		return err
	}
	var blocked bool
	err = q.QueryRowContext(ctx, `WITH linked AS (SELECT value AS task_id FROM json_each(?)) SELECT
 EXISTS(SELECT 1 FROM tasks WHERE task_id IN linked AND lifecycle NOT IN ('completed','cancelled')) OR
 EXISTS(SELECT 1 FROM runs WHERE task_id IN linked AND observed_state NOT IN ('finished','failed','interrupted')) OR
 EXISTS(SELECT 1 FROM decisions WHERE task_id IN linked AND status='open') OR
 EXISTS(SELECT 1 FROM work_item_choices WHERE work_item_id=? AND status='open') OR
 EXISTS(SELECT 1 FROM plan_actions a JOIN request_records r ON r.request_id=a.created_request_id WHERE a.task_id IN linked AND r.status IN ('unknown','accepted')) OR
 EXISTS(SELECT 1 FROM events e JOIN request_records r ON r.request_id=e.request_id WHERE r.status IN ('unknown','accepted') AND ((e.object_type='task' AND e.object_id IN linked) OR (e.object_type='work_item' AND e.object_id=?)))`, string(membership), id, id).Scan(&blocked)
	if err != nil {
		return err
	}
	if blocked {
		return protocol.Errorf(protocol.CodeInvalidTransition, "active tasks, runs, open decisions, unanswered choices or uncertain operations prevent closure")
	}
	return nil
}

// Freeze direct owner evidence to the durable request identity before persisting.
func freezeIssueCurrentEvidence(w *protocol.WorkItem, requestID string) {
	freeze := func(evidence []protocol.IssueEvidence) {
		for i := range evidence {
			if evidence[i].Kind == "owner_decision" && evidence[i].Reference == "current_request" {
				evidence[i].Reference = requestID
			}
		}
	}
	for i := range w.Stages {
		freeze(w.Stages[i].Evidence)
	}
	for i := range w.FollowOns {
		freeze(w.FollowOns[i].Evidence)
	}
	if w.Closure != nil {
		freeze(w.Closure.Evidence)
	}
}

// The evidence identity excludes caller-supplied snapshots: once recorded,
// its facts stay fixed even if the provider is queried again or unavailable.
func savedIssueEvidence(previous *protocol.WorkItem, wanted protocol.IssueEvidence) (protocol.IssueEvidence, bool) {
	if previous == nil {
		return protocol.IssueEvidence{}, false
	}
	groups := [][]protocol.IssueEvidence{}
	for _, stage := range previous.Stages {
		groups = append(groups, stage.Evidence)
	}
	for _, follow := range previous.FollowOns {
		groups = append(groups, follow.Evidence)
	}
	if previous.Closure != nil {
		groups = append(groups, previous.Closure.Evidence)
	}
	for _, group := range groups {
		for _, e := range group {
			if e.Kind == wanted.Kind && e.Reference == wanted.Reference && e.Summary == wanted.Summary {
				return e, true
			}
		}
	}
	return protocol.IssueEvidence{}, false
}

// Ignore revisions such as title edits when proving an explicit task update.
func issueTaskAgreementRevision(ctx context.Context, q store.Querier, item protocol.WorkItem, taskID string) (int64, error) {
	current := item
	for current.Revision > 1 {
		previous, err := store.GetIssueRevision(ctx, q, item.WorkItemID, current.Revision-1)
		if err != nil {
			return 0, err
		}
		if domain.IssueTaskPlanChanged(previous.IssuePlan, current.IssuePlan, taskID) {
			break
		}
		current = previous
	}
	return current.Revision, nil
}
