package app

import (
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

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
