package app

import (
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestCanonicalDefinitionFieldsPersistHistoryAndFrozenFacts(t *testing.T) {
	h := newHarness(t)
	plan := protocol.IssuePlan{Title: "Define a bounded responsibility", Project: "default", Intent: "Investigate the goal", Scope: "Only the isolated fixture", CompletionCriteria: []string{"Owner can retrieve the final evidence"}, Stages: []protocol.IssueStage{}}
	issue := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, protocol.CreateIssuePayload{Plan: plan}, nil), CanonicalOwnerIssueCommand{Action: "create"}))
	task := createCanonicalTaskForTest(t, h, issue)
	var original []protocol.TaskWorkContext
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		original, err = freezeCanonicalTaskContext(t.Context(), tx.Q(), issue)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	savedSpec := h.storedSpec(task, 1)
	if !reflect.DeepEqual(savedSpec.SharedContext, original) {
		t.Fatal("created task lost definition facts")
	}
	if len(original) != 1 || !strings.Contains(original[0].Facts[len(original[0].Facts)-1].Text, "not task acceptance") || !strings.Contains(original[0].Facts[0].Source, issue.IssueID) {
		t.Fatal(original)
	}
	current := canonicalTaskIssue(t, h, issue.IssueID)
	next := current.Plan
	next.Scope = "A revised explicit scope"
	next.CompletionCriteria = []string{"Owner can retrieve the corrected evidence"}
	// Explicitly keep the existing task's frozen contract while revising the responsibility.
	payload := canonicalOwnerIssueUpdate{Plan: next, ExpectedAssignmentEpoch: current.AssignmentEpoch}
	payload.TaskDispositions = []protocol.IssueTaskDisposition{{TaskID: task, Action: protocol.WorkItemTaskContinueExisting, SpecVersion: 1, Reason: "Original bounded contract remains useful"}}
	updated := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.update", protocol.Target{Type: "issue", ID: current.IssueID}, payload, rev(current.Revision)), CanonicalOwnerIssueCommand{Action: "update"}))
	if updated.Plan.Scope != next.Scope || !reflect.DeepEqual(updated.Plan.CompletionCriteria, next.CompletionCriteria) {
		t.Fatal(updated)
	}
	if !reflect.DeepEqual(h.storedSpec(task, 1), savedSpec) {
		t.Fatal("plan edit rewrote frozen spec")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		old, err := store.GetCanonicalIssueRevision(t.Context(), tx.Q(), issue.IssueID, 1)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(old.Plan, plan) {
			t.Fatal("immutable definition overwritten", old.Plan)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
