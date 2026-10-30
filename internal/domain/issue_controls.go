package domain

import (
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

// IssueLifecycleFacts are gathered in one app transaction from exact persisted identities.
type IssueLifecycleFacts struct {
	ManagerTurns      []protocol.IssueBlocker
	LogicalDuties     []protocol.IssueBlocker
	Executions        []protocol.IssueBlocker
	UnknownOperations []protocol.IssueBlocker
	PendingControls   []protocol.IssueBlocker
	UnsettledChildren []protocol.IssueBlocker
	NonTerminalTasks  []protocol.IssueBlocker
	OpenAttention     []protocol.IssueBlocker
	OpenCommitments   []protocol.IssueBlocker
}

// ValidateIssueControl validates persisted shape; authentication and registry resolution belong to app.
func ValidateIssueControl(c protocol.IssueControl) error {
	if c.WorkStatus != "" && !slices.Contains([]protocol.IssueWorkStatus{protocol.IssueBacklog, protocol.IssueTodo, protocol.IssueInProgress, protocol.IssueInReview}, c.WorkStatus) {
		return protocol.Errorf(protocol.CodeInvalidRequest, "invalid issue work status")
	}
	if !protocol.ValidID(protocol.PrefixWorkItem, c.IssueID) || (c.ParentIssueID != "" && !protocol.ValidID(protocol.PrefixWorkItem, c.ParentIssueID)) || c.ParentIssueID == c.IssueID || c.AssignmentEpoch < 1 {
		return protocol.Errorf(protocol.CodeInvalidRequest, "invalid issue control identity")
	}
	if !slices.Contains([]protocol.IssueState{protocol.IssueActive, protocol.IssuePaused, protocol.IssueCancelling, protocol.IssueCancelled, protocol.IssueClosed}, c.State) {
		return protocol.Errorf(protocol.CodeInvalidRequest, "invalid issue state")
	}
	if err := validateIssueAssigneeShape(c.Assignee); err != nil {
		return err
	}
	switch c.AssignmentState {
	case "active":
		if c.PendingAssignee != nil || c.PendingHandoffID != "" {
			return protocol.Errorf(protocol.CodeInvalidRequest, "active assignment cannot retain a pending handoff")
		}
	case "handoff":
		if c.PendingAssignee == nil || c.PendingHandoffID == "" {
			return protocol.Errorf(protocol.CodeInvalidRequest, "handoff requires a pending assignee and identity")
		}
		if err := validateIssueAssigneeShape(*c.PendingAssignee); err != nil {
			return err
		}
	default:
		return protocol.Errorf(protocol.CodeInvalidRequest, "invalid assignment state")
	}
	return nil
}

func validateIssueAssigneeShape(a protocol.IssueAssignee) error {
	if (a.Kind != protocol.IssueAssigneeOwner && a.Kind != protocol.IssueAssigneeAgent) || strings.TrimSpace(a.Ref) == "" || a.Ref != strings.TrimSpace(a.Ref) {
		return protocol.Errorf(protocol.CodeInvalidRequest, "a concrete assignee reference is required")
	}
	// This validates shape only. App resolves owner or P2 registry; a string is not proof.
	return nil
}

// CheckIssueEpoch requires the active assignment at the caller's expected epoch.
func CheckIssueEpoch(c protocol.IssueControl, expected int64) error {
	if c.AssignmentEpoch != expected {
		return protocol.Errorf(protocol.CodeStaleRevision, "issue assignment changed")
	}
	if c.AssignmentState != "active" {
		return protocol.Errorf(protocol.CodeInvalidTransition, "issue assignment is being reconciled")
	}
	return nil
}

// PauseIssue changes dispatch admission only and emits no task or run control effect.
func PauseIssue(c protocol.IssueControl) (protocol.IssueControl, error) {
	if c.State != protocol.IssueActive || c.AssignmentState != "active" {
		return c, protocol.Errorf(protocol.CodeInvalidTransition, "only an active issue can pause")
	}
	c.State = protocol.IssuePaused
	return c, nil
}

// ResumeIssue restores new-dispatch admission without changing any task or run.
func ResumeIssue(c protocol.IssueControl) (protocol.IssueControl, error) {
	if c.State != protocol.IssuePaused || c.AssignmentState != "active" {
		return c, protocol.Errorf(protocol.CodeInvalidTransition, "only a paused issue can resume")
	}
	c.State = protocol.IssueActive
	return c, nil
}

// BeginIssueCancellation records intent; caller must explicitly plan task/child dispositions.
func BeginIssueCancellation(c protocol.IssueControl) (protocol.IssueControl, error) {
	if (c.State != protocol.IssueActive && c.State != protocol.IssuePaused) || c.AssignmentState != "active" {
		return c, protocol.Errorf(protocol.CodeInvalidTransition, "issue cannot begin cancellation")
	}
	c.State = protocol.IssueCancelling
	return c, nil
}

// IssueEndpointBlockers retains every unresolved responsibility fact for close or cancel.
func IssueEndpointBlockers(f IssueLifecycleFacts) []protocol.IssueBlocker {
	var out []protocol.IssueBlocker
	for _, group := range [][]protocol.IssueBlocker{f.ManagerTurns, f.LogicalDuties, f.Executions, f.UnknownOperations, f.PendingControls, f.UnsettledChildren, f.NonTerminalTasks, f.OpenAttention, f.OpenCommitments} {
		out = append(out, group...)
	}
	return out
}

// FinishIssueCancellation requires observed settlement rather than cancellation intent.
func FinishIssueCancellation(c protocol.IssueControl, f IssueLifecycleFacts) (protocol.IssueControl, error) {
	if c.State != protocol.IssueCancelling || c.AssignmentState != "active" || len(IssueEndpointBlockers(f)) != 0 {
		return c, protocol.Errorf(protocol.CodeInvalidTransition, "issue cancellation has unsettled obligations")
	}
	c.State = protocol.IssueCancelled
	return c, nil
}

// CloseIssueControl checks structural facts; endpoint evidence is validated separately.
func CloseIssueControl(c protocol.IssueControl, f IssueLifecycleFacts) (protocol.IssueControl, error) {
	if (c.State != protocol.IssueActive && c.State != protocol.IssuePaused) || c.AssignmentState != "active" || len(IssueEndpointBlockers(f)) != 0 {
		return c, protocol.Errorf(protocol.CodeInvalidTransition, "issue closure has unsettled obligations")
	}
	c.State = protocol.IssueClosed
	return c, nil
}

// ReopenIssueControl restores responsibility without reopening task or run state.
func ReopenIssueControl(c protocol.IssueControl) (protocol.IssueControl, error) {
	if (c.State != protocol.IssueClosed && c.State != protocol.IssueCancelled) || c.AssignmentState != "active" {
		return c, protocol.Errorf(protocol.CodeInvalidTransition, "issue cannot reopen")
	}
	c.State = protocol.IssueActive
	return c, nil
}

// ValidateIssueAncestorActivation is required for create, reparent and reopen.
// A paused ancestor permits planning but still gates new dispatch separately.
func ValidateIssueAncestorActivation(parent string, controls map[string]protocol.IssueControl) error {
	seen := map[string]bool{}
	for id := parent; id != ""; {
		if seen[id] {
			return protocol.Errorf(protocol.CodeInvalidRequest, "issue ancestry must be acyclic")
		}
		seen[id] = true
		ancestor, ok := controls[id]
		if !ok {
			return protocol.Errorf(protocol.CodeInvalidRequest, "canonical ancestor is missing")
		}
		if (ancestor.State != protocol.IssueActive && ancestor.State != protocol.IssuePaused) || ancestor.AssignmentState != "active" {
			return protocol.Errorf(protocol.CodeInvalidTransition, "ancestor responsibility must be reopened or reconciled first")
		}
		id = ancestor.ParentIssueID
	}
	return nil
}

// IssueCompletionFact is assembled only from persisted server-validated immutable
// endpoint evidence. EvidenceVerified is not a payload field or trusted restored
// JSON boolean; absent/older evidence remains an explicit blocker.
type IssueCompletionFact struct {
	State            protocol.IssueState
	ClosureKind      protocol.IssueClosureKind
	EvidenceVerified bool
}

// IssueDependencySatisfied requires a verified achieved endpoint, not merely a terminal label.
func IssueDependencySatisfied(f IssueCompletionFact) bool {
	return f.State == protocol.IssueClosed && f.ClosureKind == protocol.WorkItemClosureCompleted && f.EvidenceVerified
}

// BeginIssueAssignment requires app-verified target identity and a caller-created handoff ID.
func BeginIssueAssignment(c protocol.IssueControl, target protocol.IssueAssignee, targetResolved bool, handoffID string) (protocol.IssueControl, error) {
	if !targetResolved {
		return c, protocol.Errorf(protocol.CodeInvalidRequest, "assignee has no verified registration")
	}
	if err := validateIssueAssigneeShape(target); err != nil {
		return c, err
	}
	if c.AssignmentState != "active" || c.State == protocol.IssueClosed || c.State == protocol.IssueCancelled {
		return c, protocol.Errorf(protocol.CodeInvalidTransition, "issue cannot transfer responsibility")
	}
	if c.Assignee == target {
		return c, nil
	}
	if handoffID == "" || c.AssignmentEpoch == int64(^uint64(0)>>1) {
		return c, protocol.Errorf(protocol.CodeInvalidRequest, "invalid handoff identity or epoch")
	}
	c.AssignmentEpoch++
	c.AssignmentState, c.PendingHandoffID = "handoff", handoffID
	c.PendingAssignee = &target
	return c, nil
}

// FinishIssueAssignment uses proven old-turn reconciliation, never a model's assertion.
func FinishIssueAssignment(c protocol.IssueControl, handoffID string, reconciled bool) (protocol.IssueControl, error) {
	if c.AssignmentState != "handoff" || c.PendingHandoffID != handoffID || c.PendingAssignee == nil || !reconciled {
		return c, protocol.Errorf(protocol.CodeInvalidTransition, "old issue responsibility is not reconciled")
	}
	c.Assignee, c.PendingAssignee = *c.PendingAssignee, nil
	c.AssignmentState, c.PendingHandoffID = "active", ""
	return c, nil
}

// ValidateIssueReparentFacts rejects activity; graph/project/authority checks are separate.
func ValidateIssueReparentFacts(f IssueLifecycleFacts) error {
	if len(f.ManagerTurns)+len(f.LogicalDuties)+len(f.Executions)+len(f.UnknownOperations)+len(f.PendingControls) != 0 {
		return protocol.Errorf(protocol.CodeInvalidTransition, "active or uncertain issue responsibility prevents reparenting")
	}
	return nil
}

// IssueDispatchBlockers separates ancestor admission from the primary's explicit
// completion prerequisites. Child membership alone is never a task claim gate.
func IssueDispatchBlockers(primary string, controls map[string]protocol.IssueControl, dependencies []protocol.IssueDependency, satisfied map[string]bool) []protocol.IssueBlocker {
	var blockers []protocol.IssueBlocker
	seen := map[string]bool{}
	for id := primary; id != ""; {
		if seen[id] {
			return append(blockers, protocol.IssueBlocker{Code: "issue_cycle", ObjectID: id})
		}
		seen[id] = true
		control, ok := controls[id]
		if !ok {
			return append(blockers, protocol.IssueBlocker{Code: "issue_control_missing", ObjectID: id})
		}
		if control.State != protocol.IssueActive {
			blockers = append(blockers, protocol.IssueBlocker{Code: "issue_admission", ObjectID: id})
		}
		if control.AssignmentState != "active" {
			blockers = append(blockers, protocol.IssueBlocker{Code: "issue_assignment", ObjectID: id})
		}
		id = control.ParentIssueID
	}
	for _, edge := range dependencies {
		if edge.IssueID == primary && !satisfied[edge.DependsOn] {
			blockers = append(blockers, protocol.IssueBlocker{Code: "issue_dependency", ObjectID: edge.DependsOn})
		}
	}
	return blockers
}

// ValidateIssueGraph distinguishes ancestry from completion requirements.
// Requirement edge a->b means a cannot complete until b has completed.
func ValidateIssueGraph(controls []protocol.IssueControl, dependencies []protocol.IssueDependency) error {
	nodes := map[string]protocol.IssueControl{}
	parents, deps, completion := map[string][]string{}, map[string][]string{}, map[string][]string{}
	for _, c := range controls {
		if c.IssueID == "" {
			return protocol.Errorf(protocol.CodeInvalidRequest, "issue identity is required")
		}
		if _, exists := nodes[c.IssueID]; exists {
			return protocol.Errorf(protocol.CodeInvalidRequest, "duplicate issue identity")
		}
		nodes[c.IssueID] = c
	}
	for _, c := range controls {
		if c.ParentIssueID == "" {
			continue
		}
		if _, exists := nodes[c.ParentIssueID]; !exists {
			return protocol.Errorf(protocol.CodeInvalidRequest, "issue parent is missing")
		}
		parents[c.IssueID] = []string{c.ParentIssueID}
		completion[c.ParentIssueID] = append(completion[c.ParentIssueID], c.IssueID)
	}
	if err := validateIssueEdges(nodes, parents); err != nil {
		return err
	}
	seen := map[protocol.IssueDependency]bool{}
	for _, edge := range dependencies {
		if _, ok := nodes[edge.IssueID]; !ok {
			return protocol.Errorf(protocol.CodeInvalidRequest, "dependent issue is missing")
		}
		if _, ok := nodes[edge.DependsOn]; !ok {
			return protocol.Errorf(protocol.CodeInvalidRequest, "issue prerequisite is missing")
		}
		if seen[edge] {
			return protocol.Errorf(protocol.CodeInvalidRequest, "duplicate issue prerequisite")
		}
		seen[edge] = true
		deps[edge.IssueID] = append(deps[edge.IssueID], edge.DependsOn)
	}
	if err := validateIssueEdges(nodes, deps); err != nil {
		return err
	}
	for id := range nodes {
		completion[id] = append(completion[id], deps[id]...)
	}
	return validateIssueEdges(nodes, completion)
}

func validateIssueEdges(nodes map[string]protocol.IssueControl, edges map[string][]string) error {
	visiting, done := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(id string) error {
		if visiting[id] {
			return protocol.Errorf(protocol.CodeInvalidRequest, "issue tree and prerequisites must be acyclic")
		}
		if done[id] {
			return nil
		}
		visiting[id] = true
		for _, other := range edges[id] {
			if _, exists := nodes[other]; !exists {
				return protocol.Errorf(protocol.CodeInvalidRequest, "issue graph target is missing")
			}
			if err := visit(other); err != nil {
				return err
			}
		}
		visiting[id], done[id] = false, true
		return nil
	}
	for id := range nodes {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}
