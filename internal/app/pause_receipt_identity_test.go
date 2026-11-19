package app

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

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

func TestFirstPauseReceiptPreservesNegotiatedHandbackBehavior(t *testing.T) {
	for _, tc := range []struct {
		name     string
		deferral bool
		draining bool
	}{
		{name: "legacy primary"},
		{name: "capable primary", deferral: true},
		{name: "legacy draining behind capable primary", draining: true},
		{name: "capable draining behind legacy primary", deferral: true, draining: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, runtime := pendingIssueOwnerHarness(t)
			runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityPauseHandoffs)
			if tc.deferral {
				runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityPauseReceiptDeferral)
			}
			runtime.Sequence++
			if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
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
			if tc.draining {
				ctx := t.Context()
				err := h.store.Write(ctx, func(tx *store.Tx) error {
					original, err := store.GetHostRuntime(ctx, tx.Q(), run.HostID)
					if err != nil {
						return err
					}
					const upgrade = "rolling-upgrade"
					if err := tx.PutUpgrade(ctx, store.UpgradeRow{View: protocol.UpgradeView{
						UpgradeID: upgrade, ReleaseID: "test-release", Revision: 1,
						Status: "completed", Phase: "completed", CreatedAt: h.clock.Now(), UpdatedAt: h.clock.Now(),
					}}); err != nil {
						return err
					}
					if err := tx.PreserveDrainingRunner(ctx, run.HostID, upgrade, original, protocol.RunnerControl{Mode: protocol.RunnerMaintenance}); err != nil {
						return err
					}
					next := original
					next.Runtime.InstanceID = "replacement-instance"
					next.Runtime.JournalID = "replacement-journal"
					next.Runtime.Capabilities = []string{protocol.CapabilityPauseHandoffs}
					if !tc.deferral {
						next.Runtime.Capabilities = append(next.Runtime.Capabilities, protocol.CapabilityPauseReceiptDeferral)
					}
					return tx.PutHostRuntime(ctx, run.HostID, next)
				})
				if err != nil {
					t.Fatal(err)
				}
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
			first := h.exec(principal, env, YieldPause{})
			if tc.deferral {
				if first.Status != protocol.OpFailed || first.Error == nil || first.Error.Code != protocol.CodeInvalidTransition ||
					!reflect.DeepEqual(h.run(as.RunID), heldRun) || h.lease(as.RunID) != heldLease {
					t.Fatal("first receipt changed the Run during owner control", first)
				}
			} else if first.Status != protocol.OpSucceeded || h.run(as.RunID).ObservedState != protocol.RunFinished || h.lease(as.RunID).Status != store.LeaseReleased {
				t.Fatal("legacy runner could not report its stopped original process", first)
			}
			actions := h.count("plan_actions")
			issueAdmissionStatus(t, h, admissionCurrent(t, h, issue.IssueID), protocol.IssueTodo, nil)
			if h.count("plan_actions") != actions {
				t.Fatal("physical stop receipt scheduled a successor during owner control")
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
		})
	}
}

func TestPauseControlHelloPreservesLapsedLease(t *testing.T) {
	h, runtime := pendingIssueOwnerHarness(t)
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityPauseHandoffs)
	i := issueAdmissionStatus(t, h, createBacklogTestIssue(t, h), protocol.IssueTodo, &protocol.IssueOwnerRequest{CategoryID: "implementer"})
	as := admissionClaim(t, h, runtime)
	if as == nil || !as.PauseHandoffs {
		t.Fatalf("no negotiated pause assignment: %+v", as)
	}
	issueAdmissionStatus(t, h, admissionCurrent(t, h, i.IssueID), protocol.IssueBacklog, nil)
	h.clock.Advance(DefaultLeaseTTL + time.Second)
	if _, err := h.app.ExpireLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	original := h.lease(as.RunID)
	paused := h.run(as.RunID)
	if paused.ObservedState != protocol.RunUnknown || paused.PauseHandoff == nil || original.Status != store.LeaseLapsed {
		t.Fatalf("fixture did not reach lapsed pause: run=%s lease=%s", h.run(as.RunID).ObservedState, original.Status)
	}
	for _, control := range []struct {
		mode protocol.ControlMode
		want protocol.RunDesired
	}{{protocol.ControlHumanControl, protocol.DesiredHeld}, {protocol.ControlManaged, protocol.DesiredPaused}} {
		task := h.task(as.TaskID)
		op := h.exec(owner, h.env("control.set_mode", taskTarget(as.TaskID), protocol.SetControlModePayload{Mode: control.mode}, rev(task.ControlRevision)), SetControlMode{})
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		runtime.Sequence++
		p := protocol.HelloPayload{Runtime: runtime, WorkspaceProtocol: 1, Held: []protocol.HeldRun{{RunID: as.RunID, LeaseID: as.LeaseID, Observed: protocol.RunUnknown}}}
		op = h.exec(runner("linux-1"), h.env(protocol.OpRunnerHello, protocol.Target{Type: "host", ID: "linux-1"}, p, nil), Hello{})
		var reply protocol.HelloResult
		if op.Status != protocol.OpSucceeded || json.Unmarshal(op.Result, &reply) != nil || len(reply.Runs) != 1 ||
			reply.Runs[0].Desired != control.want || reply.Runs[0].PauseHandoff == nil ||
			*reply.Runs[0].PauseHandoff != *paused.PauseHandoff {
			t.Fatalf("Hello did not return %s with the original pause: %+v, %+v", control.want, op, reply)
		}
		if got := h.lease(as.RunID); got != original || h.run(as.RunID).ObservedState != protocol.RunUnknown {
			t.Fatalf("control-only Hello changed the original lease or Run: lease=%+v, want=%+v, observed=%s", got, original, h.run(as.RunID).ObservedState)
		}
	}
}
