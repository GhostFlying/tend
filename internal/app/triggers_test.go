package app

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/webhookauth"
)

func TestWebhookDispatchReachesIssueAndRetainsSingleSource(t *testing.T) {
	h := hostedManagerHarness(t)
	h.app.followupsEnabled = true
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	keys := &webhookauth.Files{Dir: filepath.Join(t.TempDir(), "keys")}
	h.app.webhookKeys = keys
	keyRef := h.ids.New(protocol.PrefixRequest)
	if err := keys.Put(t.Context(), keyRef, bytes.Repeat([]byte("k"), 32)); err != nil {
		t.Fatal(err)
	}
	p := protocol.WebhookSubscriptionPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Read external result", Instruction: "Only inspect facts", KeyRef: keyRef}
	op := h.exec(owner, h.env("webhooks.create", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(issue.Revision)), WebhookCommand{Action: "create"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var sub protocol.WebhookSubscription
	if err := json.Unmarshal(op.Result, &sub); err != nil {
		t.Fatal(err)
	}
	payload := ReceiveWebhookPayload{SubscriptionID: sub.ID, DeliveryID: "1", Body: json.RawMessage(`{"state":"merged"}`)}
	env := h.env("webhooks.receive", protocol.Target{Type: "webhook", ID: sub.ID}, payload, nil)
	first := h.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, env, ReceiveWebhook{KeyRef: keyRef})
	if first.Status != protocol.OpSucceeded {
		t.Fatal(first)
	}
	replay := h.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, env, ReceiveWebhook{KeyRef: keyRef})
	if string(first.Result) != string(replay.Result) {
		t.Fatal("receipt changed")
	}
	if h.count("webhook_receipts") != 1 || h.count("trigger_events") != 1 {
		t.Fatal("duplicated delivery")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var n int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM background_sources s JOIN background_batches b USING(batch_id) WHERE s.kind='webhook.received' AND b.work_item_id=?`, issue.IssueID).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			t.Fatal("did not route once", n)
		}
		page, err := store.TriggerEvents(t.Context(), tx.Q(), protocol.TriggerQuery{IssueID: issue.IssueID}, h.clock.Now().UnixMilli())
		if err != nil {
			return err
		}
		if len(page.Items) != 1 || page.Items[0].BatchID == "" {
			t.Fatal(page)
		}
		detail, err := store.GetTrigger(t.Context(), tx.Q(), page.Items[0].ID)
		if err == nil && len(detail.Steps) < 3 {
			t.Fatal(detail)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
func TestCronFollowupCoalescesMissedSlotsAndDoesNotDuplicate(t *testing.T) {
	h := hostedManagerHarness(t)
	h.app.followupsEnabled = true
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	spec := canonicalFollowupSpec(h)
	spec.Kind = "cron"
	spec.At = 0
	spec.Cron = "*/10 * * * *"
	spec.ExpiresAt = h.clock.Now().Add(48 * time.Hour).UnixMilli()
	p := protocol.IssueFollowupPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Spec: &spec}
	r := canonicalRuleForTest(t, h.exec(owner, h.env("issues.followups_create", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(issue.Revision)), IssueFollowupCommand{Action: "create"}))
	h.clock.Advance(2 * time.Hour)
	h.sweepFollowups()
	h.sweepFollowups()
	if len(h.occurrences(r.RuleID)) != 1 {
		t.Fatal("missed slots replayed")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		current, err := store.GetFollowup(t.Context(), tx.Q(), r.RuleID)
		if err == nil && (current.NextAt <= h.clock.Now().UnixMilli() || current.TriggerCount != 1 || current.Status != "active") {
			t.Fatal(current)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
