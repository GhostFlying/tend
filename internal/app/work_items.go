package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// CreateWorkItem starts an open responsibility without automatically creating work.
type CreateWorkItem struct{}

func (CreateWorkItem) Operation() string { return "work_items.create" }
func (CreateWorkItem) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, planners...)
}
func (CreateWorkItem) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
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
		p.Stages = []protocol.WorkItemStage{}
	}
	if err := domain.ValidateWorkItem(p.WorkItemPlan, nil); err != nil {
		return Outcome{}, err
	}
	w := protocol.WorkItem{WorkItemPlan: p.WorkItemPlan, WorkItemID: x.IDs.New(protocol.PrefixWorkItem), Revision: 1, Status: "open", CreatedAt: x.Now.UnixMilli(), UpdatedAt: x.Now.UnixMilli()}
	if err := validateWorkItemReferences(ctx, x, tx.Q(), w, nil); err != nil {
		return Outcome{}, err
	}
	if err := claimWorkItemSource(ctx, x, tx, w.WorkItemID, p.SourceMessageID, p.CommitSeq); err != nil {
		return Outcome{}, err
	}
	freezeWorkItemCurrentEvidence(&w, x.Env.RequestID)
	return saveWorkItem(ctx, tx, w, 0, "work_item.created", p)
}

// UpdateWorkItem revises a plan while preserving execution agreements and history.
type UpdateWorkItem struct{}

func (UpdateWorkItem) Operation() string { return "work_items.update" }
func (UpdateWorkItem) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, planners...)
}
func (UpdateWorkItem) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.UpdateWorkItemPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	w, err := workItemForWrite(ctx, x, tx.Q())
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
		p.Stages = []protocol.WorkItemStage{}
	}
	if err := domain.ValidateWorkItem(p.WorkItemPlan, nil); err != nil {
		return Outcome{}, err
	}
	candidate := w
	candidate.WorkItemPlan = p.WorkItemPlan
	if err := validateWorkItemReferences(ctx, x, tx.Q(), candidate, &w); err != nil {
		return Outcome{}, err
	}
	p.WorkItemPlan = candidate.WorkItemPlan
	if err := domain.ValidateWorkItem(p.WorkItemPlan, &w.WorkItemPlan); err != nil {
		return Outcome{}, err
	}
	if err := domain.ValidateWorkItemUpdateStatus(p.Status); err != nil {
		return Outcome{}, err
	}
	status := protocol.WorkItemStatus(p.Status)
	if status == "" {
		status = w.Status
	}
	if err := validateWorkItemDispositions(ctx, tx.Q(), w, p.WorkItemPlan, p.TaskDispositions); err != nil {
		return Outcome{}, err
	}
	old := w.Revision
	changed := domain.WorkItemPlanChanged(w.WorkItemPlan, p.WorkItemPlan) || status != w.Status || len(p.CompletionReviews) > 0
	if err := claimWorkItemSource(ctx, x, tx, w.WorkItemID, p.SourceMessageID, p.CommitSeq); err != nil {
		return Outcome{}, err
	}
	if !changed {
		return Outcome{Result: w, NoEffect: true}, nil
	}
	w.WorkItemPlan = p.WorkItemPlan
	w.Status = status
	w.Revision++
	w.UpdatedAt = x.Now.UnixMilli()
	freezeWorkItemCurrentEvidence(&w, x.Env.RequestID)
	if err := acknowledgeCompletionReviews(ctx, tx, w, p.CompletionReviews); err != nil {
		return Outcome{}, err
	}
	return saveWorkItem(ctx, tx, w, old, "work_item.updated", p)
}

// CloseWorkItem records an explicit endpoint only after all execution is settled.
type CloseWorkItem struct{}

