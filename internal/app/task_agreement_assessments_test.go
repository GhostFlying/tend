package app

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/scheduler"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func agreementFixture(t *testing.T, h *harness, issue protocol.Issue) (string, string) {
	t.Helper()
	task := canonicalTaskReadFixture(t, h, issue)
	var raw []byte
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		spec := testSpec
		if err := refreshTaskWorkContext(t.Context(), tx.Q(), task, &spec, nil); err != nil {
			return err
		}
		var err error
		raw, err = json.Marshal(spec)
		if err != nil {
			return err
		}
		if err := tx.InsertSpec(t.Context(), task, 2, string(raw), "", 2); err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `UPDATE tasks SET spec_version=2 WHERE task_id=?`, task)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return task, string(raw)
}

func appendAgreementSource(t *testing.T, h *harness, issue string) int64 {
	t.Helper()
	var revision int64
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		message := h.ids.New(protocol.PrefixMessage)
		if _, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: message, Author: "owner", Body: "Git is repaired; retry the original task without changing its scope"}); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO work_item_context_facts(fact_id,work_item_id,project,scope,kind,body,source_message_id,attribution,created_at) VALUES(?,?,'default','work_item','authorization','Original task may retry with unchanged scope',?,'owner',1)`, h.ids.New(protocol.Prefix("fact")), issue, message); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE work_item_contexts SET revision=revision+1 WHERE work_item_id=?`, issue); err != nil {
			return err
		}
		// The fixture represents a source already read by its responsible owner.
		if _, err := tx.Exec(t.Context(), `UPDATE message_context_obligations SET needs_review=0 WHERE work_item_id=?`, issue); err != nil {
			return err
		}
		return tx.Q().QueryRowContext(t.Context(), `SELECT revision FROM work_item_contexts WHERE work_item_id=?`, issue).Scan(&revision)
	}); err != nil {
		t.Fatal(err)
	}
	return revision
}

