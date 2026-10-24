package domain

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

func workInvalid(format string, args ...any) error {
	return protocol.Errorf(protocol.CodeInvalidRequest, format, args...)
}

// ValidateWorkItem validates a plan and preserves stable ledger identity.
func ValidateWorkItem(next protocol.WorkItemPlan, previous *protocol.WorkItemPlan) error {
	if strings.TrimSpace(next.Title) == "" || strings.TrimSpace(next.Intent) == "" || next.Project == "" {
		return workInvalid("title, intent and project are required")
	}
	for i, f := range next.Background {
		if err := validateWorkItemEnum(fmt.Sprintf("background[%d].kind", i), f.Kind, protocol.SharedFactKindValues()); err != nil {
			return err
		}
		if strings.TrimSpace(f.Text) == "" || strings.TrimSpace(f.Source) == "" {
			return workInvalid("background[%d] requires text and source", i)
		}
	}
	stages := map[string]protocol.WorkItemStage{}
	for i, s := range next.Stages {
		if s.StageID == "" || s.Title == "" {
			return workInvalid("stage_id and title are required")
		}
		if _, ok := stages[s.StageID]; ok {
			return workInvalid("duplicate stage_id %s", s.StageID)
		}
		if err := validateWorkItemEnum(fmt.Sprintf("stages[%d].status", i), s.Status, protocol.WorkItemStageStatusValues()); err != nil {
			return err
		}
		if s.Status == "done" {
			if err := ValidateWorkItemEvidenceAt(s.Evidence, fmt.Sprintf("stages[%d].evidence", i)); err != nil {
				return err
			}
		}
		if (s.Status == "cancelled" || s.Status == "retired") && strings.TrimSpace(s.Reason) == "" {
			return workInvalid("retired or cancelled stages require a reason")
		}
		seen := map[string]bool{}
		for _, id := range s.TaskIDs {
			if !protocol.ValidID(protocol.PrefixTask, id) || seen[id] {
				return workInvalid("invalid or duplicate stage task reference")
			}
			seen[id] = true
		}
		stages[s.StageID] = s
	}
	for _, stage := range next.Stages {
		if stage.ParentID != "" && stages[stage.ParentID].Status == "done" && !slices.Contains([]protocol.WorkItemStageStatus{protocol.WorkItemStageDone, protocol.WorkItemStageCancelled, protocol.WorkItemStageRetired}, stage.Status) {
			return workInvalid("complete or explicitly retire child stages before completing their parent")
		}
	}
	visiting, done := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(id string) error {
		if visiting[id] {
			return workInvalid("stage parents and dependencies must be acyclic")
		}
		if done[id] {
			return nil
		}
		s, ok := stages[id]
		if !ok {
			return workInvalid("stage reference %s does not exist", id)
		}
		visiting[id] = true
		edges := slices.Clone(s.DependsOn)
		if s.ParentID != "" {
			edges = append(edges, s.ParentID)
		}
		for _, edge := range edges {
			if err := visit(edge); err != nil {
				return err
			}
		}
		visiting[id] = false
		done[id] = true
		return nil
	}
	for id := range stages {
		if err := visit(id); err != nil {
			return err
		}
	}
	// Completion also waits for child stages, and every child inherits its
	// ancestors' prerequisites. Check this execution graph separately from the
	// structural parent graph above: a child never waits for its parent itself.
	completionEdges := map[string][]string{}
	for _, stage := range next.Stages {
		completionEdges[stage.StageID] = append(completionEdges[stage.StageID], stage.DependsOn...)
		if stage.ParentID != "" {
			completionEdges[stage.ParentID] = append(completionEdges[stage.ParentID], stage.StageID)
		}
		for parent := stage.ParentID; parent != ""; parent = stages[parent].ParentID {
			completionEdges[stage.StageID] = append(completionEdges[stage.StageID], stages[parent].DependsOn...)
		}
	}
	visiting, done = map[string]bool{}, map[string]bool{}
	var visitCompletion func(string) error
	visitCompletion = func(id string) error {
		if visiting[id] {
			return workInvalid("stage prerequisites and group completion must be acyclic")
		}
		if done[id] {
			return nil
		}
		visiting[id] = true
		for _, edge := range completionEdges[id] {
			if err := visitCompletion(edge); err != nil {
				return err
			}
		}
		visiting[id] = false
		done[id] = true
		return nil
	}
	for id := range stages {
		if err := visitCompletion(id); err != nil {
			return err
		}
	}
	follows := map[string]protocol.WorkItemFollowOn{}
	for i, f := range next.FollowOns {
		if f.FollowOnID == "" || f.Text == "" || f.Source == "" {
			return workInvalid("follow-on requires stable id, text and source")
		}
		if _, ok := follows[f.FollowOnID]; ok {
			return workInvalid("duplicate follow-on id")
		}
		if err := validateWorkItemEnum(fmt.Sprintf("follow_ons[%d].kind", i), f.Kind, protocol.WorkItemFollowOnKindValues()); err != nil {
			return err
		}
		if err := validateWorkItemEnum(fmt.Sprintf("follow_ons[%d].status", i), f.Status, protocol.WorkItemFollowOnStatusValues()); err != nil {
			return err
		}
		if f.Status != "open" {
			if err := ValidateWorkItemEvidenceAt(f.Evidence, fmt.Sprintf("follow_ons[%d].evidence", i)); err != nil {
				return err
			}
		}
		if f.Status == "transferred" && f.TransferredTo == "" {
			return workInvalid("transferred follow-on requires destination")
		}
		follows[f.FollowOnID] = f
	}
	deliveries := map[string]protocol.WorkItemDelivery{}
	for i, d := range next.Deliveries {
		if err := validateWorkItemEnum(fmt.Sprintf("deliveries[%d].provider", i), d.Provider, protocol.WorkItemDeliveryProviderValues()); err != nil {
			return err
		}
		if d.DeliveryID == "" || d.Repository == "" || d.Number <= 0 {
			return workInvalid("invalid delivery identity")
		}
		if _, ok := deliveries[d.DeliveryID]; ok {
			return workInvalid("duplicate delivery id")
		}
		deliveries[d.DeliveryID] = d
	}
	if previous == nil {
		return nil
	}
	for _, old := range previous.Stages {
		n, ok := stages[old.StageID]
		if !ok {
			return workInvalid("retain stage %s; retire it with a reason", old.StageID)
		}
		for _, id := range old.TaskIDs {
			if !slices.Contains(n.TaskIDs, id) {
				return workInvalid("stage task history cannot be removed")
			}
		}
		for _, e := range old.Evidence {
			if !slices.ContainsFunc(n.Evidence, func(candidate protocol.WorkItemEvidence) bool { return reflect.DeepEqual(candidate, e) }) {
				return workInvalid("stage evidence history cannot be removed")
			}
		}
	}
	for _, old := range previous.FollowOns {
		n, ok := follows[old.FollowOnID]
		if !ok {
			return workInvalid("follow-on history cannot be removed")
		}
		if n.Kind != old.Kind || n.Source != old.Source {
			return workInvalid("follow-on kind and source are immutable")
		}
		for _, e := range old.Evidence {
			if !slices.ContainsFunc(n.Evidence, func(candidate protocol.WorkItemEvidence) bool { return reflect.DeepEqual(candidate, e) }) {
				return workInvalid("follow-on evidence history cannot be removed")
			}
		}
	}
	for _, old := range previous.Deliveries {
		n, ok := deliveries[old.DeliveryID]
		if !ok {
			return workInvalid("delivery history cannot be removed")
		}
		if old.Provider != n.Provider || old.Repository != n.Repository || old.Number != n.Number || old.HostID != n.HostID {
			return workInvalid("delivery target identity is immutable")
		}
	}
	return nil
}

