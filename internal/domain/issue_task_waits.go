package domain

import (
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
)

// ValidateIssuePrimaryTaskWaits combines actual dispatch and completion waits.
// Parent completion waits for children; a child's task does not inherit the
// parent's explicit prerequisites. Result references have no edges here.
func ValidateIssuePrimaryTaskWaits(issues []protocol.Issue, primary map[string][]string, lives map[string]protocol.TaskLifecycle, dependencies map[string][]string, legacy ...protocol.WorkItem) error {
	type node struct{ kind, id string }
	edges := map[node][]node{}
	achieved := map[string]bool{}
	controls := map[string]protocol.IssueControl{}
	for _, issue := range issues {
		controls[issue.IssueID] = issue.IssueControl
		achieved[issue.IssueID] = issue.State == protocol.IssueClosed && issue.Closure != nil && issue.Closure.Kind == protocol.WorkItemClosureCompleted
	}
	for task, deps := range dependencies {
		if lives[task].Terminal() {
			continue
		}
		for _, dep := range deps {
			if lives[dep] != protocol.TaskCompleted {
				edges[node{"task", task}] = append(edges[node{"task", task}], node{"task", dep})
			}
		}
	}
	appendStageWaits := func(scopeID string, plan []protocol.IssueStage) {
		stages := map[string]protocol.IssueStage{}
		for _, stage := range plan {
			stages[stage.StageID] = stage
		}
		for _, stage := range plan {
			stageNode := node{"stage:" + scopeID, stage.StageID}
			if stage.ParentID != "" && !slices.Contains([]protocol.IssueStageStatus{protocol.WorkItemStageDone, protocol.WorkItemStageCancelled, protocol.WorkItemStageRetired}, stage.Status) && stages[stage.ParentID].Status != protocol.WorkItemStageDone {
				edges[node{"stage:" + scopeID, stage.ParentID}] = append(edges[node{"stage:" + scopeID, stage.ParentID}], stageNode)
			}
			for _, task := range stage.TaskIDs {
				if lives[task].Terminal() {
					continue
				}
				if stage.Status != protocol.WorkItemStageDone {
					edges[stageNode] = append(edges[stageNode], node{"task", task})
				}
				for ancestor := stage; ; ancestor = stages[ancestor.ParentID] {
					for _, id := range ancestor.DependsOn {
						dep := stages[id]
						if dep.Status != protocol.WorkItemStageDone && !slices.Contains(dep.TaskIDs, task) {
							edges[node{"task", task}] = append(edges[node{"task", task}], node{"stage:" + scopeID, id})
						}
					}
					if ancestor.ParentID == "" {
						break
					}
				}
			}
		}
	}
	for _, issue := range issues {
		current := node{"issue", issue.IssueID}
		if issue.State == protocol.IssueClosed || issue.State == protocol.IssueCancelled {
			continue
		}
		if parent, ok := controls[issue.ParentIssueID]; ok && parent.State != protocol.IssueClosed && parent.State != protocol.IssueCancelled {
			edges[node{"issue", issue.ParentIssueID}] = append(edges[node{"issue", issue.ParentIssueID}], current)
		}
		for _, task := range primary[issue.IssueID] {
			if !lives[task].Terminal() {
				edges[current] = append(edges[current], node{"task", task})
			}
		}

		appendStageWaits(issue.IssueID, issue.Plan.Stages)

		for _, dep := range issue.DependsOn {
			if achieved[dep] {
				continue
			}
			edges[current] = append(edges[current], node{"issue", dep})
			for _, task := range primary[issue.IssueID] {
				if !lives[task].Terminal() {
					edges[node{"task", task}] = append(edges[node{"task", task}], node{"issue", dep})
				}
			}
		}
	}
	for _, item := range legacy {
		if item.Status != "closed" {
			appendStageWaits(item.WorkItemID, item.Stages)
		}
	}
	visiting, done := map[node]bool{}, map[node]bool{}
	var visit func(node) error
	visit = func(n node) error {
		if visiting[n] {
			return protocol.Errorf(protocol.CodeInvalidRequest, "issue completion and task prerequisites create a waiting cycle at %s", n.id)
		}
		if done[n] {
			return nil
		}
		visiting[n] = true
		for _, dep := range edges[n] {
			if err := visit(dep); err != nil {
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
