package domain

import (
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

// Assignment fixtures represent already-resolved identities, not authenticated runtimes.
func issueControlFixture() protocol.IssueControl {
	return protocol.IssueControl{IssueID: "wi_00000000000000000000000001", State: protocol.IssueActive, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: "fixture-owner"}, AssignmentEpoch: 1, AssignmentState: "active"}
}

func TestIssueControlPauseLeavesAssignmentAndFactsUntouched(t *testing.T) {
	before := issueControlFixture()
	paused, err := PauseIssue(before)
	if err != nil {
		t.Fatal(err)
	}
	want := before
	want.State = protocol.IssuePaused
	if !reflect.DeepEqual(paused, want) {
		t.Fatalf("changed more than dispatch state: %+v", paused)
	}
	resumed, err := ResumeIssue(paused)
	if err != nil || !reflect.DeepEqual(resumed, before) {
		t.Fatalf("resume: %+v %v", resumed, err)
	}
}

func TestIssueControlCancellationIntentDoesNotCertifyStopping(t *testing.T) {
	cancelling, err := BeginIssueCancellation(issueControlFixture())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []IssueLifecycleFacts{
		{Executions: []protocol.IssueBlocker{{Code: "waiting_worker", ObjectID: "run_fixture"}}},
		{UnknownOperations: []protocol.IssueBlocker{{Code: "unknown_native", RequestID: "req_fixture"}}},
		{UnsettledChildren: []protocol.IssueBlocker{{Code: "child", ObjectID: "wi_child"}}},
		{NonTerminalTasks: []protocol.IssueBlocker{{Code: "primary_task", ObjectID: "task_no_stage"}}},
	} {
		if _, err := FinishIssueCancellation(cancelling, f); err == nil {
			t.Fatal("cancelled before proven settlement")
		}
	}
	finished, err := FinishIssueCancellation(cancelling, IssueLifecycleFacts{})
	if err != nil || finished.State != protocol.IssueCancelled {
		t.Fatalf("settled cancellation: %+v %v", finished, err)
	}
}

func TestIssueControlOldEpochFencedBeforeNewAssigneeActivation(t *testing.T) {
	before := issueControlFixture()
	target := protocol.IssueAssignee{Kind: protocol.IssueAssigneeAgent, Ref: "pre-resolved-test-fixture"}
	if _, err := BeginIssueAssignment(before, target, false, "handoff_fixture"); err == nil {
		t.Fatal("accepted unresolved target")
	}
	fenced, err := BeginIssueAssignment(before, target, true, "handoff_fixture")
	if err != nil {
		t.Fatal(err)
	}
	if fenced.AssignmentEpoch != 2 || fenced.Assignee != before.Assignee || fenced.AssignmentState != "handoff" {
		t.Fatalf("not fenced before activation: %+v", fenced)
	}
	if CheckIssueEpoch(fenced, 1) == nil || CheckIssueEpoch(fenced, 2) == nil {
		t.Fatal("handoff admitted a controller")
	}
	if _, err := FinishIssueAssignment(fenced, "handoff_fixture", false); err == nil {
		t.Fatal("activated without reconciliation")
	}
	active, err := FinishIssueAssignment(fenced, "handoff_fixture", true)
	if err != nil || active.Assignee != target || CheckIssueEpoch(active, 2) != nil {
		t.Fatalf("activation: %+v %v", active, err)
	}
	if before.AssignmentEpoch != 1 || before.PendingAssignee != nil {
		t.Fatal("mutated original control")
	}
}

func TestIssueControlSameOwnerAssignmentDoesNotInventTransfer(t *testing.T) {
	before := issueControlFixture()
	after, err := BeginIssueAssignment(before, before.Assignee, true, "")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("no-op changed responsibility: %+v %v", after, err)
	}
}

func TestIssueControlReparentBlocksNativeAndLogicalResponsibility(t *testing.T) {
	for _, f := range []IssueLifecycleFacts{
		{ManagerTurns: []protocol.IssueBlocker{{Code: "manager_turn"}}},
		{LogicalDuties: []protocol.IssueBlocker{{Code: "logical_processing"}}},
		{UnknownOperations: []protocol.IssueBlocker{{Code: "native_receipt"}}},
		{Executions: []protocol.IssueBlocker{{Code: "worker"}}},
		{PendingControls: []protocol.IssueBlocker{{Code: "control_request"}}},
	} {
		if ValidateIssueReparentFacts(f) == nil {
			t.Fatal("reparent ignored active responsibility")
		}
	}
}