// ValidateWorkItemEvidence requires an explicit, attributable completion basis.
func ValidateWorkItemEvidence(evidence []protocol.WorkItemEvidence) error {
	return ValidateWorkItemEvidenceAt(evidence, "evidence")
}

// ValidateWorkItemEvidenceAt retains the containing field path for nested errors.
func ValidateWorkItemEvidenceAt(evidence []protocol.WorkItemEvidence, field string) error {
	if len(evidence) == 0 {
		return workInvalid("%s: completion requires evidence", field)
	}
	for i, e := range evidence {
		if err := validateWorkItemEnum(fmt.Sprintf("%s[%d].kind", field, i), e.Kind, protocol.WorkItemEvidenceKindValues()); err != nil {
			return err
		}
		if strings.TrimSpace(e.Reference) == "" || strings.TrimSpace(e.Summary) == "" {
			return workInvalid("%s[%d] requires reference and summary", field, i)
		}
	}
	return nil
}

// ValidateWorkItemUpdateStatus preserves empty-as-unchanged update semantics.
func ValidateWorkItemUpdateStatus(status protocol.WorkItemUpdateStatus) error {
	return validateWorkItemEnum("status", status, protocol.WorkItemUpdateStatusValues())
}

// ValidateWorkItemDispositionAction checks an action when its task agreement is used.
func ValidateWorkItemDispositionAction(action protocol.WorkItemTaskDispositionAction, field string) error {
	return validateWorkItemEnum(field, action, protocol.WorkItemTaskDispositionActionValues())
}

