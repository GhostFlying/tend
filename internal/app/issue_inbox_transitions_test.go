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
	status := ""
	if actionable {
		status = "open"
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		page, err = views.Attentions(t.Context(), tx.Q(), views.AttentionFilter{
			IssueID: issue, Recipient: "owner", OwnerSubject: owner.Subject,
			ActionableOnly: actionable, Status: status, IncludeArchived: true,
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
				DeliverySnapshot: protocol.DeliverySnapshot{State: "open", HeadSHA: head, CI: inboxCheckState(head, state)},
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
	assertItems(secondWait)
	// The worker already published the notice before the next regular sweep
	// visited the manager. Resuming it first must leave the manager linked.
	op := h.exec(owner, h.env(protocol.OpTasksResumeWait, taskTarget(worker.TaskID), protocol.ResumeExternalWaitPayload{
		WaitID: secondWait.WaitID, Reason: "Inspect CI",
	}, rev(h.task(worker.TaskID).ControlRevision)), ResumeExternalWait{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
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

func inboxCheckState(head, state string) *protocol.DeliveryCI {
	ci := &protocol.DeliveryCI{HeadSHA: head, State: state}
	switch state {
	case "pending":
		ci.Checks = []protocol.DeliveryCheck{{Name: "check", State: "in_progress", Required: new(true)}}
	case "success":
		ci.Checks = []protocol.DeliveryCheck{{Name: "check", State: "completed", Conclusion: "success", Required: new(true)}}
	case "optional":
		ci.State = "pending"
		ci.Checks = []protocol.DeliveryCheck{
			{Name: "check", State: "completed", Conclusion: "success", Required: new(true)},
			{Name: "optional", State: "in_progress", Required: new(false)},
		}
	}
	return ci
}

func refreshInboxDelivery(t *testing.T, h *harness, issue protocol.Issue, ref protocol.IssueDelivery) {
	t.Helper()
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	op := h.exec(owner, h.env("issues.refresh_delivery", protocol.Target{Type: "issue", ID: issue.IssueID},
		protocol.IssueRefreshDeliveryPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, DeliveryID: ref.DeliveryID}, rev(issue.Revision)), IssueRefreshDelivery{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}

func claimInboxDelivery(t *testing.T, h *harness, issue protocol.Issue, ref protocol.IssueDelivery) string {
	t.Helper()
	refreshInboxDelivery(t, h, issue, ref)
	query, err := h.app.ClaimServerDelivery(t.Context())
	if err != nil || query == nil {
		t.Fatal("expected a provider query", query, err)
	}
	return query.ID
}

func recordInboxDelivery(t *testing.T, h *harness, query, head, ci string) {
	t.Helper()
	if err := h.app.RecordServerDelivery(t.Context(), query, &protocol.DeliverySnapshot{
		State: "open", HeadSHA: head, CI: inboxCheckState(head, ci),
	}, ""); err != nil {
		t.Fatal(err)
	}
}

func registerInboxChecksWait(t *testing.T, h *harness, as protocol.Assignment, issue protocol.Issue, ref protocol.IssueDelivery) protocol.ExternalWait {
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

func TestIssueInboxDoesNotReviveEndedChecksWait(t *testing.T) {
	for _, scenario := range []struct {
		name, initial, middleHead, middleCI string
		refreshAgain, wantNotice            bool
	}{
		{name: "terminal at registration", initial: "success"},
		{name: "only optional check running at registration", initial: "optional"},
		{name: "required check ended while optional was still running", initial: "pending", middleHead: "head-a", middleCI: "optional"},
		{name: "same head rerun", initial: "pending", middleHead: "head-a", middleCI: "success"},
		{name: "head round trip", initial: "pending", middleHead: "head-b", middleCI: "pending"},
		{name: "terminal behind refresh fence", initial: "pending", middleHead: "head-a", middleCI: "success", refreshAgain: true, wantNotice: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			h, runtime := managerClaimHarness(t)
			h.app.serverDeliveryObservations = true
			ref := protocol.IssueDelivery{DeliveryID: "delivery-inbox", Provider: "github", Repository: "example/project", Number: 7}
			issue := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, protocol.CreateIssuePayload{
				Plan: protocol.IssuePlan{Title: "Wait for CI", Project: "default", Intent: "Wait for checks", Deliveries: []protocol.IssueDelivery{ref}},
			}, nil), historicalAdmittedIssueFixture{}))
			issue = canonicalLifecycleIssue(t, h.exec(owner, h.env(protocol.OpIssueRequestManager, protocol.Target{Type: "issue", ID: issue.IssueID},
				protocol.RequestIssueManagerPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Track the candidate"}, rev(issue.Revision)), RequestIssueManager{}))

			as := ownerClaim(t, h, runtime)
			recordInboxDelivery(t, h, claimInboxDelivery(t, h, issue, ref), "head-a", scenario.initial)
			wait := registerInboxChecksWait(t, h, as, issue, ref)
			if scenario.middleHead != "" {
				query := claimInboxDelivery(t, h, issue, ref)
				if scenario.refreshAgain {
					refreshInboxDelivery(t, h, issue, ref)
				}
				recordInboxDelivery(t, h, query, scenario.middleHead, scenario.middleCI)
			}
			recordInboxDelivery(t, h, claimInboxDelivery(t, h, issue, ref), "head-a", "pending")
			stopOwnerClaim(t, h, as)
			items := readIssueInbox(t, h, issue.IssueID, false)
			if scenario.wantNotice {
				if len(items) != 1 || items[0].IssueTransition == nil || items[0].IssueTransition.WaitID != wait.WaitID {
					t.Fatal("the superseded observation invalidated a current wait", items)
				}
			} else if len(items) != 0 {
				t.Fatal("an invalidated wait emitted a notification", items)
			}
		})
	}
}

func TestIssueInboxReviewWaitDoesNotNotifyImplementingIssue(t *testing.T) {
	h, runtime, issue := issueOwnerHarness(t)
	h.app.serverDeliveryObservations = true
	ref := protocol.IssueDelivery{DeliveryID: "delivery-inbox", Provider: "github", Repository: "example/project", Number: 7}
	issue.Plan.Deliveries = []protocol.IssueDelivery{ref}
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "update", protocol.UpdateIssuePayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Plan: issue.Plan}))
	task := ownerArtifact(t, h, runtime, issue)
	as := ownerWorkerClaim(t, h, runtime, protocol.RoleAccept)
	if as.Responsibility == nil || as.Responsibility.IssueID == issue.IssueID {
		t.Fatal("expected a distinct review responsibility", as.Responsibility)
	}
	if op := h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	recordInboxDelivery(t, h, claimInboxDelivery(t, h, issue, ref), "head-a", "pending")
	wait := registerInboxChecksWait(t, h, as, issue, ref)
	if op := h.runAs(as, protocol.OpRunsYieldExternalWait, protocol.YieldExternalWaitPayload{WaitID: wait.WaitID}, YieldExternalWait{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if items := readIssueInbox(t, h, issue.IssueID, false); len(items) != 0 {
		t.Fatal("review Run was attributed to the implementing Issue", items)
	}
	recordInboxDelivery(t, h, claimInboxDelivery(t, h, issue, ref), "head-a", "success")
	h.clock.Advance(6 * time.Second)
	if err := h.app.SweepExternalWaits(t.Context()); err != nil {
		t.Fatal(err)
	}
	waits, err := h.app.ExternalWaits(t.Context(), task.TaskID)
	if err != nil || len(waits) != 1 || waits[0].Status != "continued" || waits[0].WakeReason != "checks_success" {
		t.Fatal("review Run could no longer observe and resume", waits, err)
	}
}

func TestIssueInboxExactPRWaitRequiresSelectedCheck(t *testing.T) {
	for _, tc := range []struct {
		name      string
		required  *bool
		wantCount int
	}{
		{name: "provider requires the selected check", required: new(true), wantCount: 1},
		{name: "selected check is optional", required: new(false)},
		{name: "selected check has unknown requirements"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPRWaitTest(t, true)
			o := prWaitSnapshot()
			o.CI.Checks[0].Required = tc.required
			o.CI.Checks = append(o.CI.Checks, protocol.DeliveryCheck{
				ID: "other-check", Name: "another-required-check", Source: "github_check_run",
				App: "github-actions", State: "queued", Required: new(true),
			})
			f.record(t, &o, "")
			wait := f.request(t, "inbox-pr-check", protocol.PRWaitCondition{
				Kind: protocol.PRWaitCheckCompleted, HeadSHA: o.HeadSHA, Name: prWaitCheck,
			})
			f.record(t, &o, "")
			stopOwnerClaim(t, f.h, f.run)
			items := readIssueInbox(t, f.h, f.issue.IssueID, false)
			if len(items) != tc.wantCount {
				t.Fatalf("Inbox has %d entries, want %d: %+v", len(items), tc.wantCount, items)
			}
			if len(items) == 1 && (items[0].IssueTransition == nil || items[0].IssueTransition.WaitID != wait.WaitID) {
				t.Fatal("notification lost original wait identity", items)
			}
			if current := f.readWait(t); current.Status != "waiting" || current.WaitID != wait.WaitID {
				t.Fatal("notification eligibility changed the wait", current)
			}
		})
	}
}

func TestIssueInboxNegotiatesRequiredChecksForAggregateWait(t *testing.T) {
	for _, supported := range []bool{false, true} {
		t.Run(map[bool]string{true: "capable runner", false: "legacy runner"}[supported], func(t *testing.T) {
			f := newPRWaitTest(t, false)
			if supported {
				f.rt.Capabilities = append(f.rt.Capabilities, protocol.CapabilityDeliveryRequiredChecks)
				f.rt.Sequence++
				if op := f.h.versionedHello("linux-1", f.rt); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			o := prWaitSnapshot()
			f.record(t, &o, "")
			registerInboxChecksWait(t, f.h, f.run, f.issue, f.ref)
			refreshInboxDelivery(t, f.h, f.issue, f.ref)
			as := pollDelivery(t, f.h, "linux-1", f.rt.InstanceID)
			if len(as) != 1 || as[0].PRChecks != supported || as[0].RequiredChecks != supported {
				t.Fatal("aggregate wait did not use the negotiated report format", as)
			}
		})
	}
}