func assertAgreementAdmission(t *testing.T, h *harness, issue, task string, pending bool) {
	t.Helper()
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		candidates := []scheduler.Candidate{{TaskID: task, SpecVersion: 2, Kind: protocol.ActionImplement, DependenciesDone: true}}
		if err := views.ApplyIssueDependencies(t.Context(), tx.Q(), candidates); err != nil {
			return err
		}
		if candidates[0].ContextPending != pending {
			t.Fatalf("ordinary source unexpectedly gated execution: candidate=%+v want pending=%v", candidates[0], pending)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTaskAgreementAssessmentRecordsJudgementWithoutGatingNewSources(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	task, original := agreementFixture(t, h, issue)
	revision := appendAgreementSource(t, h, issue.IssueID)
	assertAgreementAdmission(t, h, issue.IssueID, task, false)
	payload := protocol.UpdateIssuePayload{Plan: issue.Plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch, ExpectedContextRevision: rev(revision), TaskDispositions: []protocol.IssueTaskDisposition{{TaskID: task, SpecVersion: 2, ExpectedControlRevision: rev(1), Action: protocol.WorkItemTaskContinueExisting, Reason: "The repair permits the original agreement; no requirement changed"}}}
	env := h.env("issues.update", protocol.Target{Type: "issue", ID: issue.IssueID}, payload, rev(issue.Revision))
	op := h.exec(owner, env, CanonicalOwnerIssueCommand{Action: "update"})
	if op.Status != protocol.OpSucceeded || h.count("task_agreement_assessments") != 1 {
		t.Fatal(op)
	}
	assertAgreementAdmission(t, h, issue.IssueID, task, false)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE tasks SET implement_generation=implement_generation+1 WHERE task_id=?`, task)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	assertAgreementAdmission(t, h, issue.IssueID, task, false)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		raw, err := store.GetSpec(t.Context(), tx.Q(), task, 2)
		if err != nil {
			return err
		}
		row, err := store.GetTask(t.Context(), tx.Q(), task)
		if err != nil {
			return err
		}
		if raw != original || row.SpecVersion != 2 || row.ControlRevision != 1 {
			t.Fatal("assessment rewrote the task contract", row)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	appendAgreementSource(t, h, issue.IssueID)
	assertAgreementAdmission(t, h, issue.IssueID, task, false)
	replay := h.exec(owner, env, CanonicalOwnerIssueCommand{Action: "update"})
	if replay.Status != protocol.OpFailed || protocol.CodeOf(replay.Error) != protocol.CodeStaleRevision || h.count("task_agreement_assessments") != 1 {
		t.Fatal("replay refreshed an old judgement", replay)
	}
	assertAgreementAdmission(t, h, issue.IssueID, task, false)
}

func TestTaskAgreementAssessmentRejectsStaleAndForeignResponsibility(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	other := createCanonicalOwnerTestIssue(t, h)
	task, _ := agreementFixture(t, h, issue)
	contextRevision := appendAgreementSource(t, h, issue.IssueID)
	payload := protocol.UpdateIssuePayload{Plan: issue.Plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch, ExpectedContextRevision: rev(contextRevision), TaskDispositions: []protocol.IssueTaskDisposition{{TaskID: task, SpecVersion: 2, ExpectedControlRevision: rev(1), Action: protocol.WorkItemTaskContinueExisting, Reason: "Original requirements remain valid"}}}
	command := CanonicalOwnerIssueCommand{Action: "update"}
	for _, kind := range []string{"task", "spec", "context", "foreign", "updated", "stopped"} {
		t.Run(kind, func(t *testing.T) {
			bad := payload
			bad.TaskDispositions = append([]protocol.IssueTaskDisposition(nil), payload.TaskDispositions...)
			target := issue
			switch kind {
			case "task":
				bad.TaskDispositions[0].ExpectedControlRevision = rev(99)
			case "spec":
				bad.TaskDispositions[0].SpecVersion = 1
			case "context":
				bad.ExpectedContextRevision = rev(contextRevision - 1)
			case "updated":
				bad.TaskDispositions[0].Action = protocol.WorkItemTaskUpdated
			case "stopped":
				bad.TaskDispositions[0].Action = protocol.WorkItemTaskStopped
			case "foreign":
				target = other
				bad.Plan = other.Plan
				var current int64
				if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
					return tx.Q().QueryRowContext(t.Context(), `SELECT revision FROM work_item_contexts WHERE work_item_id=?`, other.IssueID).Scan(&current)
				}); err != nil {
					t.Fatal(err)
				}
				bad.ExpectedContextRevision = rev(current)
			}
			op, err := h.app.Execute(t.Context(), owner, h.env(command.Operation(), protocol.Target{Type: "issue", ID: target.IssueID}, bad, rev(target.Revision)), command)
			if (err == nil && op.Status == protocol.OpSucceeded) || h.count("task_agreement_assessments") != 0 {
				t.Fatal("invalid assessment committed", op, err)
			}
		})
	}
}

func TestCanonicalDefinitionChangeWaitsForOwnerJudgementWithoutInventingDisposition(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	task, original := agreementFixture(t, h, issue)
	plan := issue.Plan
	plan.Scope = "Clarified responsibility; the owner will decide task impact"
	op := canonicalLifecycleControl(t, h, issue, "update", protocol.UpdateIssuePayload{Plan: plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch})
	if op.Status != protocol.OpSucceeded || h.count("task_agreement_assessments") != 0 {
		t.Fatal(op)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		raw, err := store.GetSpec(t.Context(), tx.Q(), task, 2)
		if raw != original {
			t.Fatal("definition edit rewrote frozen spec")
		}
		var frozen domain.Spec
		if err := json.Unmarshal([]byte(raw), &frozen); err != nil {
			return err
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTaskAgreementAssessmentRequiresInheritedVersionsAndCurrentOwner(t *testing.T) {
	h := newHarness(t)
	parent := createCanonicalOwnerTestIssue(t, h)
	child := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, canonicalOwnerIssueCreate{Plan: parent.Plan, ParentIssueID: parent.IssueID}, nil), CanonicalOwnerIssueCommand{Action: "create"}))
	task, _ := agreementFixture(t, h, child)
	parentRevision := appendAgreementSource(t, h, parent.IssueID)
	var childRevision int64
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT revision FROM work_item_contexts WHERE work_item_id=?`, child.IssueID).Scan(&childRevision)
	}); err != nil {
		t.Fatal(err)
	}
	p := protocol.UpdateIssuePayload{Plan: child.Plan, ExpectedAssignmentEpoch: child.AssignmentEpoch, ExpectedContextRevision: rev(childRevision), TaskDispositions: []protocol.IssueTaskDisposition{{TaskID: task, SpecVersion: 2, ExpectedControlRevision: rev(1), Action: protocol.WorkItemTaskContinueExisting, Reason: "The original task remains appropriate after reading its parent"}}}
	command := CanonicalOwnerIssueCommand{Action: "update"}
	target := protocol.Target{Type: "issue", ID: child.IssueID}
	expectWorkCode(t, h.exec(owner, h.env(command.Operation(), target, p, rev(child.Revision)), command), protocol.CodeStaleRevision)
	p.TaskDispositions[0].ContextRevisions = []protocol.LogicalContextReview{{WorkItemID: parent.IssueID, Revision: parentRevision}}
	if op := h.exec(owner, h.env(command.Operation(), target, p, rev(child.Revision)), command); op.Status != protocol.OpSucceeded || h.count("task_agreement_assessments") != 2 {
		t.Fatal(op)
	}
	check := func(want bool) {
		t.Helper()
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			candidates := []scheduler.Candidate{{TaskID: task, SpecVersion: 2, Kind: protocol.ActionImplement, DependenciesDone: true}}
			if err := views.ApplyIssueDependencies(t.Context(), tx.Q(), candidates); err != nil {
				return err
			}
			if candidates[0].ContextPending != want {
				t.Fatal("inherited facts unexpectedly gated dispatch", candidates)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	check(false)
	plan := parent.Plan
	plan.Title = "A metadata change, with the same task requirements"
	canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, parent, "update", protocol.UpdateIssuePayload{Plan: plan, ExpectedAssignmentEpoch: parent.AssignmentEpoch}))
	check(false)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		next, err := store.GetCanonicalIssue(t.Context(), tx.Q(), child.IssueID)
		if err != nil {
			return err
		}
		previous := next.Revision
		next.Revision++
		next.AssignmentEpoch++
		return tx.SaveCanonicalIssue(t.Context(), next, previous, store.IssueRevisionProvenance{Actor: owner, RequestID: h.ids.New(protocol.PrefixRequest), Reason: "Explicit fixture responsibility transition"})
	}); err != nil {
		t.Fatal(err)
	}
	check(false)
}