func validateWorkItemEnum[T ~string](field string, value T, allowed []T) error {
	if slices.Contains(allowed, value) {
		return nil
	}
	// These values are strings, so encoding cannot fail. Both representations
	// derive from the same list registered with the schema generator.
	choices, _ := json.Marshal(allowed)
	details, _ := json.Marshal(struct {
		Field         string `json:"field"`
		Value         T      `json:"value"`
		AllowedValues []T    `json:"allowed_values"`
	}{field, value, allowed})
	return &protocol.Error{Code: protocol.CodeInvalidRequest,
		Message: fmt.Sprintf("%s has invalid value %q; allowed values: %s", field, value, choices), Details: details}
}

// ValidateWorkItemClosure does not infer responsibility completion from task count.
func ValidateWorkItemClosure(w protocol.WorkItem, c protocol.WorkItemClosure) error {
	if err := validateWorkItemEnum("closure.kind", c.Kind, protocol.WorkItemClosureKindValues()); err != nil {
		return err
	}
	if strings.TrimSpace(c.Reason) == "" {
		return workInvalid("closure.reason is required")
	}
	if err := ValidateWorkItemEvidenceAt(c.Evidence, "closure.evidence"); err != nil {
		return err
	}
	hasOwner := false
	for _, e := range c.Evidence {
		if e.Kind == "owner_decision" {
			hasOwner = true
		}
	}
	if !hasOwner {
		return protocol.Errorf(protocol.CodeInvalidTransition, "closure requires an explicit owner responsibility endpoint")
	}
	for _, f := range w.FollowOns {
		if (f.Kind == "commitment" || f.Kind == "problem") && f.Status == "open" {
			return protocol.Errorf(protocol.CodeInvalidTransition, "unresolved commitments or problems prevent closure")
		}
	}
	for _, s := range w.Stages {
		if s.Status == "active" || s.Status == "planned" {
			return protocol.Errorf(protocol.CodeInvalidTransition, "resolve or explicitly retire remaining stages before closure")
		}
	}
	return nil
}

// WorkItemPlanChanged reports a substantive planning change.
func WorkItemPlanChanged(a, b protocol.WorkItemPlan) bool { return !reflect.DeepEqual(a, b) }

// WorkItemTaskPlanChanged compares only the plan affecting an existing task.
// Renaming the work item or editing unrelated stages is not a new agreement.
func WorkItemTaskPlanChanged(a, b protocol.WorkItemPlan, taskID string) bool {
	if a.Intent != b.Intent || a.Project != b.Project || !reflect.DeepEqual(a.Background, b.Background) {
		return true
	}
	return !reflect.DeepEqual(workItemTaskStages(a, taskID), workItemTaskStages(b, taskID))
}
func workItemTaskStages(plan protocol.WorkItemPlan, taskID string) map[string]protocol.WorkItemStage {
	stages := map[string]protocol.WorkItemStage{}
	for _, stage := range plan.Stages {
		stages[stage.StageID] = stage
	}
	out := map[string]protocol.WorkItemStage{}
	for _, stage := range plan.Stages {
		if !slices.Contains(stage.TaskIDs, taskID) {
			continue
		}
		out["stage:"+stage.StageID] = stage
		for parent := stage.ParentID; parent != ""; parent = stages[parent].ParentID {
			ancestor := stages[parent]
			out["ancestor:"+parent] = protocol.WorkItemStage{StageID: parent, ParentID: ancestor.ParentID, DependsOn: ancestor.DependsOn, Status: ancestor.Status}
		}
	}
	return out
}