func (CloseWorkItem) Operation() string { return "work_items.close" }
func (CloseWorkItem) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, planners...)
}
func (CloseWorkItem) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.CloseWorkItemPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	w, err := workItemForWrite(ctx, x, tx.Q())
	if err != nil {
		return Outcome{}, err
	}
	if w.Status == "closed" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "work item is already closed")
	}
	if err := domain.ValidateWorkItemClosure(w, p.Closure); err != nil {
		return Outcome{}, err
	}
	if err := workItemCloseGuard(ctx, tx.Q(), w.WorkItemID); err != nil {
		return Outcome{}, err
	}
	if err := validateWorkItemEvidenceReferences(ctx, x, tx.Q(), w, p.Closure.Evidence, &w, "closure.evidence"); err != nil {
		return Outcome{}, err
	}
	if err := claimWorkItemSource(ctx, x, tx, w.WorkItemID, p.SourceMessageID, p.CommitSeq); err != nil {
		return Outcome{}, err
	}
	old := w.Revision
	w.Revision++
	w.Status = "closed"
	w.Closure = &p.Closure
	w.UpdatedAt = x.Now.UnixMilli()
	freezeWorkItemCurrentEvidence(&w, x.Env.RequestID)
	if err := cancelDeliveryQueries(ctx, tx, w); err != nil {
		return Outcome{}, err
	}
	return saveWorkItem(ctx, tx, w, old, "work_item.closed", p)
}

// ReopenWorkItem resumes responsibility without changing any old task state.
type ReopenWorkItem struct{}

func (ReopenWorkItem) Operation() string { return "work_items.reopen" }
func (ReopenWorkItem) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, planners...)
}
func (ReopenWorkItem) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.ReopenWorkItemPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	w, err := workItemForWrite(ctx, x, tx.Q())
	if err != nil {
		return Outcome{}, err
	}
	if w.Status != "closed" || strings.TrimSpace(p.Reason) == "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "reopening requires a closed work item and reason")
	}
	if err := claimWorkItemSource(ctx, x, tx, w.WorkItemID, p.SourceMessageID, p.CommitSeq); err != nil {
		return Outcome{}, err
	}
	old := w.Revision
	w.Revision++
	w.Status = "open"
	w.Closure = nil
	w.UpdatedAt = x.Now.UnixMilli()
	return saveWorkItem(ctx, tx, w, old, "work_item.reopened", p)
}

// LinkWorkItemTask associates an existing task, including a shared publishing task.
type LinkWorkItemTask struct{}

func (LinkWorkItemTask) Operation() string { return "work_items.link_task" }
func (LinkWorkItemTask) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, planners...)
}
func (LinkWorkItemTask) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.LinkWorkItemTaskPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	revision, err := requireRevision(x.Env)
	if err != nil {
		return Outcome{}, err
	}
	if err := claimWorkItemSource(ctx, x, tx, x.Env.Target.ID, p.SourceMessageID, p.CommitSeq); err != nil {
		return Outcome{}, err
	}
	w, events, err := BindWorkItemTask(ctx, x, tx, x.Env.Target.ID, revision, p.TaskID, p.StageIDs)
	return Outcome{Result: w, Events: events, NoEffect: len(events) == 0}, err
}

