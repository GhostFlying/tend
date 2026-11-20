package app

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func readIssueInbox(t *testing.T, h *harness, issue string, actionable bool) []protocol.AttentionView {
	t.Helper()
	var page protocol.AttentionPage
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		page, err = views.Attentions(t.Context(), tx.Q(), views.AttentionFilter{
			IssueID: issue, Recipient: "owner", OwnerSubject: owner.Subject,
			ActionableOnly: actionable, IncludeArchived: true,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return page.Items
}

func TestIssueInboxChecksWaitUsesStoppedCurrentCandidate(t *testing.T) {
	h, runtime := managerClaimHarness(t)
	ref := protocol.IssueDelivery{DeliveryID: "delivery-inbox", Provider: "github", Repository: "example/project", Number: 7}
	issue := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, protocol.CreateIssuePayload{
		Plan: protocol.IssuePlan{Title: "Wait for CI", Project: "default", Intent: "Wait for checks", Deliveries: []protocol.IssueDelivery{ref}},
	}, nil), historicalAdmittedIssueFixture{}))
	issue = canonicalLifecycleIssue(t, h.exec(owner, h.env(protocol.OpIssueRequestManager, protocol.Target{Type: "issue", ID: issue.IssueID},
		protocol.RequestIssueManagerPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Track the candidate"}, rev(issue.Revision)), RequestIssueManager{}))

	snapshot := func(head, state string) {
		t.Helper()
		if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
			_, err := tx.PutDeliveryObservationFact(t.Context(), ref, protocol.DeliveryObservation{
				DeliverySnapshot: protocol.DeliverySnapshot{State: "open", HeadSHA: head, CI: &protocol.DeliveryCI{HeadSHA: head, State: state}},
				CheckedAt:        h.clock.Now().UnixMilli(),
			})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	begin := func(as protocol.Assignment) protocol.ExternalWait {
		t.Helper()
		op := h.runAs(as, protocol.OpRunsWaitExternal, protocol.WaitExternalPayload{
			WaitKey: "inbox-ci", Kind: "checks", IssueID: issue.IssueID, TargetID: ref.DeliveryID,
			Reason: "Wait for required CI", Recovery: "Read the required CI result",
		}, WaitExternal{})
		var wait protocol.ExternalWait
		if op.Status != protocol.OpSucceeded || json.Unmarshal(op.Result, &wait) != nil {
			t.Fatal(op)
		}
		return wait
	}
	sweep := func() {
		t.Helper()
		h.clock.Advance(6 * time.Second)
		var group sync.WaitGroup
		for range 2 {
			group.Go(func() {
				if err := h.app.SweepExternalWaits(t.Context()); err != nil {
					t.Error(err)
				}
			})
		}
		group.Wait()
	}
	assertItems := func(waits ...protocol.ExternalWait) {
		t.Helper()
		items := readIssueInbox(t, h, issue.IssueID, false)
		if len(items) != len(waits) {
			t.Fatalf("Inbox items = %+v, expected %d", items, len(waits))
		}
		found := map[string]bool{}
		for _, item := range items {
			n := item.IssueTransition
			if item.IssueID != issue.IssueID || item.IssueTitle != issue.Plan.Title || item.Kind != "information" ||
				item.ReadAt != 0 || item.ArchivedAt != 0 || item.CreatedAt == 0 || item.Historical ||
				n == nil || n.State != "waiting_ci" || n.Delivery == nil || *n.Delivery != ref {
				t.Fatalf("unexpected transition notice %+v", item)
			}
			found[n.WaitID] = true
		}
		for _, wait := range waits {
			if !found[wait.WaitID] {
				t.Fatal("missing original wait", wait.WaitID)
			}
		}
		if actions := readIssueInbox(t, h, issue.IssueID, true); len(actions) > 0 {
			t.Fatal("CI wait introduced an owner action", actions)
		}
	}

	ordinaryTask := createCanonicalTaskForTest(t, h, issue)
	first := ownerClaim(t, h, runtime)
	worker := ownerClaim(t, h, runtime)
	if worker.TaskID != ordinaryTask {
		t.Fatalf("expected ordinary Issue worker, got %+v", worker)
	}
	if op := h.runAs(worker, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	snapshot("head-a", "unknown")
	begin(first)
	assertItems()
	stopOwnerClaim(t, h, first)
	assertItems()
	snapshot("head-a", "pending")
	sweep()
	// CI alone does not put the Issue on hold while its other worker is live.
	assertItems()
	secondWait := begin(worker)
	if op := h.runAs(worker, protocol.OpRunsYieldExternalWait, protocol.YieldExternalWaitPayload{WaitID: secondWait.WaitID}, YieldExternalWait{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	sweep()
	assertItems(secondWait)
	sweep()
	assertItems(secondWait)

	snapshot("head-a", "success")
	sweep()
	next := ownerClaim(t, h, runtime)
	snapshot("head-b", "pending")
	nextWait := begin(next)
	assertItems(secondWait)
	if op := h.runAs(next, protocol.OpRunsYieldExternalWait, protocol.YieldExternalWaitPayload{WaitID: nextWait.WaitID}, YieldExternalWait{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	sweep()
	assertItems(secondWait, nextWait)

	if count := h.count("events WHERE kind='attention.issue_transition'"); count != 2 {
		t.Fatalf("transition event count = %d, want 2", count)
	}
	if count := h.count("conversation_bindings WHERE object_type='attention'"); count != 0 {
		t.Fatalf("Inbox transitions created Chat messages: %d", count)
	}
}
