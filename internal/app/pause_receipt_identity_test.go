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