// BindWorkItemTask is also called from task creation inside the same transaction.
func BindWorkItemTask(ctx context.Context, x *Exec, tx *store.Tx, itemID string, revision int64, taskID string, stageIDs []string) (protocol.WorkItem, []Event, error) {
	w, err := store.GetWorkItem(ctx, tx.Q(), itemID)
	if err != nil {
		return w, nil, errNotFound(err, "work item does not exist")
	}
	if w.Revision != revision {
		return w, nil, protocol.Errorf(protocol.CodeStaleRevision, "work item revision changed")
	}
	if w.Status == "closed" {
		return w, nil, protocol.Errorf(protocol.CodeInvalidTransition, "reopen work item before linking a task")
	}
	if len(stageIDs) == 0 {
		return w, nil, missingWorkItemStageIDs(w.WorkItemID, stageIDs)
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
				if !task.Lifecycle.Terminal() && !domain.WorkItemStageAcceptsWork(w.WorkItemPlan, id) {
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
		owner, err := store.GetTaskWorkItemOwner(ctx, tx.Q(), taskID)
		if err != nil {
			return w, nil, err
		}
		if owner == w.WorkItemID {
			w.DefaultWorkspaceTaskID = taskID
		}
	}
	w.Revision++
	w.UpdatedAt = x.Now.UnixMilli()
	out, err := saveWorkItem(ctx, tx, w, revision, "work_item.task_linked", map[string]any{"task_id": taskID, "stage_ids": stageIDs})
	return w, out.Events, err
}
func workItemForWrite(ctx context.Context, x *Exec, q store.Querier) (protocol.WorkItem, error) {
	revision, err := requireRevision(x.Env)
	if err != nil {
		return protocol.WorkItem{}, err
	}
	w, err := store.GetWorkItem(ctx, q, x.Env.Target.ID)
	if err != nil {
		return w, errNotFound(err, "work item does not exist")
	}
	if revision != w.Revision {
		return w, protocol.Errorf(protocol.CodeStaleRevision, "work item revision changed")
	}
	return w, nil
}
func saveWorkItem(ctx context.Context, tx *store.Tx, w protocol.WorkItem, expected int64, kind string, reason any) (Outcome, error) {
	if err := validateWorkItemTaskDependencies(ctx, tx.Q(), w); err != nil {
		return Outcome{}, err
	}
	if err := tx.SaveWorkItem(ctx, w, expected, reason); err != nil {
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
func claimWorkItemSource(ctx context.Context, x *Exec, tx *store.Tx, id, source string, seq int64) error {
	if err := claimPlanCommit(ctx, x, tx, source, seq); err != nil {
		return err
	}
	return bindSource(ctx, tx, source, "work_item", id)
}

func validateWorkItemReferences(ctx context.Context, x *Exec, q store.Querier, w protocol.WorkItem, previous *protocol.WorkItem) error {
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
			if !existed && !task.Lifecycle.Terminal() && !domain.WorkItemStageAcceptsWork(w.WorkItemPlan, s.StageID) {
				return protocol.Errorf(protocol.CodeInvalidTransition, "new work requires an unfinished stage and containing groups")
			}

		}
		if err := validateWorkItemEvidenceReferences(ctx, x, q, w, s.Evidence, previous, fmt.Sprintf("stages[%d].evidence", i)); err != nil {
			return err
		}
	}
	for i, f := range w.FollowOns {
		if err := validateWorkItemEvidenceReferences(ctx, x, q, w, f.Evidence, previous, fmt.Sprintf("follow_ons[%d].evidence", i)); err != nil {
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

func validateWorkItemEvidenceReferences(ctx context.Context, x *Exec, q store.Querier, w protocol.WorkItem, evidence []protocol.WorkItemEvidence, previous *protocol.WorkItem, field string) error {
	if len(evidence) == 0 {
		return nil
	}
	if err := domain.ValidateWorkItemEvidenceAt(evidence, field); err != nil {
		return err
	}
	for i, e := range evidence {
		if old, ok := savedWorkItemEvidence(previous, e); ok {
			evidence[i] = old
			continue
		}
		evidence[i].Observation = nil
		var valid bool
		switch e.Kind {
		case "owner_decision":
			if e.Reference == "current_request" && x.Principal.Kind == protocol.PrincipalOwner {
				valid = true
				break
			}
			if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_messages WHERE message_id=? AND author='owner') OR EXISTS(SELECT 1 FROM decisions WHERE decision_id=? AND status='answered') OR EXISTS(SELECT 1 FROM request_records WHERE request_id=? AND domain='owner' AND status='succeeded' AND operation LIKE 'work_items.%')`, e.Reference, e.Reference, e.Reference).Scan(&valid); err != nil {
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

func validateWorkItemDispositions(ctx context.Context, q store.Querier, item protocol.WorkItem, next protocol.WorkItemPlan, dispositions []protocol.WorkItemTaskDisposition) error {
	affected := map[string]bool{}
	for _, stage := range item.Stages {
		for _, id := range stage.TaskIDs {
			if domain.WorkItemTaskPlanChanged(item.WorkItemPlan, next, id) {
				affected[id] = true
			}
		}
	}
	byTask := map[string]protocol.WorkItemTaskDisposition{}
	dispositionIndices := map[string]int{}
	for i, d := range dispositions {
		if _, ok := byTask[d.TaskID]; ok {
			return protocol.Errorf(protocol.CodeInvalidRequest, "duplicate task disposition")
		}
		byTask[d.TaskID] = d
		dispositionIndices[d.TaskID] = i
	}
	for id := range affected {
		task, err := store.GetTask(ctx, q, id)
		if err != nil {
			return err
		}
		if domain.WorkItemTaskRetired(item.WorkItemPlan, next, id) {
			var live bool
			if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE task_id=? AND observed_state NOT IN ('finished','failed','interrupted'))`, id).Scan(&live); err != nil {
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
			if err := domain.ValidateWorkItemDispositionAction(d.Action, fmt.Sprintf("task_dispositions[%d].action", dispositionIndices[id])); err != nil {
				return err
			}
		}
		if !ok || d.Reason == "" || d.SpecVersion != task.SpecVersion {
			return protocol.Errorf(protocol.CodeInvalidRequest, "plan change requires current spec disposition for active task %s", id)
		}
		if domain.WorkItemTaskRetired(item.WorkItemPlan, next, id) && d.Action != "stopped" {
			return protocol.Errorf(protocol.CodeInvalidTransition, "stop active task agreements before retiring or cancelling their stage")
		}
		if d.Action == "updated" {
			revision, err := workItemTaskAgreementRevision(ctx, q, item, id)
			if err != nil {
				return err
			}
			var updated bool
			if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE object_type='task' AND object_id=? AND kind='task.spec_updated' AND json_extract(body_json,'$.spec_version')=? AND event_seq > COALESCE((SELECT MAX(event_seq) FROM events WHERE object_type='work_item' AND object_id=? AND object_version=?),0))`, id, task.SpecVersion, item.WorkItemID, revision).Scan(&updated); err != nil {
				return err
			}
			if !updated {
				return protocol.Errorf(protocol.CodeInvalidTransition, "update the task agreement explicitly before marking it updated")
			}
		}
		if d.Action == "stopped" {
			var live bool
			if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE task_id=? AND observed_state NOT IN ('finished','failed','interrupted'))`, id).Scan(&live); err != nil {
				return err
			}
			if task.ControlMode != protocol.ControlPaused || live {
				return protocol.Errorf(protocol.CodeInvalidTransition, "pause the task and confirm all runs stopped before marking it stopped")
			}
		}
	}
	return nil
}
func workItemCloseGuard(ctx context.Context, q store.Querier, id string) error {
	var blocked bool
	err := q.QueryRowContext(ctx, `WITH linked AS (SELECT DISTINCT task_id FROM work_item_task_links WHERE work_item_id=?) SELECT
 EXISTS(SELECT 1 FROM tasks WHERE task_id IN linked AND lifecycle NOT IN ('completed','cancelled')) OR
 EXISTS(SELECT 1 FROM runs WHERE task_id IN linked AND observed_state NOT IN ('finished','failed','interrupted')) OR
 EXISTS(SELECT 1 FROM decisions WHERE task_id IN linked AND status='open') OR
 EXISTS(SELECT 1 FROM plan_actions a JOIN request_records r ON r.request_id=a.created_request_id WHERE a.task_id IN linked AND r.status IN ('unknown','accepted')) OR
 EXISTS(SELECT 1 FROM events e JOIN request_records r ON r.request_id=e.request_id WHERE r.status IN ('unknown','accepted') AND ((e.object_type='task' AND e.object_id IN linked) OR (e.object_type='work_item' AND e.object_id=?)))`, id, id).Scan(&blocked)
	if err != nil {
		return err
	}
	if blocked {
		return protocol.Errorf(protocol.CodeInvalidTransition, "active tasks, runs, open decisions or uncertain operations prevent closure")
	}
	return nil
}

// Freeze direct owner evidence to the durable request identity before persisting.
func freezeWorkItemCurrentEvidence(w *protocol.WorkItem, requestID string) {
	freeze := func(evidence []protocol.WorkItemEvidence) {
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
func savedWorkItemEvidence(previous *protocol.WorkItem, wanted protocol.WorkItemEvidence) (protocol.WorkItemEvidence, bool) {
	if previous == nil {
		return protocol.WorkItemEvidence{}, false
	}
	groups := [][]protocol.WorkItemEvidence{}
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
	return protocol.WorkItemEvidence{}, false
}

// Ignore revisions such as title edits when proving an explicit task update.
func workItemTaskAgreementRevision(ctx context.Context, q store.Querier, item protocol.WorkItem, taskID string) (int64, error) {
	current := item
	for current.Revision > 1 {
		previous, err := store.GetWorkItemRevision(ctx, q, item.WorkItemID, current.Revision-1)
		if err != nil {
			return 0, err
		}
		if domain.WorkItemTaskPlanChanged(previous.WorkItemPlan, current.WorkItemPlan, taskID) {
			break
		}
		current = previous
	}
	return current.Revision, nil
}
