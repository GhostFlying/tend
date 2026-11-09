package app

import (
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestFault_ClosingNativeOwnerRetainsOriginalThreadBinding(t *testing.T) {
	h, runtime, _ := issueOwnerHarness(t)
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityIssueOwnerNativeTools)
	runtime.Sequence++
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	issue := createCanonicalOwnerTestIssue(t, h)
	op := h.exec(owner, h.env(protocol.OpIssueRequestOwner, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.RequestIssueManagerPayload{CategoryID: "implementer", AgentID: "linux-1/alpha", ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Continue the negotiated owner execution"}, rev(issue.Revision)), RequestIssueOwner{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	as := ownerClaim(t, h, runtime)
	for range 5 {
		if as.Manager != nil && as.Manager.IssueID == issue.IssueID {
			break
		}
		if as.Manager == nil {
			t.Fatal(as)
		}
		stopOwnerClaim(t, h, as)
		as = ownerClaim(t, h, runtime)
	}
	if as.Manager == nil || as.Manager.IssueID != issue.IssueID || !as.Manager.NativeOwnerTools {
		t.Fatal("fixture did not claim the negotiated owner", as)
	}
	before, named, err := h.app.HostedRuntimeSelection(t.Context(), as.Manager.ConversationID)
	if err != nil || !named || !before.NativeOwnerTools {
		t.Fatal(before, named, err)
	}
	instance := before.BindingInstance("coordinator")
	if err := h.app.SetMainThread(t.Context(), instance, "original-thread"); err != nil {
		t.Fatal(err)
	}
	source := queueMessage(t, h, "Preserve the original admitted input")
	business := h.ids.New(protocol.PrefixRequest)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.PutMainTurn(t.Context(), store.MainTurn{RequestID: business, MessageID: source, ThreadID: "original-thread", TurnID: "original-turn", Status: store.TurnStarted}, h.clock.Now().UnixMilli()); err != nil {
			return err
		}
		return tx.InsertHostedExecution(t.Context(), store.HostedExecution{ID: business, ConversationID: as.Manager.ConversationID, IssueID: issue.IssueID, AssignmentEpoch: before.AssignmentEpoch, Selection: before, CreatedAt: h.clock.Now().UnixMilli()})
	}); err != nil {
		t.Fatal(err)
	}
	close, err := h.app.QueueManagerNative(t.Context(), as.RunID, "close", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, frozen := range []bool{false, true} {
		t.Run(map[bool]string{false: "current_selection", true: "frozen_startup"}[frozen], func(t *testing.T) {
			selected, named := before, false
			var err error
			if frozen {
				var attachment AgentStartupAttachment
				selected, named, attachment, err = h.app.HostedRuntimeStartupSelection(t.Context(), as.Manager.ConversationID)
				if err == nil && (!attachment.Reconciliation || attachment.RequestID != business) {
					t.Fatal("startup did not select the original pending turn", attachment)
				}
			} else {
				err = h.store.Read(t.Context(), func(tx *store.ReadTx) error {
					manager, err := store.CurrentIssueManagerTask(t.Context(), tx.Q(), issue.IssueID)
					if err == nil {
						selected, named, err = runnerManagerSelection(t.Context(), tx.Q(), manager)
					}
					return err
				})
			}
			if err != nil || !named || !selected.ReceiptOnly {
				t.Fatal("closing run did not retain a receipt-only attachment", selected, named, err)
			}
			if !selected.NativeOwnerTools || selected.BindingInstance("coordinator") != instance {
				t.Fatal("temporary fencing changed the original native thread identity", selected.NativeOwnerTools, selected.BindingInstance("coordinator"), instance)
			}
			thread, exists, err := h.app.MainThread(t.Context(), selected.BindingInstance("coordinator"))
			if err != nil || !exists || thread != "original-thread" {
				t.Fatal("receipt attachment lost its original native thread", thread, exists, err)
			}
		})
	}
	if _, err := h.app.QueueManagerNative(t.Context(), as.RunID, "start", []byte(`{}`)); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("stable identity widened fenced native execution", err)
	}
	if _, ready, err := h.app.ManagerNativeResult(t.Context(), as.RunID, close); err != nil || ready {
		t.Fatal("attachment fabricated the unresolved close result", ready, err)
	}
}

func TestIssueOwnerNativeToolsAreExplicitlyNegotiated(t *testing.T) {
	h, runtime, issue := issueOwnerHarness(t)
	read := func(want bool) {
		t.Helper()
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			m, err := store.CurrentIssueManagerTask(t.Context(), tx.Q(), issue.IssueID)
			if err != nil {
				return err
			}
			selected, named, err := runnerManagerSelection(t.Context(), tx.Q(), m)
			if err != nil {
				return err
			}
			if !named || selected.NativeOwnerTools != want {
				t.Fatal("selection guessed native tools", selected)
			}
			runs, err := store.ListRunsByTask(t.Context(), tx.Q(), m.TaskID)
			if err != nil {
				return err
			}
			if len(runs) != 1 {
				t.Fatal(runs)
			}
			run := runs[0]
			lease, err := store.GetLeaseByRun(t.Context(), tx.Q(), run.RunID)
			if err != nil {
				return err
			}
			as, err := buildManagerAssignment(t.Context(), tx.Q(), run, lease, h.app.leaseTTL)
			if err == nil && (as.Manager == nil || as.Manager.NativeOwnerTools != want) {
				t.Fatal("assignment mode differs", as.Manager)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	read(false)
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityIssueOwnerNativeTools)
	runtime.Sequence++
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	// Advertising a new binary cannot widen an already frozen old Run.
	read(false)
	issue = createCanonicalOwnerTestIssue(t, h)
	op := h.exec(owner, h.env(protocol.OpIssueRequestOwner, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.RequestIssueManagerPayload{CategoryID: "implementer", AgentID: "linux-1/alpha", ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Start a new owner turn with the negotiated native adapter"}, rev(issue.Revision)), RequestIssueOwner{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	as := ownerClaim(t, h, runtime)
	for range 5 {
		if as.Manager != nil && as.Manager.IssueID == issue.IssueID {
			break
		}
		if as.Manager == nil {
			t.Fatal(as)
		}
		stopOwnerClaim(t, h, as)
		as = ownerClaim(t, h, runtime)
	}
	if as.Manager == nil || as.Manager.IssueID != issue.IssueID {
		t.Fatal("new responsibility was not claimed", as)
	}
	if as.Manager == nil || !as.Manager.NativeOwnerTools {
		t.Fatal("new Run did not freeze the negotiated mode", as.Manager)
	}
	read(true)
}