func TestTaskAgreementAssessmentPausedDispositionKeepsExecutionStopped(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	task, _ := agreementFixture(t, h, issue)
	appendAgreementSource(t, h, issue.IssueID)
	if op := h.exec(owner, h.env("control.set_mode", taskTarget(task), protocol.SetControlModePayload{Mode: protocol.ControlPaused}, rev(1)), SetControlMode{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var revision int64
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT revision FROM work_item_contexts WHERE work_item_id=?`, issue.IssueID).Scan(&revision)
	}); err != nil {
		t.Fatal(err)
	}
	p := protocol.UpdateIssuePayload{Plan: issue.Plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch, ExpectedContextRevision: rev(revision), TaskDispositions: []protocol.IssueTaskDisposition{{TaskID: task, SpecVersion: 2, ExpectedControlRevision: rev(h.task(task).ControlRevision), Action: protocol.WorkItemTaskStopped, Reason: "Sources reviewed; keep this task paused until a later explicit choice"}}}
	op := canonicalLifecycleControl(t, h, issue, "update", p)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	assertAgreementAdmission(t, h, issue.IssueID, task, false)
	if current := h.task(task); current.ControlMode != protocol.ControlPaused || current.SpecVersion != 2 {
		t.Fatal("assessment resumed or revised a stopped agreement", current)
	}
	if op := h.exec(owner, h.env("control.set_mode", taskTarget(task), protocol.SetControlModePayload{Mode: protocol.ControlManaged}, rev(h.task(task).ControlRevision)), SetControlMode{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	assertAgreementAdmission(t, h, issue.IssueID, task, false)
}

func TestHostedOwnerAssessesOriginalSpecThroughNormalCoordination(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	task, _ := agreementFixture(t, h, issue)
	appendAgreementSource(t, h, issue.IssueID)
	turn, principal := startHostedIssueReview(t, h, issue)
	if op := hostedEffect(t, h, principal, turn, 1, protocol.Target{Type: "issue", ID: issue.IssueID}, nil, hostedReviewPayload(t, h, issue), ReviewHostedIssueContext{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var revision int64
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT revision FROM work_item_contexts WHERE work_item_id=?`, issue.IssueID).Scan(&revision)
	}); err != nil {
		t.Fatal(err)
	}
	p := protocol.UpdateIssuePayload{Plan: issue.Plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch, ExpectedContextRevision: rev(revision), TaskDispositions: []protocol.IssueTaskDisposition{{TaskID: task, SpecVersion: 2, ExpectedControlRevision: rev(h.task(task).ControlRevision), Action: protocol.WorkItemTaskContinueExisting, Reason: "Read the new source; retain this original agreement"}}}
	op := hostedEffect(t, h, principal, turn, 2, protocol.Target{Type: "issue", ID: issue.IssueID}, rev(issue.Revision), p, CanonicalOwnerIssueCommand{Action: "update"})
	if op.Status != protocol.OpSucceeded || h.count("task_agreement_assessments") != 1 {
		t.Fatal(op)
	}
	assertAgreementAdmission(t, h, issue.IssueID, task, false)
}
