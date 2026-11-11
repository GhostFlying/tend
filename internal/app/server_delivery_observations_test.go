package app

import (
	"context"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestServerDeliverySharedFactsWithoutRunner(t *testing.T) {
	h := newHarness(t)
	h.app.serverDeliveryObservations = true
	first, second := observerItem(t, h), observerItem(t, h)
	q, err := h.app.ClaimServerDelivery(t.Context())
	if err != nil || q == nil {
		t.Fatal(q, err)
	}
	snapshot := &protocol.DeliverySnapshot{State: "open", HeadSHA: "head", BaseSHA: "base", TargetSHA: "target", Mergeability: "mergeable", CI: &protocol.DeliveryCI{HeadSHA: "head", State: "pending", Checks: []protocol.DeliveryCheck{{Name: "check", State: "in_progress"}}}}
	if err = h.app.RecordServerDelivery(t.Context(), q.ID, snapshot, ""); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{first.WorkItemID, second.WorkItemID} {
		o := readObservations(t, h, id)[0]
		if o.Observer != "server" || o.HeadSHA != "head" || o.RefreshPending || o.CI == nil || o.CI.State != "pending" {
			t.Fatal(o)
		}
	}
	if q, err = h.app.ClaimServerDelivery(t.Context()); err != nil || q != nil {
		t.Fatal("shared read repeated", q, err)
	}
	h.clock.Advance(6 * time.Minute)
	q, err = h.app.ClaimServerDelivery(t.Context())
	if err != nil || q == nil {
		t.Fatal("periodic read reused stale cache", q, err)
	}
	snapshot.State = "merged"
	snapshot.MergeSHA = "merged"
	snapshot.TargetSHA = "merged"
	snapshot.TargetContainsMerge = true
	snapshot.CI.State = "success"
	if err = h.app.RecordServerDelivery(t.Context(), q.ID, snapshot, ""); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{first.WorkItemID, second.WorkItemID} {
		o := readObservations(t, h, id)[0]
		if o.State != "merged" || !o.TargetContainsMerge || o.CI.State != "success" {
			t.Fatal(o)
		}
	}
}

func TestServerDeliveryBoundedRetryAndExpiredRead(t *testing.T) {
	h := newHarness(t)
	h.app.serverDeliveryObservations = true
	item := observerItem(t, h)
	for attempt := 1; attempt <= 3; attempt++ {
		q, err := h.app.ClaimServerDelivery(t.Context())
		if err != nil || q == nil {
			t.Fatal(attempt, q, err)
		}
		if attempt == 1 {
			h.clock.Advance(serverDeliveryLease + time.Second)
			if expired, err := h.app.ClaimServerDelivery(t.Context()); err != nil || expired != nil {
				t.Fatal(expired, err)
			}
			// The original late read remains history and cannot erase the timeout.
			if err = h.app.RecordServerDelivery(t.Context(), q.ID, &protocol.DeliverySnapshot{State: "merged"}, ""); err != nil {
				t.Fatal(err)
			}
		} else if err = h.app.RecordServerDelivery(t.Context(), q.ID, nil, "rate_limited"); err != nil {
			t.Fatal(err)
		}
		o := readObservations(t, h, item.WorkItemID)[0]
		if o.RefreshPending || o.ErrorKind == "" || o.ErrorMessage == "" || o.RetryCount != attempt-1 {
			t.Fatal(attempt, o)
		}
		if attempt < 3 {
			if o.RetryAt <= h.clock.Now().UnixMilli() {
				t.Fatal(o)
			}
			h.clock.Advance(time.Duration(o.RetryAt-h.clock.Now().UnixMilli()) * time.Millisecond)
		} else if o.RetryAt != 0 {
			t.Fatal("exhausted budget still pending", o)
		}
	}
	if q, err := h.app.ClaimServerDelivery(t.Context()); err != nil || q != nil {
		t.Fatal(q, err)
	}
}

func TestServerDeliveryFencesOriginalRunnerNonce(t *testing.T) {
	h := newHarness(t)
	item := observerItem(t, h)
	observerHello(t, h, "linux-1", "old-observer")
	old := pollDelivery(t, h, "linux-1", "old-observer")[0]
	h.app.serverDeliveryObservations = true
	if q, err := h.app.ClaimServerDelivery(t.Context()); err != nil || q != nil {
		t.Fatal("overlapping original read", q, err)
	}
	if assignments := pollDelivery(t, h, "linux-1", "old-observer"); len(assignments) != 0 {
		t.Fatal(assignments)
	}
	h.clock.Advance(2*time.Minute + time.Second)
	q, err := h.app.ClaimServerDelivery(t.Context())
	if err != nil || q == nil {
		t.Fatal(q, err)
	}
	if err = h.app.RecordServerDelivery(t.Context(), q.ID, &protocol.DeliverySnapshot{State: "open", HeadSHA: "new-head"}, ""); err != nil {
		t.Fatal(err)
	}
	if accepted(t, reportDelivery(t, h, "linux-1", "old-observer", old, "merged", "")) {
		t.Fatal("stale runner replaced server facts")
	}
	o := readObservations(t, h, item.WorkItemID)[0]
	if o.State != "open" || o.HeadSHA != "new-head" {
		t.Fatal(o)
	}
	if err = h.store.Read(context.Background(), func(tx *store.ReadTx) error {
		var raw int64
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT late_report_at FROM retired_runner_delivery_queries WHERE nonce=?`, old.Nonce).Scan(&raw); err != nil {
			return err
		}
		if raw == 0 {
			t.Error("late report lost")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestServerDeliveryMissingProviderEndsPendingAndPreservesFacts(t *testing.T) {
	h := newHarness(t)
	h.app.serverDeliveryObservations = true
	item := observerItem(t, h)
	q, err := h.app.ClaimServerDelivery(t.Context())
	if err != nil || q == nil {
		t.Fatal(q, err)
	}
	if err = h.app.RecordServerDelivery(t.Context(), q.ID, nil, "provider_not_configured"); err != nil {
		t.Fatal(err)
	}
	o := readObservations(t, h, item.WorkItemID)[0]
	if o.RefreshPending || o.RetryAt != 0 || o.ErrorKind != "provider_not_configured" || o.State != "unknown" || o.ErrorMessage == "" {
		t.Fatal(o)
	}
	projected := protocol.CanonicalDeliveryObservation(o)
	if projected.ErrorMessage != o.ErrorMessage || projected.RetryCount != o.RetryCount || projected.Observer != "server" || projected.RetryAt != o.RetryAt {
		t.Fatal("canonical Issue omitted observer diagnostics", projected)
	}
	if q, err = h.app.ClaimServerDelivery(t.Context()); err != nil || q != nil {
		t.Fatal(q, err)
	}
}

func TestServerDeliveryLateAssociationReusesOriginalCheckTime(t *testing.T) {
	h := newHarness(t)
	h.app.serverDeliveryObservations = true
	first := observerItem(t, h)
	q, err := h.app.ClaimServerDelivery(t.Context())
	if err != nil || q == nil {
		t.Fatal(q, err)
	}
	if err = h.app.RecordServerDelivery(t.Context(), q.ID, &protocol.DeliverySnapshot{State: "open", HeadSHA: "head"}, ""); err != nil {
		t.Fatal(err)
	}
	checked := readObservations(t, h, first.WorkItemID)[0].CheckedAt
	h.clock.Advance(time.Minute)
	second := observerItem(t, h)
	q, err = h.app.ClaimServerDelivery(t.Context())
	if err != nil || q != nil {
		t.Fatal("new association repeated provider query", q, err)
	}
	got := readObservations(t, h, second.WorkItemID)[0]
	if got.CheckedAt != checked || got.AttemptedAt != checked || got.HeadSHA != "head" || got.RefreshPending {
		t.Fatal("shared cache invented a fresh check", got, checked)
	}
}

func TestServerDeliveryUnchangedFactsDoNotWakeCanonicalOwner(t *testing.T) {
	h := hostedManagerHarness(t)
	h.app.serverDeliveryObservations = true
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	plan := issue.Plan
	plan.Deliveries = []protocol.IssueDelivery{{DeliveryID: "pr", Provider: "github", Repository: "example/project", Number: 7}}
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "update", protocol.UpdateIssuePayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Plan: plan}))
	q, err := h.app.ClaimServerDelivery(t.Context())
	if err != nil || q == nil {
		t.Fatal(q, err)
	}
	snapshot := &protocol.DeliverySnapshot{State: "open", HeadSHA: "head"}
	if err = h.app.RecordServerDelivery(t.Context(), q.ID, snapshot, ""); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(3 * time.Minute)
	sweepStatus(t, h)
	if got := issueChangeSources(t, h, issue.IssueID); got != 1 {
		t.Fatal("changed facts did not coalesce for owner", got)
	}
	before := h.count("message_notices")
	h.clock.Advance(3 * time.Minute)
	q, err = h.app.ClaimServerDelivery(t.Context())
	if err != nil || q == nil {
		t.Fatal(q, err)
	}
	if err = h.app.RecordServerDelivery(t.Context(), q.ID, snapshot, ""); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(3 * time.Minute)
	sweepStatus(t, h)
	if got := issueChangeSources(t, h, issue.IssueID); got != 1 || h.count("message_notices") != before {
		t.Fatal("unchanged provider facts woke owner", got)
	}
}

func TestServerDeliveryReadFailurePreservesOriginalRunnerFacts(t *testing.T) {
	h := newHarness(t)
	item := observerItem(t, h)
	observerHello(t, h, "linux-1", "observer")
	original := pollDelivery(t, h, "linux-1", "observer")[0]
	reportDelivery(t, h, "linux-1", "observer", original, "open", "")
	previous := readObservations(t, h, item.WorkItemID)[0]
	h.app.serverDeliveryObservations = true
	q, err := h.app.ClaimServerDelivery(t.Context())
	if err != nil || q == nil {
		t.Fatal(q, err)
	}
	if err = h.app.RecordServerDelivery(t.Context(), q.ID, nil, "authentication_required"); err != nil {
		t.Fatal(err)
	}
	got := readObservations(t, h, item.WorkItemID)[0]
	if got.HeadSHA != previous.HeadSHA || got.State != previous.State || got.CheckedAt != previous.CheckedAt || got.ErrorKind != "authentication_required" || got.RefreshPending {
		t.Fatal(got)
	}
}

func TestServerDeliveryLargeAssociationGroupHasBoundedCachedFanout(t *testing.T) {
	h := newHarness(t)
	h.app.serverDeliveryObservations = true
	items := make([]protocol.WorkItem, 101)
	for i := range items {
		items[i] = observerItem(t, h)
	}
	q, err := h.app.ClaimServerDelivery(t.Context())
	if err != nil || q == nil {
		t.Fatal(q, err)
	}
	if err = h.app.RecordServerDelivery(t.Context(), q.ID, &protocol.DeliverySnapshot{State: "open", HeadSHA: "head"}, ""); err != nil {
		t.Fatal(err)
	}
	if q, err = h.app.ClaimServerDelivery(t.Context()); err != nil || q != nil {
		t.Fatal("fanout repeated external query", q, err)
	}
	if fact := normalizedDeliveryFact(t, h); fact.FactRevision != 1 {
		t.Fatal("cached fanout rewrote actual provider fact", fact)
	}
	if got := h.count("delivery_observations WHERE json_extract(query_json,'$.observed_revision')=1"); got != len(items) {
		t.Fatal("bounded fanout did not consume each member revision", got)
	}
	for _, item := range items {
		got := readObservations(t, h, item.WorkItemID)
		if len(got) != 1 || got[0].HeadSHA != "head" || got[0].RefreshPending {
			t.Fatal(item.WorkItemID, got)
		}
	}
	if h.count("server_delivery_queries WHERE status='completed'") != 1 || h.count("server_delivery_queries WHERE status='cached'") != 1 {
		t.Fatal("shared read history lost")
	}
}

func TestServerDeliveryExplicitRefreshFanoutReadsProviderOnce(t *testing.T) {
	h := newHarness(t)
	h.app.serverDeliveryObservations = true
	items := make([]protocol.WorkItem, 101)
	for i := range items {
		items[i] = observerItem(t, h)
	}
	// Populate the whole group first; every old and refreshed observation can
	// share a millisecond, so timestamps alone cannot prioritize pending work.
	initial, err := h.app.ClaimServerDelivery(t.Context())
	if err != nil || initial == nil {
		t.Fatal(initial, err)
	}
	if err = h.app.RecordServerDelivery(t.Context(), initial.ID, &protocol.DeliverySnapshot{State: "open", HeadSHA: "before"}, ""); err != nil {
		t.Fatal(err)
	}
	if initial, err = h.app.ClaimServerDelivery(t.Context()); err != nil || initial != nil {
		t.Fatal(initial, err)
	}
	for _, item := range items {
		serverRefreshDelivery(t, h, item)
	}
	q, err := h.app.ClaimServerDelivery(t.Context())
	if err != nil || q == nil {
		t.Fatal(q, err)
	}
	if err = h.app.RecordServerDelivery(t.Context(), q.ID, &protocol.DeliverySnapshot{State: "open", HeadSHA: "head"}, ""); err != nil {
		t.Fatal(err)
	}
	if q, err = h.app.ClaimServerDelivery(t.Context()); err != nil || q != nil {
		t.Fatal("queued explicit fanout repeated provider query", q, err)
	}
	for _, item := range items {
		o := readObservations(t, h, item.WorkItemID)[0]
		if o.RefreshPending || o.HeadSHA != "head" {
			t.Fatal(o)
		}
	}
}

func TestServerDeliveryRefreshAfterClaimRequiresNewReadAtSameTime(t *testing.T) {
	h := newHarness(t)
	h.app.serverDeliveryObservations = true
	item := observerItem(t, h)
	serverRefreshDelivery(t, h, item)
	q, err := h.app.ClaimServerDelivery(t.Context())
	if err != nil || q == nil {
		t.Fatal(q, err)
	}
	serverRefreshDelivery(t, h, item)
	if err = h.app.RecordServerDelivery(t.Context(), q.ID, &protocol.DeliverySnapshot{State: "open", HeadSHA: "old"}, ""); err != nil {
		t.Fatal(err)
	}
	q, err = h.app.ClaimServerDelivery(t.Context())
	if err != nil || q == nil {
		t.Fatal("later refresh was swallowed by original read", q, err)
	}
	if err = h.app.RecordServerDelivery(t.Context(), q.ID, &protocol.DeliverySnapshot{State: "open", HeadSHA: "new"}, ""); err != nil {
		t.Fatal(err)
	}
	o := readObservations(t, h, item.WorkItemID)[0]
	if o.RefreshPending || o.HeadSHA != "new" {
		t.Fatal(o)
	}
}

func serverRefreshDelivery(t *testing.T, h *harness, item protocol.WorkItem) {
	t.Helper()
	op := h.exec(owner, h.env(protocol.OpRefreshDelivery, workTarget(item.WorkItemID), protocol.RefreshDeliveryPayload{DeliveryID: "delivery-1"}, rev(item.Revision)), RefreshDelivery{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}

func TestServerDeliveryPreparationStopsNewClaimsButRetainsOriginalReport(t *testing.T) {
	h := newHarness(t)
	h.app.serverDeliveryObservations = true
	item := observerItem(t, h)
	q, err := h.app.ClaimServerDelivery(t.Context())
	if err != nil || q == nil {
		t.Fatal(q, err)
	}
	prepared, err := h.app.ChangeArchivePreparation(t.Context(), protocol.ArchiveControlRequest{RequestID: h.ids.New(protocol.PrefixRequest)}, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = h.app.RecordServerDelivery(t.Context(), q.ID, &protocol.DeliverySnapshot{State: "open", HeadSHA: "before"}, ""); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(6 * time.Minute)
	if next, err := h.app.ClaimServerDelivery(t.Context()); err != nil || next != nil {
		t.Fatal("maintenance started new external read", next, err)
	}
	if o := readObservations(t, h, item.WorkItemID)[0]; o.HeadSHA != "before" || o.RefreshPending {
		t.Fatal(o)
	}
	if _, err = h.app.ChangeArchivePreparation(t.Context(), protocol.ArchiveControlRequest{RequestID: h.ids.New(protocol.PrefixRequest), ExpectedControlRevision: prepared.Revision}, false); err != nil {
		t.Fatal(err)
	}
	if next, err := h.app.ClaimServerDelivery(t.Context()); err != nil || next == nil {
		t.Fatal("observer did not resume after explicit preparation cancel", next, err)
	}
}

func TestServerDeliveryMergedLineageRetriesSharedReadAndStopsAfterRecovery(t *testing.T) {
	h := newHarness(t)
	h.app.serverDeliveryObservations = true
	first, second := observerItem(t, h), observerItem(t, h)
	query, err := h.app.ClaimServerDelivery(t.Context())
	if err != nil || query == nil {
		t.Fatal(query, err)
	}
	if err = h.app.RecordServerDelivery(t.Context(), query.ID, &protocol.DeliverySnapshot{State: "open", HeadSHA: "head"}, ""); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(6 * time.Minute)
	query, err = h.app.ClaimServerDelivery(t.Context())
	if err != nil || query == nil {
		t.Fatal(query, err)
	}
	snapshot := protocol.DeliverySnapshot{State: "merged", HeadSHA: "head", BaseSHA: "base", TargetSHA: "later-target", MergeSHA: "merge", LineageErrorKind: "timeout"}
	if err = h.app.RecordServerDelivery(t.Context(), query.ID, &snapshot, ""); err != nil {
		t.Fatal(err)
	}
	failed := readObservations(t, h, first.WorkItemID)[0]
	if failed.State != "merged" || failed.MergeSHA != "merge" || failed.LineageErrorKind != "timeout" || failed.ErrorKind != "timeout" || failed.RetryCount != 0 || failed.RetryAt <= h.clock.Now().UnixMilli() || failed.RefreshPending {
		t.Fatal("partial lineage failure lost facts or a fresh bounded retry budget", failed)
	}
	h.clock.Advance(time.Duration(failed.RetryAt-h.clock.Now().UnixMilli()) * time.Millisecond)
	retry, err := h.app.ClaimServerDelivery(t.Context())
	if err != nil || retry == nil || retry.ID == query.ID {
		t.Fatal("known merge with missing lineage stopped retrying", retry, err)
	}
	snapshot.LineageErrorKind = ""
	snapshot.TargetContainsMerge = true
	if err = h.app.RecordServerDelivery(t.Context(), retry.ID, &snapshot, ""); err != nil {
		t.Fatal(err)
	}
	for _, item := range []protocol.WorkItem{first, second} {
		o := readObservations(t, h, item.WorkItemID)[0]
		if o.State != "merged" || !o.TargetContainsMerge || o.LineageErrorKind != "" || o.ErrorKind != "" || o.RetryAt != 0 || o.RefreshPending {
			t.Fatal("shared bounded read did not restore real ancestry", o)
		}
	}
	h.clock.Advance(time.Hour)
	if q, err := h.app.ClaimServerDelivery(t.Context()); err != nil || q != nil {
		t.Fatal("successful terminal merge kept polling", q, err)
	}
}

func TestServerDeliveryMergedLineageExhaustsBudgetAndRetainsOriginalReceipts(t *testing.T) {
	for _, kind := range []string{"timeout", "rate_limited"} {
		t.Run(kind, func(t *testing.T) {
			h := newHarness(t)
			h.app.serverDeliveryObservations = true
			item := observerItem(t, h)
			snapshot := protocol.DeliverySnapshot{State: "merged", HeadSHA: "head", BaseSHA: "base", TargetSHA: "later-target", MergeSHA: "merge", LineageErrorKind: kind}
			for attempt := 0; attempt < 3; attempt++ {
				query, err := h.app.ClaimServerDelivery(t.Context())
				if err != nil || query == nil {
					t.Fatal(attempt, query, err)
				}
				if err = h.app.RecordServerDelivery(t.Context(), query.ID, &snapshot, ""); err != nil {
					t.Fatal(err)
				}
				o := readObservations(t, h, item.WorkItemID)[0]
				if o.ErrorKind != kind || o.LineageErrorKind != kind || o.State != "merged" || o.RetryCount != attempt || o.RefreshPending || o.ErrorMessage == "" {
					t.Fatal(attempt, o)
				}
				// A lost report reply reuses the original query/request. It neither
				// consumes another attempt nor invents another provider observation.
				if err = h.app.RecordServerDelivery(t.Context(), query.ID, &snapshot, ""); err != nil {
					t.Fatal(err)
				}
				replay := readObservations(t, h, item.WorkItemID)[0]
				if replay.Generation != o.Generation || replay.RetryCount != o.RetryCount || replay.RetryAt != o.RetryAt {
					t.Fatal("report replay changed original attempt identity", replay, o)
				}
				if attempt < 2 {
					if o.RetryAt <= h.clock.Now().UnixMilli() {
						t.Fatal("missing finite retry deadline", o)
					}
					h.clock.Advance(time.Duration(o.RetryAt-h.clock.Now().UnixMilli()) * time.Millisecond)
				} else if o.RetryAt != 0 {
					t.Fatal("exhausted lineage still pending", o)
				}
			}
			h.clock.Advance(time.Hour)
			if q, err := h.app.ClaimServerDelivery(t.Context()); err != nil || q != nil {
				t.Fatal("exhausted terminal lineage restarted indefinitely", q, err)
			}
			if h.count("server_delivery_queries") != 3 {
				t.Fatal("unexpected external query after budget exhaustion", h.count("server_delivery_queries"))
			}
			serverRefreshDelivery(t, h, item)
			q, err := h.app.ClaimServerDelivery(t.Context())
			if err != nil || q == nil {
				t.Fatal("explicit refresh did not recover exhausted proof", q, err)
			}
			snapshot.LineageErrorKind = ""
			snapshot.TargetContainsMerge = true
			if err = h.app.RecordServerDelivery(t.Context(), q.ID, &snapshot, ""); err != nil {
				t.Fatal(err)
			}
			o := readObservations(t, h, item.WorkItemID)[0]
			if o.ErrorKind != "" || !o.TargetContainsMerge || o.RetryAt != 0 || o.RefreshPending {
				t.Fatal(o)
			}
		})
	}
}
