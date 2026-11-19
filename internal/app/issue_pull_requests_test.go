package app

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func TestIssueBoardPullRequestRegistrationUsesSharedProviderFacts(t *testing.T) {
	h := newHarness(t)
	h.app.serverDeliveryObservations = true
	first, second := createCanonicalOwnerTestIssue(t, h), createCanonicalOwnerTestIssue(t, h)
	register := func(issue string, link string) protocol.IssueDelivery {
		t.Helper()
		op := updateDeliveryForTest(t, h, issue, protocol.UpdateIssueDeliveryPayload{
			Action: "register_pr", PRURL: link, IssueControlPayload: protocol.IssueControlPayload{Reason: "Register the provider URL"},
		})
		var ref protocol.IssueDelivery
		if op.Status != protocol.OpSucceeded || json.Unmarshal(op.Result, &ref) != nil {
			t.Fatal(op)
		}
		return ref
	}
	ref := register(first.IssueID, "https://github.com/Acme/Game/pull/27")
	duplicate := register(first.IssueID, "https://GITHUB.COM/acme/GAME/pull/27/?tab=files#top")
	if duplicate != ref || h.count("server_delivery_jobs") != 1 || h.count("issue_delivery_duties") != 0 {
		t.Fatal("registration did not preserve one reference and zero delivery duties", ref, duplicate)
	}
	register(second.IssueID, "https://github.com/ACME/game/pull/27")
	if h.count("server_delivery_jobs") != 1 {
		t.Fatal("the same provider PR was assigned multiple queries")
	}
	read := func(want protocol.IssueBoardPRStatus) {
		t.Helper()
		cursor := ""
		seen := map[string]bool{first.IssueID: false, second.IssueID: false}
		for range seen {
			err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				page, err := views.CanonicalIssues(t.Context(), tx.Q(), views.CanonicalIssueFilter{Board: true, Limit: 1, After: cursor})
				if err != nil {
					return err
				}
				if len(page.Items) != 1 || page.Items[0].Board == nil {
					t.Fatalf("unexpected board page: %+v", page)
				}
				id := page.Items[0].IssueID
				if visited, exists := seen[id]; !exists || visited {
					t.Fatalf("unexpected Issue on board page: %s", id)
				}
				seen[id] = true
				got := page.Items[0].Board.PullRequests
				if len(got) != 1 || got[0].URL != "https://github.com/acme/game/pull/27" || got[0].Status != want {
					t.Fatalf("board PRs = %+v, want status %s", got, want)
				}
				cursor = page.Next
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	read(protocol.IssueBoardPRUnknown)
	observe := func(snapshot *protocol.DeliverySnapshot, errKind string) {
		t.Helper()
		q, err := h.app.ClaimServerDelivery(t.Context())
		if err != nil || q == nil || q.Number != 27 {
			t.Fatal(q, err)
		}
		if err = h.app.RecordServerDelivery(t.Context(), q.ID, snapshot, errKind); err != nil {
			t.Fatal(err)
		}
	}
	draft := true
	observe(&protocol.DeliverySnapshot{State: "open", Draft: &draft}, "")
	read(protocol.IssueBoardPRDraft)
	if q, err := h.app.ClaimServerDelivery(t.Context()); err != nil || q != nil {
		t.Fatal("reading the board requested another provider query", q, err)
	}
	h.clock.Advance(6 * time.Minute)
	draft = false
	observe(&protocol.DeliverySnapshot{State: "open", Draft: &draft}, "")
	read(protocol.IssueBoardPROpen)
	h.clock.Advance(6 * time.Minute)
	observe(nil, "unreachable")
	read(protocol.IssueBoardPRUnknown)
}
