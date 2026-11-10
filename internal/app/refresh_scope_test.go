package app

import (
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"strings"
	"testing"
)

func TestLocalIssueWriteDoesNotRefreshUnrelatedManager(t *testing.T) {
	h, _ := managerClaimHarness(t)
	local := createCanonicalOwnerTestIssue(t, h)
	other := createCanonicalOwnerTestIssue(t, h)
	op := h.exec(owner, h.env(protocol.OpIssueRequestManager, protocol.Target{Type: "issue", ID: other.IssueID}, protocol.RequestIssueManagerPayload{ExpectedAssignmentEpoch: other.AssignmentEpoch, Reason: "Delegate unrelated responsibility"}, rev(other.Revision)), retainedManagerCommand{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	previous := other.Revision
	other.State = protocol.IssueClosed
	other.Closure = &protocol.IssueClosure{Kind: protocol.WorkItemClosureDeclined, Reason: "Retained terminal projection awaiting refresh"}
	other.Revision++
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		return tx.SaveCanonicalIssue(t.Context(), other, previous, store.IssueRevisionProvenance{Actor: owner, RequestID: h.ids.New(protocol.PrefixRequest)})
	}); err != nil {
		t.Fatal(err)
	}

	// Turn a visit to the unrelated manager into an observable failure. A local
	// update must succeed, while an explicit global refresh must still visit it.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `CREATE TRIGGER reject_unrelated_refresh BEFORE UPDATE ON issue_manager_tasks WHEN OLD.issue_id='`+other.IssueID+`' BEGIN SELECT RAISE(ABORT,'unrelated manager visited'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	plan := local.Plan
	plan.Intent = "Clarify only the local responsibility"
	op = h.exec(owner, h.env("issues.update", protocol.Target{Type: "issue", ID: local.IssueID}, protocol.UpdateIssuePayload{Plan: plan, ExpectedAssignmentEpoch: local.AssignmentEpoch}, rev(local.Revision)), CanonicalOwnerIssueCommand{Action: "update"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		rows, err := tx.Q().QueryContext(t.Context(), `EXPLAIN QUERY PLAN SELECT task_id FROM issue_manager_tasks WHERE state IN ('pending','assigned','retiring') AND issue_id=? ORDER BY task_id`, other.IssueID)
		if err != nil {
			return err
		}
		defer rows.Close()
		indexed := false
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				return err
			}
			indexed = indexed || strings.Contains(detail, "SEARCH issue_manager_tasks")
		}
		if !indexed {
			t.Fatal("scoped refresh scans manager history")
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}

	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		return refreshManagerTasks(t.Context(), &Exec{Env: &protocol.Envelope{}, IDs: h.ids, Now: h.clock.Now()}, tx)
	}); err == nil {
		t.Fatal("global scheduler skipped unrelated manager")
	}
}
