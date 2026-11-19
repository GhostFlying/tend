package app

import (
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestCommittedPauseReceiptRejectsChangedScope(t *testing.T) {
	h, runtime := pendingIssueOwnerHarness(t)
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityPauseHandoffs)
	issue := issueAdmissionStatus(t, h, createBacklogTestIssue(t, h), protocol.IssueTodo, &protocol.IssueOwnerRequest{CategoryID: "implementer"})
	as := admissionClaim(t, h, runtime)
	if as == nil {
		t.Fatal("execution was not assigned")
	}
	issueAdmissionStatus(t, h, admissionCurrent(t, h, issue.IssueID), protocol.IssueBacklog, nil)
	paused := h.run(as.RunID)
	if paused.PauseHandoff == nil {
		t.Fatal("pause handoff was not recorded")
	}
	original := protocol.YieldPausePayload{PauseHandoff: *paused.PauseHandoff, ThreadID: "original-native-thread"}
	if op := h.runAs(*as, protocol.OpRunsYieldPause, original, YieldPause{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	beforeRun, beforeLease, beforeActions := h.run(as.RunID), h.lease(as.RunID), h.count("plan_actions")
	for _, field := range []string{"thread", "issue", "revision", "control", "role"} {
		t.Run(field, func(t *testing.T) {
			payload := original
			principal := runPrincipal(as.RunID, as.Role)
			switch field {
			case "thread":
				payload.ThreadID = "another-thread"
			case "issue":
				payload.IssueID = h.ids.New(protocol.PrefixWorkItem)
			case "revision":
				payload.IssueRevision++
			case "control":
				payload.ControlRevision++
			case "role":
				principal.Role = protocol.RoleAccept
			}
			op, err := h.app.Execute(t.Context(), principal, h.env(protocol.OpRunsYieldPause, runTarget(as.RunID), payload, nil), YieldPause{})
			if boundedReleaseCode(op, err) != protocol.CodeInvalidTransition {
				t.Fatal("changed proof was accepted", op, err)
			}
			if !reflect.DeepEqual(h.run(as.RunID), beforeRun) || h.lease(as.RunID) != beforeLease || h.count("plan_actions") != beforeActions || h.lease(as.RunID).Status != store.LeaseReleased {
				t.Fatal("rejected proof changed the settled execution")
			}
		})
	}
}

func TestFirstPauseReceiptWaitsForOwnerHandback(t *testing.T) {
	h, runtime := pendingIssueOwnerHarness(t)
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityPauseHandoffs)
	issue := issueAdmissionStatus(t, h, createBacklogTestIssue(t, h), protocol.IssueTodo, &protocol.IssueOwnerRequest{CategoryID: "implementer"})
	as := admissionClaim(t, h, runtime)
	if as == nil {
		t.Fatal("execution was not assigned")
	}
	issueAdmissionStatus(t, h, admissionCurrent(t, h, issue.IssueID), protocol.IssueBacklog, nil)
	run := h.run(as.RunID)
	if run.PauseHandoff == nil {
		t.Fatal("pause handoff was not recorded")
	}
	env := h.env(protocol.OpRunsYieldPause, runTarget(as.RunID), protocol.YieldPausePayload{PauseHandoff: *run.PauseHandoff, ThreadID: "original-native-thread"}, nil)
	setControl := func(mode protocol.ControlMode) {
		t.Helper()
		task := h.task(as.TaskID)
		op := h.exec(owner, h.env("control.set_mode", taskTarget(as.TaskID), protocol.SetControlModePayload{Mode: mode}, rev(task.ControlRevision)), SetControlMode{})
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	setControl(protocol.ControlHumanControl)
	heldRun, heldLease := h.run(as.RunID), h.lease(as.RunID)
	principal := runPrincipal(as.RunID, as.Role)
	rejected := h.exec(principal, env, YieldPause{})
	if rejected.Status != protocol.OpFailed || rejected.Error == nil ||
		rejected.Error.Code != protocol.CodeInvalidTransition ||
		!reflect.DeepEqual(h.run(as.RunID), heldRun) || h.lease(as.RunID) != heldLease {
		t.Fatal("first receipt changed the Run during owner control", rejected)
	}
	setControl(protocol.ControlManaged)
	if op := h.exec(principal, env, YieldPause{}); op.Status != protocol.OpSucceeded {
		t.Fatal("handback did not admit the original receipt", op)
	}
	setControl(protocol.ControlHumanControl)
	if op := h.exec(principal, env, YieldPause{}); op.Status != protocol.OpSucceeded ||
		h.lease(as.RunID).Status != store.LeaseReleased {
		t.Fatal("owner control hid the acknowledgement of a prior commit", op)
	}
}
