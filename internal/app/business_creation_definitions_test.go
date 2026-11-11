package app

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/webhookauth"
)

func requireCreationDefinitionCode(t *testing.T, h *harness, envelope *protocol.Envelope, command Command, code protocol.Code) {
	t.Helper()
	op, err := h.app.Execute(t.Context(), owner, envelope, command)
	if protocol.CodeOf(err) != code && (op.Error == nil || op.Error.Code != code) {
		t.Fatal("creation definition was not rejected", op, err)
	}
}

func TestWebhookCreationDefinitionRejectsChangesAndReturnsCurrentState(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	keys := &webhookauth.Files{Dir: filepath.Join(t.TempDir(), "keys")}
	h.app.webhookKeys = keys
	keyRef, rotatedRef := h.ids.New(protocol.PrefixRequest), h.ids.New(protocol.PrefixRequest)
	for _, ref := range []string{keyRef, rotatedRef} {
		if err := keys.Put(t.Context(), ref, bytes.Repeat([]byte("k"), 32)); err != nil {
			t.Fatal(err)
		}
	}
	original := protocol.WebhookSubscriptionPayload{SubscriptionID: h.ids.New(protocol.Prefix("webhook")), ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Observe the original result", Instruction: "Inspect the agreed external evidence", KeyRef: keyRef}
	envelope := h.env("webhooks.create", protocol.Target{Type: "issue", ID: issue.IssueID}, original, rev(issue.Revision))
	first := h.exec(owner, envelope, WebhookCommand{Action: "create"})
	var current protocol.WebhookSubscription
	if first.Status != protocol.OpSucceeded || json.Unmarshal(first.Result, &current) != nil {
		t.Fatal(first)
	}
	counts := func() [3]int { return [3]int{h.count("webhook_subscriptions"), h.count("events"), h.count("outbox")} }
	before := counts()
	for name, change := range map[string]func(*protocol.WebhookSubscriptionPayload){
		"title":       func(p *protocol.WebhookSubscriptionPayload) { p.Title = "Different result" },
		"instruction": func(p *protocol.WebhookSubscriptionPayload) { p.Instruction = "Execute another action" },
		"key":         func(p *protocol.WebhookSubscriptionPayload) { p.KeyRef = rotatedRef },
	} {
		t.Run(name, func(t *testing.T) {
			changed := original
			change(&changed)
			requireCreationDefinitionCode(t, h, h.env("webhooks.create", envelope.Target, changed, envelope.ExpectedControlRevision), WebhookCommand{Action: "create"}, protocol.CodeConflictPayload)
			if counts() != before {
				t.Fatal("conflicting creation mutated subscription", before, counts())
			}
		})
	}
	update := original
	update.Title = "Clarified result"
	update.Instruction = "Inspect the clarified evidence"
	edited := h.exec(owner, h.env("webhooks.update", protocol.Target{Type: "webhook", ID: current.ID}, update, rev(current.Revision)), WebhookCommand{Action: "update"})
	if edited.Status != protocol.OpSucceeded || json.Unmarshal(edited.Result, &current) != nil {
		t.Fatal(edited)
	}
	rotate := protocol.WebhookSubscriptionPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, KeyRef: rotatedRef}
	rotated := h.exec(owner, h.env("webhooks.rotate", protocol.Target{Type: "webhook", ID: current.ID}, rotate, rev(current.Revision)), WebhookCommand{Action: "rotate"})
	if rotated.Status != protocol.OpSucceeded || json.Unmarshal(rotated.Result, &current) != nil {
		t.Fatal(rotated)
	}
	before = counts()
	h.app.webhookKeys = nil // Reading the original business definition grants no new credential action.
	retry := *envelope
	retry.RequestID = h.ids.New(protocol.PrefixRequest)
	again := h.exec(owner, &retry, WebhookCommand{Action: "create"})
	var returned protocol.WebhookSubscription
	if again.Status != protocol.OpSucceeded || json.Unmarshal(again.Result, &returned) != nil || !reflect.DeepEqual(returned, current) || counts() != before {
		t.Fatal("original creation did not read current subscription", again, returned, current)
	}
	changed := original
	changed.Title = current.Title
	changed.Instruction = current.Instruction
	changed.KeyRef = current.KeyRef
	requireCreationDefinitionCode(t, h, h.env("webhooks.create", envelope.Target, changed, envelope.ExpectedControlRevision), WebhookCommand{Action: "create"}, protocol.CodeConflictPayload)
	if counts() != before {
		t.Fatal("unversioned changed creation mutated subscription")
	}
	// A historical edited subscription without its creation definition cannot
	// safely infer that a new input is the original creation.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM events WHERE object_type='webhook' AND object_id=? AND object_version=1`, current.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	before = counts()
	requireCreationDefinitionCode(t, h, &retry, WebhookCommand{Action: "create"}, protocol.CodeInvalidRequest)
	if counts() != before {
		t.Fatal("missing creation history manufactured an effect")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		actual, err := store.GetWebhookSubscription(t.Context(), tx.Q(), current.ID)
		if err == nil && !reflect.DeepEqual(actual, current) {
			t.Fatal("current subscription became unreadable", actual)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPlannerCreationDefinitionRejectsChangesAndReturnsCurrentState(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	attention := attentionForTest(t, h, protocol.CreateAttentionPayload{Kind: "action", Title: "Review original draft", IssueID: issue.IssueID})
	source, otherSource := h.ownerEvidence().Reference, h.ownerEvidence().Reference
	expected, estimate := int64(1790000000000), int64(600)
	original := protocol.PlannerEntry{Kind: "user_action", IssueID: issue.IssueID, AttentionID: attention.AttentionID, Title: "Review the original draft", Responsible: owner.Subject, Rank: 4, Status: "open", Condition: "After the draft is available", ExpectedAt: &expected, EstimateSeconds: &estimate, SourceMessageID: source, Reason: "Owner chooses the initial order"}
	envelope := h.env("planner.entries_put", protocol.Target{Type: "planner_entry"}, protocol.PutPlannerEntryPayload{Entry: original}, nil)
	first := h.exec(owner, envelope, PutPlannerEntry{})
	var current protocol.PlannerEntry
	if first.Status != protocol.OpSucceeded || json.Unmarshal(first.Result, &current) != nil {
		t.Fatal(first)
	}
	counts := func() [3]int { return [3]int{h.count("planner_entries"), h.count("events"), h.count("outbox")} }
	before := counts()
	for name, change := range map[string]func(*protocol.PlannerEntry){
		"title":       func(p *protocol.PlannerEntry) { p.Title = "Different action" },
		"responsible": func(p *protocol.PlannerEntry) { p.Responsible = "another-owner" },
		"rank":        func(p *protocol.PlannerEntry) { p.Rank = 9 },
		"status":      func(p *protocol.PlannerEntry) { p.Status = "blocked" },
		"condition":   func(p *protocol.PlannerEntry) { p.Condition = "After another result" },
		"expected_at": func(p *protocol.PlannerEntry) { v := expected + 1; p.ExpectedAt = &v },
		"estimate":    func(p *protocol.PlannerEntry) { v := estimate + 1; p.EstimateSeconds = &v },
		"source":      func(p *protocol.PlannerEntry) { p.SourceMessageID = otherSource },
		"reason":      func(p *protocol.PlannerEntry) { p.Reason = "A different instruction" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := original
			change(&changed)
			requireCreationDefinitionCode(t, h, h.env("planner.entries_put", envelope.Target, protocol.PutPlannerEntryPayload{Entry: changed}, nil), PutPlannerEntry{}, protocol.CodeConflictPayload)
			if counts() != before {
				t.Fatal("conflicting creation changed planner entry", before, counts())
			}
		})
	}
	update := original
	update.Title = "Clarified action"
	update.Rank = 9
	update.Status = "blocked"
	update.Reason = "Owner edits current order"
	edited := h.exec(owner, h.env("planner.entries_put", envelope.Target, protocol.PutPlannerEntryPayload{Entry: update}, rev(current.Revision)), PutPlannerEntry{})
	if edited.Status != protocol.OpSucceeded || json.Unmarshal(edited.Result, &current) != nil {
		t.Fatal(edited)
	}
	before = counts()
	retry := *envelope
	retry.RequestID = h.ids.New(protocol.PrefixRequest)
	again := h.exec(owner, &retry, PutPlannerEntry{})
	var returned protocol.PlannerEntry
	if again.Status != protocol.OpSucceeded || json.Unmarshal(again.Result, &returned) != nil || !reflect.DeepEqual(returned, current) || counts() != before {
		t.Fatal("original creation did not read current entry", again, returned, current)
	}
	requireCreationDefinitionCode(t, h, h.env("planner.entries_put", envelope.Target, protocol.PutPlannerEntryPayload{Entry: update}, nil), PutPlannerEntry{}, protocol.CodeConflictPayload)
	if counts() != before {
		t.Fatal("unversioned edit changed planner entry")
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM events WHERE object_type='planner_entry' AND object_id=? AND object_version=1`, current.EntryID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	before = counts()
	requireCreationDefinitionCode(t, h, &retry, PutPlannerEntry{}, protocol.CodeInvalidRequest)
	if counts() != before {
		t.Fatal("missing original planner definition manufactured an effect")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		actual, err := store.GetPlannerEntry(t.Context(), tx.Q(), current.EntryID)
		if err == nil && !reflect.DeepEqual(actual, current) {
			t.Fatal("current planner entry became unreadable", actual)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