// WorkItemTaskRetired reports whether an agreement newly became unavailable,
// including when its stage was moved into a retired containing group.
func WorkItemTaskRetired(a, b protocol.WorkItemPlan, taskID string) bool {
	old, next := workItemTaskStages(a, taskID), workItemTaskStages(b, taskID)
	for id, stage := range next {
		if slices.Contains([]protocol.WorkItemStageStatus{protocol.WorkItemStageRetired, protocol.WorkItemStageCancelled}, stage.Status) && old[id].Status != stage.Status {
			return true
		}
	}
	return false
}

// WorkItemStageAcceptsWork excludes completed or retired containing groups.
func WorkItemStageAcceptsWork(plan protocol.WorkItemPlan, id string) bool {
	stages := map[string]protocol.WorkItemStage{}
	for _, stage := range plan.Stages {
		stages[stage.StageID] = stage
	}
	for id != "" {
		stage, ok := stages[id]
		if !ok || slices.Contains([]protocol.WorkItemStageStatus{protocol.WorkItemStageDone, protocol.WorkItemStageRetired, protocol.WorkItemStageCancelled}, stage.Status) {
			return false
		}
		id = stage.ParentID
	}
	return true
}

// ValidateWorkItemTaskDependencies checks the actual waiting graph after tasks
// cover multiple stages or work items. Stage DAG validation alone cannot detect
// cycles introduced by that shared execution identity. Plans must already have
// valid parent and dependency references.
func ValidateWorkItemTaskDependencies(items []protocol.WorkItem, lifecycles map[string]protocol.TaskLifecycle, taskDependencies map[string][]string) error {
	type node struct {
		item string // Empty for a task, otherwise the containing work item.
		id   string
	}
	edges := map[node][]node{}
	for taskID, dependencies := range taskDependencies {
		if lifecycles[taskID].Terminal() {
			continue
		}
		for _, dependency := range dependencies {
			if lifecycles[dependency] != protocol.TaskCompleted {
				edges[node{id: taskID}] = append(edges[node{id: taskID}], node{id: dependency})
			}
		}
	}
	for _, item := range items {
		if item.Status == "closed" {
			continue
		}
		stages := map[string]protocol.WorkItemStage{}
		for _, stage := range item.Stages {
			stages[stage.StageID] = stage
		}
		for _, stage := range item.Stages {
			stageNode := node{item: item.WorkItemID, id: stage.StageID}
			if stage.ParentID != "" && !slices.Contains([]protocol.WorkItemStageStatus{protocol.WorkItemStageDone, protocol.WorkItemStageCancelled, protocol.WorkItemStageRetired}, stage.Status) && stages[stage.ParentID].Status != "done" {
				parent := node{item: item.WorkItemID, id: stage.ParentID}
				edges[parent] = append(edges[parent], stageNode)
			}
			for _, taskID := range stage.TaskIDs {
				if lifecycles[taskID].Terminal() {
					continue
				}
				taskNode := node{id: taskID}
				if stage.Status != "done" {
					edges[stageNode] = append(edges[stageNode], taskNode)
				}
				// Match the claim gate: an unfinished prerequisite explicitly
				// covered by this same task does not require a separate run.
				for ancestor := stage; ; ancestor = stages[ancestor.ParentID] {
					for _, id := range ancestor.DependsOn {
						dependency := stages[id]
						if dependency.Status != "done" && !slices.Contains(dependency.TaskIDs, taskID) {
							edges[taskNode] = append(edges[taskNode], node{item: item.WorkItemID, id: id})
						}
					}
					if ancestor.ParentID == "" {
						break
					}
				}
			}
		}
	}
	visiting, done := map[node]bool{}, map[node]bool{}
	var visit func(node) error
	visit = func(n node) error {
		if visiting[n] {
			return workInvalid("task links and stage prerequisites create a waiting cycle at %s; split the task or revise its stage coverage", n.id)
		}
		if done[n] {
			return nil
		}
		visiting[n] = true
		for _, dependency := range edges[n] {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		visiting[n] = false
		done[n] = true
		return nil
	}
	for n := range edges {
		if err := visit(n); err != nil {
			return err
		}
	}
	return nil
}
