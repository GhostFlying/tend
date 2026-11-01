package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/webhookauth"
)

func TestTriggerCancelledDeferredRuleNeverDispatches(t *testing.T) {
	h := hostedManagerHarness(t)
	h.app.followupsEnabled = true
	issue := createCanonicalOwnerTestIssue(t, h)
	spec := canonicalFollowupSpec(h)
	spec.At = h.clock.Now().Add(time.Hour).UnixMilli()
	p := protocol.IssueFollowupPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Spec: &spec}
	r := canonicalRuleForTest(t, h.exec(owner, h.env("issues.followups_create", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(issue.Revision)), IssueFollowupCommand{Action: "create"}))
	for _, action := range []string{"trigger", "cancel"} {
		env := h.env("issues.followups_"+action, protocol.Target{Type: "followup", ID: r.RuleID}, protocol.IssueFollowupPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch}, rev(r.Revision))
		env.Reason = "owner controls rule"
		r = canonicalRuleForTest(t, h.exec(owner, env, IssueFollowupCommand{Action: action}))
	}
	assignHostedManager(t, h, issue)
	cmd := SweepTriggers{}
	op := h.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, h.env(cmd.Operation(), protocol.Target{Type: "followups"}, struct{}{}, nil), cmd)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if n := h.count("background_sources WHERE kind='followup.triggered'"); n != 0 {
		t.Fatalf("cancelled deferred occurrence was dispatched: %d sources, rule status %s", n, r.Status)
	}
}

func TestTriggerReadyWebhookBypassesOwnerHeldBacklog(t *testing.T) {
	h := hostedManagerHarness(t)
	h.app.followupsEnabled = true
	blocked := createCanonicalOwnerTestIssue(t, h)
	ready := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	keys := &webhookauth.Files{Dir: filepath.Join(t.TempDir(), "keys")}
	h.app.webhookKeys = keys
	ref := h.ids.New(protocol.PrefixRequest)
	if err := keys.Put(t.Context(), ref, bytes.Repeat([]byte("k"), 32)); err != nil {
		t.Fatal(err)
	}
	create := func(issue protocol.Issue) protocol.WebhookSubscription {
		p := protocol.WebhookSubscriptionPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "read result", Instruction: "read facts only", KeyRef: ref}
		op := h.exec(owner, h.env("webhooks.create", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(issue.Revision)), WebhookCommand{Action: "create"})
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		var sub protocol.WebhookSubscription
		if err := json.Unmarshal(op.Result, &sub); err != nil {
			t.Fatal(err)
		}
		return sub
	}
	b, r := create(blocked), create(ready)
	receive := func(sub protocol.WebhookSubscription, i int) {
		p := ReceiveWebhookPayload{SubscriptionID: sub.ID, DeliveryID: fmt.Sprint(i), Body: json.RawMessage(`{"state":"done"}`)}
		op := h.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, h.env("webhooks.receive", protocol.Target{Type: "webhook", ID: sub.ID}, p, nil), ReceiveWebhook{KeyRef: ref})
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	for i := range 100 {
		receive(b, i)
	}
	receive(r, 101)
	cmd := SweepTriggers{}
	h.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, h.env(cmd.Operation(), protocol.Target{Type: "followups"}, struct{}{}, nil), cmd)
	if n := h.count("background_sources WHERE kind='webhook.received'"); n != 1 {
		t.Fatalf("ready Issue webhook starved by 100 owner-held receipts: sources=%d", n)
	}
}

