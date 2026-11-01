package app

import (
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func TestAttentionActionablePaginationRetainsArchivedObligations(t *testing.T) {
	h := newHarness(t)
	archived := true
	for i := 0; i < 50; i++ {
		a := attentionForTest(t, h, protocol.CreateAttentionPayload{Kind: "information", Title: "Earlier notification"})
		op := h.exec(owner, h.env("attentions.present", protocol.Target{Type: "attention", ID: a.AttentionID}, protocol.PresentAttentionPayload{Archived: &archived}, rev(a.Revision)), AttentionCommand{Action: "present"})
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		attentionForTest(t, h, protocol.CreateAttentionPayload{Kind: "information", Title: "Unread notification"})
	}
	action := attentionForTest(t, h, protocol.CreateAttentionPayload{Kind: "action", Title: "Outstanding owner action"})
	if op := h.exec(owner, h.env("attentions.present", protocol.Target{Type: "attention", ID: action.AttentionID}, protocol.PresentAttentionPayload{Archived: &archived}, rev(action.Revision)), AttentionCommand{Action: "present"}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	decision := attentionForTest(t, h, protocol.CreateAttentionPayload{Kind: "decision", Title: "Outstanding owner decision", Options: []string{"Proceed"}})
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		filter := views.AttentionFilter{Status: "open", IncludeArchived: true, ActionableOnly: true, Limit: 1}
		first, err := views.Attentions(t.Context(), tx.Q(), filter)
		if err != nil {
			return err
		}
		if len(first.Items) != 1 || first.Items[0].AttentionID != action.AttentionID || !first.More || first.Next == "" {
			t.Fatalf("notification displaced the archived owner action: %+v", first)
		}
		filter.After = first.Next
		second, err := views.Attentions(t.Context(), tx.Q(), filter)
		if err != nil {
			return err
		}
		if len(second.Items) != 1 || second.Items[0].AttentionID != decision.AttentionID || second.More {
			t.Fatalf("actionable cursor lost the owner decision: %+v", second)
		}
		notices, err := views.Attentions(t.Context(), tx.Q(), views.AttentionFilter{Kind: "information", Status: "open"})
		if err != nil {
			return err
		}
		if len(notices.Items) != 50 || notices.More {
			t.Fatalf("notification filter lost unarchived notices: %+v", notices)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAttentionCurrentSpecDeliverySurvivesChangesRequired(t *testing.T) {
	h := newHarness(t)
	task, acceptance := h.toAcceptanceClaim()
	if op := h.runAs(acceptance, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictChangesRequired}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	next, ok, _ := h.claim("linux-1")
	if !ok || next.Role != protocol.RoleImplement {
		t.Fatalf("missing next implementation: %+v", next)
	}
	h.runAs(next, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	if op := h.runAs(next, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "worker_failed"}, Release{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.task(task).CurrentArtifactID != "" {
		t.Fatal("fixture did not clear the rejected acceptance candidate")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		page, err := views.Attentions(t.Context(), tx.Q(), views.AttentionFilter{Status: "open", ActionableOnly: true})
		if err != nil {
			return err
		}
		found := false
		for _, a := range page.Items {
			if a.TaskID != task {
				continue
			}
			found = true
			if a.TaskContext == nil || !a.TaskContext.HasCurrentArtifact || a.TaskContext.CurrentArtifactAccepted {
				t.Fatalf("rejected delivery evidence was lost or shown as accepted: %+v", a.TaskContext)
			}
		}
		if !found {
			t.Fatal("missing failed implementation attention")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAttentionCurrentDeliveryAcceptanceIsProjected(t *testing.T) {
	h := newHarness(t)
	task, decision := h.failImplementation()
	h.answer(decision, "retry the implementation")
	reviewDecisionAnswer(t, h, decision.DecisionID)
	implementation, ok, _ := h.claim("linux-1")
	if !ok || implementation.Role != protocol.RoleImplement {
		t.Fatal("missing implementation retry", implementation)
	}
	h.runAs(implementation, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	if op := h.runAs(implementation, protocol.OpRunsSubmitArtifact, testArtifact(h.task(task).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	acceptance, ok, _ := h.claim("linux-1")
	if !ok || acceptance.Role != protocol.RoleAccept {
		t.Fatal("missing independent acceptance", acceptance)
	}
	if op := h.runAs(acceptance, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		page, err := views.Attentions(t.Context(), tx.Q(), views.AttentionFilter{})
		if err != nil {
			return err
		}
		found := false
		for _, view := range page.Items {
			if view.DecisionID != decision.DecisionID {
				continue
			}
			found = true
			if view.TaskContext == nil || !view.TaskContext.HasCurrentArtifact || !view.TaskContext.CurrentArtifactAccepted {
				t.Fatalf("current delivery acceptance is missing: %+v", view.TaskContext)
			}
		}
		if !found {
			t.Fatal("original failure question was lost")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAttentionContextPreservesUnknownExecution(t *testing.T) {
	h, assignment, _ := questionRun(t, protocol.RoleImplement)
	question := askQuestion(t, h, assignment, scopeQuestion)
	h.clock.Advance(DefaultLeaseTTL + time.Second)
	if n, err := h.app.ExpireLeases(t.Context()); err != nil || n != 1 {
		t.Fatalf("expire leases: %d %v", n, err)
	}
	if h.executionTask(assignment.TaskID).Execution.State != protocol.ExecutionUnknown {
		t.Fatal("fixture has no unknown execution")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		page, err := views.Attentions(t.Context(), tx.Q(), views.AttentionFilter{Status: "open"})
		if err != nil {
			return err
		}
		found := false
		for _, a := range page.Items {
			if a.DecisionID != question.DecisionID {
				continue
			}
			found = true
			if a.TaskContext == nil || a.TaskContext.Execution == nil || a.TaskContext.Execution.State != protocol.ExecutionUnknown {
				t.Fatalf("uncertain worker is presented as active: %+v", a.TaskContext)
			}
		}
		if !found {
			t.Fatal("missing original worker question")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAttentionQuestionCurrentFollowsControlRevision(t *testing.T) {
	h := newHarness(t)
	task, original := h.failImplementation()
	if op := h.requestAction(task, protocol.ActionImplement, 2); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	next, ok, _ := h.claim("linux-1")
	if !ok || next.Role != protocol.RoleImplement {
		t.Fatal("missing next implementation", next)
	}
	h.runAs(next, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	if op := h.runAs(next, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "worker_failed"}, Release{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	current := h.openDecision(task)
	if current.DecisionID == original.DecisionID {
		t.Fatal("fixture did not create a new failure question")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		page, err := views.Attentions(t.Context(), tx.Q(), views.AttentionFilter{Status: "open"})
		if err != nil {
			return err
		}
		found := 0
		for _, a := range page.Items {
			if a.DecisionID != original.DecisionID && a.DecisionID != current.DecisionID {
				continue
			}
			found++
			if a.TaskContext == nil || a.TaskContext.QuestionCurrent != (a.DecisionID == current.DecisionID) {
				t.Fatalf("question does not match its original revision: %+v", a)
			}
		}
		if found != 2 {
			t.Fatalf("expected both original and current open questions, got %d", found)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
