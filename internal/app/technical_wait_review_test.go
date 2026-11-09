package app

import (
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestStoppedResponsibilityAdoptsExistingUnclaimedContinuation(t *testing.T) {
	f, issue := stoppedOwnerContinuation(t)
	env, cmd := f.resumeEnvelope(t, false)
	if op := f.h.exec(owner, env, cmd); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	queued := f.view(t)
	if queued.State != "continued" || queued.ContinuationActionID == "" || queued.SuccessorRunID != "" {
		t.Fatal("no pending continuation", queued)
	}
	// An already valid continuation is a real no-op, not another adoption/action.
	again, cmd := f.resumeEnvelope(t, false)
	if op := f.h.exec(owner, again, cmd); op.Status != protocol.OpSucceeded || f.h.count("task_execution_responsibility_adoptions") != 0 {
		t.Fatal("valid continued request gained a new adoption", op)
	}
	issue = replaceContinuationManager(t, f, issue)
	v := f.view(t)
	if v.State != "continued" || !v.ResponsibilityAdoptionRequired {
		t.Fatal("fixture missed queued old responsibility", v)
	}
	payload := protocol.ResumeDecisionContinuationPayload{ExpectedRevision: v.Revision, ExpectedAssignmentEpoch: rev(issue.AssignmentEpoch), Reason: "Continue the exact unclaimed action after management recovery"}
	env = f.h.env(protocol.OpResumeDecisionContinuation, protocol.Target{Type: "decision", ID: f.q.DecisionID}, payload, rev(v.TaskControlRevision))
	op := f.h.exec(owner, env, ResumeDecisionContinuation{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	after := f.view(t)
	if after.ResponsibilityAdoptionRequired || after.ContinuationActionID != queued.ContinuationActionID || f.h.count("task_execution_responsibility_adoptions") != 1 {
		t.Fatal("successful resume did not adopt the existing action", queued, after)
	}
	next := ownerWorkerClaim(t, f.h, f.runtime, protocol.RoleImplement)
	if next.ActionID != queued.ContinuationActionID || next.TaskID != f.task || next.Generation != f.as.Generation || next.Responsibility.AssignmentEpoch != issue.AssignmentEpoch {
		t.Fatal("adoption replaced or failed to claim the queued action", next)
	}
	if replay := f.h.exec(owner, env, ResumeDecisionContinuation{}); !reflect.DeepEqual(replay, op) {
		t.Fatal("adoption receipt changed after actual claim", replay, op)
	}
}

func TestStoppedResponsibilityUsesTaskWorkerPin(t *testing.T) {
	for _, tc := range []struct {
		name           string
		split, managed bool
	}{{"split_runtime", true, false}, {"task_category", false, true}, {"split_runtime_and_category", true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			h, r, issue, _, _ := ownerExtensionHarness(t, tc.split, tc.managed, tc.managed)
			f, issue := stoppedOwnerContinuationOn(t, h, r, issue, "reviewer")
			issue = replaceContinuationManager(t, f, issue)
			v := f.view(t)
			payload := protocol.ResumeDecisionContinuationPayload{ExpectedRevision: v.Revision, ExpectedAssignmentEpoch: rev(issue.AssignmentEpoch), Reason: "Keep the unchanged actual Task worker configuration"}
			op := h.exec(owner, h.env(protocol.OpResumeDecisionContinuation, protocol.Target{Type: "decision", ID: f.q.DecisionID}, payload, rev(v.TaskControlRevision)), ResumeDecisionContinuation{})
			if op.Status != protocol.OpSucceeded {
				t.Fatal("supported worker configuration was rejected", op)
			}
			next := ownerWorkerClaim(t, h, r, protocol.RoleImplement)
			if next.TaskID != f.task || next.Execution == nil || *next.Execution != *f.as.Execution {
				t.Fatal("actual worker pin changed", next, f.as.Execution)
			}
		})
	}
}

func TestStoppedResponsibilityDoesNotAdoptClaimedContinuation(t *testing.T) {
	for _, scenario := range []string{"finished_successor", "unknown_successor", "dispatched_without_receipt"} {
		t.Run(scenario, func(t *testing.T) {
			f, issue := stoppedOwnerContinuation(t)
			env, cmd := f.resumeEnvelope(t, false)
			if op := f.h.exec(owner, env, cmd); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			queued := f.view(t)
			if scenario != "dispatched_without_receipt" {
				next := ownerWorkerClaim(t, f.h, f.runtime, protocol.RoleImplement)
				f.h.runAs(next, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
				if op := f.h.runAs(next, protocol.OpRunsSubmitArtifact, testArtifact(f.h.task(f.task).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			issue = replaceContinuationManager(t, f, issue)
			if scenario == "unknown_successor" || scenario == "dispatched_without_receipt" {
				if err := f.h.store.Write(t.Context(), func(tx *store.Tx) error {
					if scenario == "unknown_successor" {
						v := f.view(t)
						return tx.UpdateRunObserved(t.Context(), v.SuccessorRunID, protocol.RunUnknown, f.h.clock.Now().UnixMilli())
					}
					return tx.SetActionStatus(t.Context(), queued.ContinuationActionID, protocol.ActionDispatched, f.h.clock.Now().UnixMilli())
				}); err != nil {
					t.Fatal(err)
				}
			}
			v := f.view(t)
			p := protocol.ResumeDecisionContinuationPayload{ExpectedRevision: v.Revision, ExpectedAssignmentEpoch: rev(issue.AssignmentEpoch), Reason: "Do not pretend an already dispatched continuation is unclaimed"}
			op, err := f.h.app.Execute(t.Context(), owner, f.h.env(protocol.OpResumeDecisionContinuation, protocol.Target{Type: "decision", ID: f.q.DecisionID}, p, rev(v.TaskControlRevision)), ResumeDecisionContinuation{})
			if code := technicalWaitErrorCode(op, err); code != protocol.CodeUnknownOutcome {
				t.Fatal("claimed or uncertain successor was treated as safe adoption", op, err)
			}
			if f.h.count("task_execution_responsibility_adoptions") != 0 {
				t.Fatal("failed adoption left a responsibility record")
			}
			if got := f.view(t); got.ContinuationActionID != queued.ContinuationActionID {
				t.Fatal("failed adoption replaced the original action", got)
			}
		})
	}
}