func TestTriggerClosingIssueSettlesObservation(t *testing.T) {
	h := newHarness(t)
	item := observerItem(t, h)
	observerHello(t, h, "linux-1", "observer")
	pollDelivery(t, h, "linux-1", "observer")
	closure := protocol.CloseWorkItemPayload{Closure: protocol.IssueClosure{Kind: "declined", Reason: "Owner ended responsibility", Evidence: []protocol.IssueEvidence{h.ownerEvidence()}}}
	op := h.exec(owner, h.env("work_items.close", workTarget(item.WorkItemID), closure, rev(1)), CloseIssue{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	h.clock.Advance(3 * time.Minute)
	cmd := SweepTriggers{}
	h.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, h.env(cmd.Operation(), protocol.Target{Type: "followups"}, struct{}{}, nil), cmd)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		page, err := store.TriggerEvents(t.Context(), tx.Q(), protocol.TriggerQuery{IssueID: item.WorkItemID}, h.clock.Now().UnixMilli())
		if err != nil {
			return err
		}
		if len(page.Items) != 1 || page.Items[0].Status == "processing" {
			t.Fatalf("closed, fenced observation still projects processing: %+v", page.Items)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func regressionWebhook(t *testing.T, h *harness, issue protocol.Issue) protocol.WebhookSubscription {
	keys := &webhookauth.Files{Dir: filepath.Join(t.TempDir(), "keys")}
	h.app.webhookKeys = keys
	ref := h.ids.New(protocol.PrefixRequest)
	if err := keys.Put(t.Context(), ref, bytes.Repeat([]byte("k"), 32)); err != nil {
		t.Fatal(err)
	}
	p := protocol.WebhookSubscriptionPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "read result", Instruction: "read facts", KeyRef: ref}
	op := h.exec(owner, h.env("webhooks.create", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(issue.Revision)), WebhookCommand{Action: "create"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var sub protocol.WebhookSubscription
	if err := json.Unmarshal(op.Result, &sub); err != nil {
		t.Fatal(err)
	}
	return sub
}
func TestTriggerWebhookPauseAfterOwnerHandoff(t *testing.T) {
	h := hostedManagerHarness(t)
	h.app.followupsEnabled = true
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	sub := regressionWebhook(t, h, issue)
	env := h.env("webhooks.receive", protocol.Target{Type: "webhook", ID: sub.ID}, ReceiveWebhookPayload{SubscriptionID: sub.ID, DeliveryID: "1", Body: json.RawMessage(`{}`)}, nil)
	h.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, env, ReceiveWebhook{KeyRef: sub.KeyRef})
	p := protocol.IssueAssignmentPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: owner.Subject}, Reason: "owner takes responsibility"}
	next := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.assign", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(issue.Revision)), HostedIssueAssignment{}))
	fin := protocol.FinishIssueAssignmentPayload{ExpectedAssignmentEpoch: next.AssignmentEpoch, HandoffID: next.PendingHandoffID, Reason: "old execution not started"}
	issue = canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.finish_assignment", protocol.Target{Type: "issue", ID: issue.IssueID}, fin, rev(next.Revision)), HostedIssueAssignment{Finish: true}))
	op := h.exec(owner, h.env("webhooks.pause", protocol.Target{Type: "webhook", ID: sub.ID}, protocol.WebhookSubscriptionPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch}, rev(sub.Revision)), WebhookCommand{Action: "pause"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if n := h.count("background_sources s JOIN background_batches b USING(batch_id) WHERE s.kind='webhook.received' AND s.cancelled_at=0 AND b.status='pending'"); n != 0 {
		t.Fatalf("paused webhook still has %d unclaimed source(s) in an owner-directed batch", n)
	}
}
func TestTriggerWebhookCanRetryAfterArchiveAdmissionClears(t *testing.T) {
	h := hostedManagerHarness(t)
	h.app.followupsEnabled = true
	issue := createCanonicalOwnerTestIssue(t, h)
	sub := regressionWebhook(t, h, issue)
	prep, err := h.app.ArchivePreparation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	change := func(v bool) {
		t.Helper()
		p, e := h.app.ChangeArchivePreparation(t.Context(), protocol.ArchiveControlRequest{RequestID: h.ids.New(protocol.PrefixRequest), ExpectedControlRevision: prep.Revision}, v)
		if e != nil {
			t.Fatal(e)
		}
		prep = p
	}
	change(true)
	env := h.env("webhooks.receive", protocol.Target{Type: "webhook", ID: sub.ID}, ReceiveWebhookPayload{SubscriptionID: sub.ID, DeliveryID: "1", Body: json.RawMessage(`{}`)}, nil)
	first, blocked := h.app.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalSystem}, env, ReceiveWebhook{KeyRef: sub.KeyRef})
	if protocol.CodeOf(blocked) != protocol.CodeInvalidTransition {
		t.Fatal(first, blocked)
	}
	if h.count("webhook_receipts") != 0 {
		t.Fatal("temporary admission recorded a delivery")
	}

	change(false)
	again := h.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, env, ReceiveWebhook{KeyRef: sub.KeyRef})
	if again.Status != protocol.OpSucceeded {
		t.Fatalf("same external delivery permanently rejected after archive gate cleared: %+v", again)
	}
}

