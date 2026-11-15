package app

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestIdleClaimUsesReadConnectionWhileWriterIsOccupied(t *testing.T) {
	h := newHarness(t)
	h.hello("linux-1")
	env := h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{WorkspaceProtocol: 0}, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- h.store.Write(t.Context(), func(*store.Tx) error { close(entered); <-release; return nil })
	}()
	<-entered
	defer func() {
		close(release)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	op, err := h.app.Execute(ctx, runner("linux-1"), env, Claim{})
	if err != nil || op.Status != protocol.OpSucceeded || string(op.Result) != `{"assignment":null}` {
		t.Fatal("idle poll acquired busy writer", op, err)
	}
}

func TestIdleClaimUnmatchedInitialOwnerDoesNotAcquireWriter(t *testing.T) {
	h, r := pendingIssueOwnerHarness(t)
	if got := admissionClaim(t, h, r); got != nil {
		t.Fatal("empty warmup unexpectedly allocated", got)
	}
	if op := h.exec(owner, h.env("categories.put", protocol.Target{Type: "category", ID: "unmatched-owner"}, protocol.PutAgentCategoryPayload{Title: "Waiting for another category", State: "active"}, rev(0)), PutAgentCategory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	issue := issueAdmissionStatus(t, h, createBacklogTestIssue(t, h), protocol.IssueTodo, &protocol.IssueOwnerRequest{CategoryID: "unmatched-owner"})
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		for _, unknown := range []struct {
			host    string
			runtime *protocol.RunnerRuntime
		}{{"linux-1", nil}, {"missing-inventory", r}} {
			potential, err := initialOwnerPotentialForHost(t.Context(), tx.Q(), unknown.host, unknown.runtime)
			if err != nil {
				return err
			}
			if !potential {
				t.Fatal("missing inventory/negotiation was treated as exclusion")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	unblock := sync.OnceFunc(func() { close(release) })
	go func() {
		done <- h.store.Write(t.Context(), func(*store.Tx) error { close(entered); <-release; return nil })
	}()
	<-entered
	defer func() {
		unblock()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	env := h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{Runtime: r, WorkspaceProtocol: 1}, nil)
	op, err := h.app.Execute(ctx, runner("linux-1"), env, Claim{})
	if err != nil || op.Status != protocol.OpSucceeded || string(op.Result) != `{"assignment":null}` {
		t.Fatal("unmatched initial owner forced an idle poll into the busy writer", op, err)
	}
	if h.count("tasks") != 0 {
		t.Fatal("idle exclusion materialized an owner")
	}
	unblock()
	// A changed selection is read afresh and still traverses actual allocation.
	issueAdmissionStatus(t, h, issue, protocol.IssueTodo, &protocol.IssueOwnerRequest{CategoryID: "implementer"})
	if got := admissionClaim(t, h, r); got == nil || got.Manager == nil || got.Manager.CategoryID != "implementer" {
		t.Fatal("new matching selection was not claimable", got)
	}
}

func TestInitialOwnerPreflightRetainsPendingReceiptAndManagerMaintenance(t *testing.T) {
	h, r := pendingIssueOwnerHarness(t)
	if got := admissionClaim(t, h, r); got != nil {
		t.Fatal(got)
	}
	issue := createBacklogTestIssue(t, h)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.InsertTask(t.Context(), store.TaskRow{TaskID: "receipt-carrier", Kind: protocol.TaskKindIssueManager, Title: "Retained receipt", Lifecycle: protocol.TaskCompleted, ControlMode: protocol.ControlManaged, ControlRevision: 1, SpecVersion: 1, Rating: protocol.RatingP2, BlockReason: protocol.BlockNone, CreatedAt: 1, UpdatedAt: 1}); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO issue_manager_tasks(task_id,issue_id,category_id,expected_epoch,state,request_id,created_at,updated_at) VALUES('receipt-carrier',?,'implementer',1,'completed','req_fixture',1,1)`, issue.IssueID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO plan_actions(action_id,task_id,kind,effect_key,trigger_json,target_spec_version,authorization_json,generation,status,created_request_id,created_at,updated_at) VALUES('receipt-action','receipt-carrier','coordinate','receipt-effect','{"receipt_only":true}',1,'{}',1,'pending','req_fixture',1,1)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	check := func(wantIdle bool) {
		t.Helper()
		env := h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{Runtime: r, WorkspaceProtocol: 1}, nil)
		idle, err := h.app.preflightClaim(t.Context(), runner("linux-1"), env)
		if err != nil || idle != wantIdle {
			t.Fatalf("maintenance idle=%t want=%t err=%v", idle, wantIdle, err)
		}
	}
	check(false)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE plan_actions SET status='done' WHERE action_id='receipt-action'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	check(true)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE issue_manager_tasks SET state='retiring' WHERE task_id='receipt-carrier'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	check(false)
}