func TestIssueControlGraphRejectsIndependentAndCollapsedCycles(t *testing.T) {
	for _, tc := range []struct {
		name  string
		nodes []protocol.IssueControl
		edges []protocol.IssueDependency
		valid bool
	}{
		{"empty_identity", []protocol.IssueControl{{}}, nil, false},
		{"duplicate_identity", []protocol.IssueControl{{IssueID: "a"}, {IssueID: "a"}}, nil, false},
		{"missing_parent", []protocol.IssueControl{{IssueID: "a", ParentIssueID: "missing"}}, nil, false},
		{"missing_prerequisite", []protocol.IssueControl{{IssueID: "a"}}, []protocol.IssueDependency{{IssueID: "a", DependsOn: "missing"}}, false},
		{"self_dependency", []protocol.IssueControl{{IssueID: "a"}}, []protocol.IssueDependency{{IssueID: "a", DependsOn: "a"}}, false},
		{"duplicate_dependency", []protocol.IssueControl{{IssueID: "a"}, {IssueID: "b"}}, []protocol.IssueDependency{{IssueID: "a", DependsOn: "b"}, {IssueID: "a", DependsOn: "b"}}, false},
		{"tree", []protocol.IssueControl{{IssueID: "a"}, {IssueID: "b", ParentIssueID: "a"}}, nil, true},
		{"parent_cycle", []protocol.IssueControl{{IssueID: "a", ParentIssueID: "b"}, {IssueID: "b", ParentIssueID: "a"}}, nil, false},
		{"dependency_cycle", []protocol.IssueControl{{IssueID: "a"}, {IssueID: "b"}}, []protocol.IssueDependency{{IssueID: "a", DependsOn: "b"}, {IssueID: "b", DependsOn: "a"}}, false},
		{"child_waits_parent", []protocol.IssueControl{{IssueID: "a"}, {IssueID: "b", ParentIssueID: "a"}}, []protocol.IssueDependency{{IssueID: "b", DependsOn: "a"}}, false},
		{"parent_duplicates_child_requirement", []protocol.IssueControl{{IssueID: "a"}, {IssueID: "b", ParentIssueID: "a"}}, []protocol.IssueDependency{{IssueID: "a", DependsOn: "b"}}, true},
		{"grandchild_waits_ancestor", []protocol.IssueControl{{IssueID: "a"}, {IssueID: "b", ParentIssueID: "a"}, {IssueID: "c", ParentIssueID: "b"}}, []protocol.IssueDependency{{IssueID: "c", DependsOn: "a"}}, false},
		{"harmless_sibling_order", []protocol.IssueControl{{IssueID: "a"}, {IssueID: "b", ParentIssueID: "a"}, {IssueID: "c", ParentIssueID: "a"}}, []protocol.IssueDependency{{IssueID: "b", DependsOn: "c"}}, true},
		{"unrelated_prerequisite", []protocol.IssueControl{{IssueID: "a"}, {IssueID: "b", ParentIssueID: "a"}, {IssueID: "c"}}, []protocol.IssueDependency{{IssueID: "a", DependsOn: "c"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateIssueGraph(tc.nodes, tc.edges)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

func TestIssueControlChildClaimsWhileParentExplicitlyWaitsForChild(t *testing.T) {
	a, b := issueControlFixture(), issueControlFixture()
	a.IssueID, b.IssueID = "a", "b"
	b.ParentIssueID = "a"
	controls := map[string]protocol.IssueControl{"a": a, "b": b}
	dependencies := []protocol.IssueDependency{{IssueID: "a", DependsOn: "b"}}
	if err := ValidateIssueGraph([]protocol.IssueControl{a, b}, dependencies); err != nil {
		t.Fatal(err)
	}
	if blockers := IssueDispatchBlockers("b", controls, dependencies, nil); len(blockers) != 0 {
		t.Fatalf("child inherited parent's completion wait: %+v", blockers)
	}
	if blockers := IssueDispatchBlockers("a", controls, dependencies, nil); len(blockers) != 1 || blockers[0].Code != "issue_dependency" {
		t.Fatalf("parent did not wait explicitly: %+v", blockers)
	}
	if blockers := IssueDispatchBlockers("a", controls, dependencies, map[string]bool{"b": true}); len(blockers) != 0 {
		t.Fatalf("verified child completion did not release parent: %+v", blockers)
	}
	a.State = protocol.IssuePaused
	controls["a"] = a
	if blockers := IssueDispatchBlockers("b", controls, dependencies, nil); len(blockers) != 1 || blockers[0].Code != "issue_admission" {
		t.Fatalf("ancestor pause lost admission effect: %+v", blockers)
	}
}

func TestIssueControlAncestorEndpointPreventsChildActivation(t *testing.T) {
	for _, state := range []protocol.IssueState{protocol.IssueClosed, protocol.IssueCancelled, protocol.IssueCancelling} {
		parent := issueControlFixture()
		parent.IssueID = "parent"
		parent.State = state
		if ValidateIssueAncestorActivation("parent", map[string]protocol.IssueControl{"parent": parent}) == nil {
			t.Fatalf("activated child under %s", state)
		}
	}
	parent := issueControlFixture()
	parent.IssueID = "parent"
	parent.State = protocol.IssuePaused
	if err := ValidateIssueAncestorActivation("parent", map[string]protocol.IssueControl{"parent": parent}); err != nil {
		t.Fatalf("pause forbade planning: %v", err)
	}
}

func TestIssueControlDependencyRequiresVerifiedAchievedEndpoint(t *testing.T) {
	for _, kind := range []protocol.IssueClosureKind{protocol.WorkItemClosureDeclined, protocol.WorkItemClosureHandedOff, protocol.WorkItemClosureSuperseded} {
		if IssueDependencySatisfied(IssueCompletionFact{State: protocol.IssueClosed, ClosureKind: kind, EvidenceVerified: true}) {
			t.Fatalf("unachieved endpoint released dependency: %s", kind)
		}
	}
	if IssueDependencySatisfied(IssueCompletionFact{State: protocol.IssueCancelled, ClosureKind: protocol.WorkItemClosureCompleted, EvidenceVerified: true}) {
		t.Fatal("cancelled prerequisite released dependency")
	}
	if IssueDependencySatisfied(IssueCompletionFact{State: protocol.IssueClosed, ClosureKind: protocol.WorkItemClosureCompleted}) {
		t.Fatal("unverified completed label released dependency")
	}
	if !IssueDependencySatisfied(IssueCompletionFact{State: protocol.IssueClosed, ClosureKind: protocol.WorkItemClosureCompleted, EvidenceVerified: true}) {
		t.Fatal("verified achievement did not release dependency")
	}
}

func TestIssueControlShapeAndFencedTransitions(t *testing.T) {
	valid := issueControlFixture()
	if err := ValidateIssueControl(valid); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*protocol.IssueControl){
		func(c *protocol.IssueControl) { c.IssueID = "unknown-prefix" },
		func(c *protocol.IssueControl) { c.ParentIssueID = c.IssueID },
		func(c *protocol.IssueControl) { c.ParentIssueID = "wi_missing" },
		func(c *protocol.IssueControl) { c.State = "unknown" },
		func(c *protocol.IssueControl) { c.AssignmentEpoch = 0 },
		func(c *protocol.IssueControl) { c.Assignee.Ref = " " },
		func(c *protocol.IssueControl) { c.Assignee.Kind = "category" },
		func(c *protocol.IssueControl) { c.AssignmentState = "unknown" },
		func(c *protocol.IssueControl) { c.PendingHandoffID = "unexpected" },
		func(c *protocol.IssueControl) { c.AssignmentState = "handoff" },
	} {
		next := valid
		mutate(&next)
		if ValidateIssueControl(next) == nil {
			t.Fatalf("invalid control accepted: %+v", next)
		}
	}
	fenced, err := BeginIssueAssignment(valid, protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: "resolved-successor"}, true, "handoff")
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateIssueControl(fenced); err != nil {
		t.Fatal(err)
	}
	if _, err := PauseIssue(fenced); err == nil {
		t.Fatal("fenced responsibility paused")
	}
	if _, err := BeginIssueCancellation(fenced); err == nil {
		t.Fatal("fenced responsibility cancelled")
	}
	fenced.State = protocol.IssuePaused
	if _, err := ResumeIssue(fenced); err == nil {
		t.Fatal("fenced responsibility resumed")
	}
}

func TestIssueControlEndpointRequiresEveryObligationSettled(t *testing.T) {
	for _, facts := range []IssueLifecycleFacts{
		{ManagerTurns: []protocol.IssueBlocker{{Code: "manager"}}},
		{LogicalDuties: []protocol.IssueBlocker{{Code: "logical"}}},
		{Executions: []protocol.IssueBlocker{{Code: "run"}}},
		{UnknownOperations: []protocol.IssueBlocker{{Code: "unknown"}}},
		{PendingControls: []protocol.IssueBlocker{{Code: "control"}}},
		{UnsettledChildren: []protocol.IssueBlocker{{Code: "child"}}},
		{NonTerminalTasks: []protocol.IssueBlocker{{Code: "task"}}},
		{OpenAttention: []protocol.IssueBlocker{{Code: "attention"}}},
		{OpenCommitments: []protocol.IssueBlocker{{Code: "commitment"}}},
	} {
		control := issueControlFixture()
		if _, err := CloseIssueControl(control, facts); err == nil {
			t.Fatalf("closed despite %+v", facts)
		}
		control.State = protocol.IssueCancelling
		if _, err := FinishIssueCancellation(control, facts); err == nil {
			t.Fatalf("cancelled despite %+v", facts)
		}
	}
	control := issueControlFixture()
	closed, err := CloseIssueControl(control, IssueLifecycleFacts{})
	if err != nil || closed.State != protocol.IssueClosed {
		t.Fatalf("close=%+v %v", closed, err)
	}
	reopened, err := ReopenIssueControl(closed)
	if err != nil || !reflect.DeepEqual(control, reopened) {
		t.Fatalf("reopen=%+v %v", reopened, err)
	}
}
