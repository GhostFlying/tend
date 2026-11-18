package app

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func TestIssueDeliveryProgressFollowsObservedCandidate(t *testing.T) {
	h := newHarness(t)
	h.app.serverDeliveryObservations = true
	ref := protocol.IssueDelivery{DeliveryID: "pr", Provider: "github", Repository: "example/project", Number: 343}
	plan := protocol.IssuePlan{Title: "Candidate progress", Project: DefaultProject, Intent: "Track the delivery", Deliveries: []protocol.IssueDelivery{ref}}
	created := h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, canonicalOwnerIssueCreate{Plan: plan}, nil), historicalAdmittedIssueFixture{})
	var issue protocol.Issue
	if created.Status != protocol.OpSucceeded || json.Unmarshal(created.Result, &issue) != nil {
		t.Fatal(created)
	}

	record := func(s *protocol.DeliverySnapshot, kind string) {
		t.Helper()
		query, err := h.app.ClaimServerDelivery(t.Context())
		if err != nil || query == nil {
			t.Fatal(query, err)
		}
		if err = h.app.RecordServerDelivery(t.Context(), query.ID, s, kind); err != nil {
			t.Fatal(err)
		}
		h.clock.Advance(6 * time.Minute)
	}
	read := func() (protocol.IssueDeliveryView, *protocol.IssueProgress, protocol.IssueActivityPage) {
		t.Helper()
		var deliveries protocol.IssueDeliveryPage
		var detail protocol.IssueView
		var activity protocol.IssueActivityPage
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			var err error
			deliveries, err = views.CanonicalIssueDeliveries(t.Context(), tx.Q(), issue.IssueID)
			if err != nil {
				return err
			}
			detail, err = views.CanonicalIssue(t.Context(), tx.Q(), issue.IssueID, 0)
			if err != nil {
				return err
			}
			activity, err = views.IssueActivity(t.Context(), tx.Q(), issue.IssueID, 0)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return deliveries.Items[0], detail.Progress, activity
	}
	update := func(d *protocol.IssueDeliveryProgress, summary string) protocol.Operation {
		t.Helper()
		current := canonicalTaskIssue(t, h, issue.IssueID)
		p := protocol.UpdateIssueProgressPayload{ExpectedAssignmentEpoch: current.AssignmentEpoch, Delivery: d, Summary: summary}
		return h.exec(owner, h.env("issues.update_progress", protocol.Target{Type: "issue", ID: current.IssueID}, p, rev(current.Revision)), UpdateIssueProgress{})
	}

	snapshot := &protocol.DeliverySnapshot{State: "open", HeadSHA: "old", BaseSHA: "base", TargetSHA: "tip", Mergeability: "conflicting", CI: &protocol.DeliveryCI{HeadSHA: "old", State: "pending"}}
	record(snapshot, "")
	delivery, _, _ := read()
	conflict := delivery.Observation.LastConflict
	if conflict == nil || conflict.HeadSHA != "old" || conflict.TargetSHA != "tip" ||
		conflict.CheckedAt == 0 || conflict.Observer != "server" || delivery.ConflictProgress != nil {
		t.Fatalf("observing a conflict must not imply an owner action: %+v", delivery)
	}

	binding := protocol.IssueDeliveryProgress{DeliveryID: ref.DeliveryID, Provider: ref.Provider, Repository: ref.Repository, Number: ref.Number, HeadSHA: "old", BaseSHA: "base", TargetSHA: "tip", ConflictRevision: conflict.Revision, ConflictAction: "seen"}
	if op := update(&binding, "Race checks are running on the old candidate"); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	delivery, _, _ = read()
	if p := delivery.ConflictProgress; p == nil || p.Delivery.ConflictAction != "seen" || p.UpdatedBy != issue.Assignee {
		t.Fatalf("missing authenticated acknowledgment: %+v", p)
	}
	record(nil, "timeout")
	delivery, _, _ = read()
	if current := delivery.Observation.LastConflict; current == nil || *current != *conflict ||
		delivery.Observation.ErrorKind != "timeout" {
		t.Fatalf("a failed read replaced conflict evidence: %+v", delivery)
	}
	binding.ConflictAction = "working"
	if op := update(&binding, "Resolving the conflict"); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	delivery, _, _ = read()
	if p := delivery.ConflictProgress; p == nil || p.Delivery.ConflictAction != "working" {
		t.Fatalf("missing conflict work: %+v", p)
	}

	snapshot.HeadSHA, snapshot.BaseSHA, snapshot.Mergeability = "new", "tip", "mergeable"
	snapshot.CI = &protocol.DeliveryCI{HeadSHA: "new", State: "pending"}
	record(snapshot, "")
	delivery, progress, history := read()
	if delivery.Observation.HeadSHA != "new" || delivery.Observation.CI.HeadSHA != "new" ||
		delivery.Observation.LastConflict.HeadSHA != "old" || delivery.ConflictProgress != nil {
		t.Fatalf("current and previous candidates were mixed: %+v", delivery)
	}
	if progress == nil || !progress.Historical || progress.HistoricalReason != "delivery_candidate_changed" {
		t.Fatalf("old candidate progress is still current: %+v", progress)
	}
	var oldCI, oldProgress, newCandidate bool
	for _, item := range history.Items {
		if d := item.Delivery; d != nil {
			oldCI = oldCI || d.Observation.HeadSHA == "old" && d.Observation.CI.HeadSHA == "old"
			newCandidate = newCandidate || d.Observation.HeadSHA == "new"
		}
		oldProgress = oldProgress || item.Progress != nil && item.Progress.Summary == "Race checks are running on the old candidate"
	}
	if !oldCI || !oldProgress || !newCandidate {
		t.Fatalf("missing candidate history: old CI=%v old progress=%v new candidate=%v", oldCI, oldProgress, newCandidate)
	}
	if op := update(&binding, "Late progress"); op.Error == nil || op.Error.Code != protocol.CodeStaleRevision {
		t.Fatalf("accepted action against old candidate: %+v", op)
	}
}