func TestTriggerOwnerHeldCronRetainsOnePendingOccurrence(t *testing.T) {
	h := hostedManagerHarness(t)
	h.app.followupsEnabled = true
	issue := createCanonicalOwnerTestIssue(t, h)
	spec := canonicalFollowupSpec(h)
	spec.Kind = "cron"
	spec.At = 0
	spec.Cron = "*/10 * * * *"
	spec.ExpiresAt = h.clock.Now().Add(24 * time.Hour).UnixMilli()
	r := canonicalRuleForTest(t, h.exec(owner, h.env("issues.followups_create", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.IssueFollowupPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Spec: &spec}, rev(issue.Revision)), IssueFollowupCommand{Action: "create"}))
	sweep := func() {
		cmd := SweepTriggers{}
		op := h.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, h.env(cmd.Operation(), protocol.Target{Type: "followups"}, struct{}{}, nil), cmd)
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	h.clock.Advance(11 * time.Minute)
	sweep()
	t.Logf("after first slot: rule=%+v, occurrences=%+v", h.readFollowup(r.RuleID), h.occurrences(r.RuleID))
	h.clock.Advance(11 * time.Minute)
	sweep()
	issue = assignHostedManager(t, h, issue)
	sweep()
	t.Logf("after assigning: rule=%+v, sources=%d", h.readFollowup(r.RuleID), h.count("background_sources WHERE kind='followup.triggered'"))
	if n := h.count("background_sources WHERE kind='followup.triggered'"); n != 1 {
		t.Fatalf("expected retained trigger to reach agent, got %d", n)
	}
}
func TestTriggerWebhookSubscriptionSearchFiltersRecords(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	sub := regressionWebhook(t, h, issue)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		page, err := store.ListWebhookSubscriptions(t.Context(), tx.Q(), protocol.TriggerQuery{Search: "should-never-match-this-title"})
		if err == nil && len(page.Items) != 0 {
			t.Errorf("search returned subscription %s (%s)", sub.ID, sub.Title)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
func TestTriggerObservationRetainsOriginalPlannedTime(t *testing.T) {
	h := newHarness(t)
	item := observerItem(t, h)
	observerHello(t, h, "linux-1", "observer")
	a := pollDelivery(t, h, "linux-1", "observer")[0]
	reportDelivery(t, h, "linux-1", "observer", a, "open", "")
	due := h.clock.Now().Add(5 * time.Minute).UnixMilli()
	h.clock.Advance(15 * time.Minute)
	pollDelivery(t, h, "linux-1", "observer")
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		page, err := store.TriggerEvents(t.Context(), tx.Q(), protocol.TriggerQuery{IssueID: item.WorkItemID}, h.clock.Now().UnixMilli())
		if err == nil && page.Items[0].PlannedAt != due {
			t.Errorf("planned_at=%d due=%d", page.Items[0].PlannedAt, due)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTriggerWebhookPausePreservesInitialAssignmentWake(t *testing.T) {
	h := hostedManagerHarness(t)
	h.app.followupsEnabled = true
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	sub := regressionWebhook(t, h, issue)
	env := h.env("webhooks.receive", protocol.Target{Type: "webhook", ID: sub.ID}, ReceiveWebhookPayload{SubscriptionID: sub.ID, DeliveryID: "one", Body: json.RawMessage(`{}`)}, nil)
	h.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, env, ReceiveWebhook{KeyRef: sub.KeyRef})
	t.Logf("pending assignment batches before pause=%d", h.count("background_batches WHERE status='pending'"))
	op := h.exec(owner, h.env("webhooks.pause", protocol.Target{Type: "webhook", ID: sub.ID}, protocol.WebhookSubscriptionPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch}, rev(sub.Revision)), WebhookCommand{Action: "pause"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	_, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:issue:"+issue.IssueID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("pausing the webhook cancelled the independent initial Issue-assignment wake")
	}
}

func TestTriggerDeadlineTraceRetainsUnknownStatus(t *testing.T) {
	h := newHarness(t)
	h.app.followupsEnabled = true
	issue := createCanonicalOwnerTestIssue(t, h)
	d := putDeadlineForTest(t, h, "", nil, deadlinePayloadForTest(h, issue, 2*time.Hour))
	delivery, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err = h.app.FinishDelivery(t.Context(), delivery, DeliveryResult{Outcome: Unknown, Reason: "lost receipt"}); err != nil {
		t.Fatal(err)
	}
	occurrence := h.occurrences(d.NearRuleID)[0]
	if err = h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		event, err := store.GetTrigger(t.Context(), tx.Q(), occurrence.OccurrenceID)
		if err == nil && (event.Status != "unknown" || event.BatchID == "") {
			t.Errorf("deadline occurrence status=%s, diagnostics status=%s, batch=%q", occurrence.Status, event.Status, event.BatchID)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTriggerJoinedBatchQueueTimeCannotPrecedeReceipt(t *testing.T) {
	h := hostedManagerHarness(t)
	h.app.followupsEnabled = true
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	sub := regressionWebhook(t, h, issue)
	first := h.env("webhooks.receive", protocol.Target{Type: "webhook", ID: sub.ID}, ReceiveWebhookPayload{SubscriptionID: sub.ID, DeliveryID: "zero", Body: json.RawMessage(`{}`)}, nil)
	h.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, first, ReceiveWebhook{KeyRef: sub.KeyRef})
	h.clock.Advance(time.Hour)
	env := h.env("webhooks.receive", protocol.Target{Type: "webhook", ID: sub.ID}, ReceiveWebhookPayload{SubscriptionID: sub.ID, DeliveryID: "one", Body: json.RawMessage(`{}`)}, nil)
	op := h.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, env, ReceiveWebhook{KeyRef: sub.KeyRef})
	var receipt protocol.WebhookReceipt
	if err := json.Unmarshal(op.Result, &receipt); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		event, err := store.GetTrigger(t.Context(), tx.Q(), receipt.EventID)
		if err == nil {
			for _, step := range event.Steps {
				if step.Kind == "queued" && step.At < event.CreatedAt {
					t.Errorf("event received=%d but queued=%d; timeline=%+v", event.CreatedAt, step.At, event.Steps)
				}
			}
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTriggerFollowupRecordsItsOwnBatchAdmission(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	h.followup(w, protocol.FollowupSpec{Kind: "at", At: h.clock.Now().UnixMilli()})
	h.clock.Advance(time.Hour)
	r := h.followup(w, protocol.FollowupSpec{Kind: "at", At: h.clock.Now().UnixMilli()})
	occurrence := h.occurrences(r.RuleID)[0]
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		event, err := store.GetTrigger(t.Context(), tx.Q(), occurrence.OccurrenceID)
		if err != nil {
			return err
		}
		var batchCreated int64
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT created_at FROM background_batches WHERE batch_id=?`, event.BatchID).Scan(&batchCreated); err != nil {
			return err
		}
		if batchCreated >= event.CreatedAt {
			t.Fatal("test did not join an older batch")
		}
		queued := 0
		for _, step := range event.Steps {
			if step.Kind == "queued" {
				queued++
				if step.At != occurrence.CreatedAt {
					t.Errorf("queue time=%d, occurrence time=%d", step.At, occurrence.CreatedAt)
				}
			}
		}
		if queued != 1 {
			t.Errorf("expected one recorded queue admission, got %d", queued)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
