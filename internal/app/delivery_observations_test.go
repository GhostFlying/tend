package app

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func observerItem(t *testing.T, h *harness) protocol.WorkItem {
	t.Helper()
	item := protocol.WorkItem{WorkItemID: h.ids.New(protocol.PrefixWorkItem), Revision: 1, Status: "open", CreatedAt: h.clock.Now().UnixMilli(), UpdatedAt: h.clock.Now().UnixMilli(), WorkItemPlan: protocol.WorkItemPlan{Title: "observe", Project: DefaultProject, Intent: "track publication", Deliveries: []protocol.WorkItemDelivery{{DeliveryID: "delivery-1", Provider: "github", Repository: "example/project", Number: 7}}}}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveWorkItem(t.Context(), item, 0, map[string]any{}) }); err != nil {
		t.Fatal(err)
	}
	return item
}
func observerHello(t *testing.T, h *harness, host, instance string) {
	t.Helper()
	rt := testRuntime(instance)
	rt.Capabilities = append(rt.Capabilities, protocol.CapabilityDeliveryGitHub)
	if op := h.versionedHello(host, rt); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}
func pollDelivery(t *testing.T, h *harness, host, instance string) []protocol.DeliveryAssignment {
	t.Helper()
	op := h.exec(runner(host), h.env(protocol.OpDeliveriesPoll, protocol.Target{Type: "host", ID: host}, protocol.DeliveryPollPayload{InstanceID: instance}, nil), PollDeliveries{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var result protocol.DeliveryPollResult
	if err := json.Unmarshal(op.Result, &result); err != nil {
		t.Fatal(err)
	}
	return result.Assignments
}
func reportDelivery(t *testing.T, h *harness, host, instance string, assignment protocol.DeliveryAssignment, state, errorKind string) protocol.Operation {
	t.Helper()
	p := protocol.DeliveryReportPayload{InstanceID: instance, WorkItemID: assignment.WorkItemID, DeliveryID: assignment.Delivery.DeliveryID, Generation: assignment.Generation, Nonce: assignment.Nonce, ErrorKind: errorKind}
	if state != "" {
		p.Snapshot = &protocol.DeliverySnapshot{State: state, HeadSHA: "head"}
	}
	return h.exec(runner(host), h.env(protocol.OpDeliveriesReport, protocol.Target{Type: "host", ID: host}, p, nil), ReportDelivery{})
}
func readObservations(t *testing.T, h *harness, item string) []protocol.DeliveryObservation {
	t.Helper()
	var out []protocol.DeliveryObservation
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		out, err = store.ListDeliveryObservations(t.Context(), tx.Q(), item)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out
}
func accepted(t *testing.T, op protocol.Operation) bool {
	t.Helper()
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var result protocol.DeliveryReportResult
	if err := json.Unmarshal(op.Result, &result); err != nil {
		t.Fatal(err)
	}
	return result.Accepted
}

func TestDeliveryObservationPreservesFactsAndStopsTerminalPolling(t *testing.T) {
	h := newHarness(t)
	item := observerItem(t, h)
	observerHello(t, h, "linux-1", "observer")
	assignments := pollDelivery(t, h, "linux-1", "observer")
	if len(assignments) != 1 {
		t.Fatal(assignments)
	}
	if !accepted(t, reportDelivery(t, h, "linux-1", "observer", assignments[0], "open", "")) {
		t.Fatal("not applied")
	}
	first := readObservations(t, h, item.WorkItemID)[0]
	if first.State != "open" || first.CheckedAt == 0 || first.RefreshPending {
		t.Fatal(first)
	}
	if len(pollDelivery(t, h, "linux-1", "observer")) != 0 {
		t.Fatal("queried before five-minute interval")
	}
	h.clock.Advance(5 * time.Minute)
	assignment := pollDelivery(t, h, "linux-1", "observer")[0]
	if !accepted(t, reportDelivery(t, h, "linux-1", "observer", assignment, "", "authentication_required")) {
		t.Fatal("failure not recorded")
	}
	failed := readObservations(t, h, item.WorkItemID)[0]
	if failed.State != "open" || failed.HeadSHA != "head" || failed.CheckedAt != first.CheckedAt || failed.ErrorKind != "authentication_required" || failed.AttemptedAt <= first.AttemptedAt {
		t.Fatal(failed)
	}
	h.clock.Advance(5 * time.Minute)
	assignment = pollDelivery(t, h, "linux-1", "observer")[0]
	if !accepted(t, reportDelivery(t, h, "linux-1", "observer", assignment, "merged", "")) {
		t.Fatal("not merged")
	}
	h.clock.Advance(24 * time.Hour)
	if len(pollDelivery(t, h, "linux-1", "observer")) != 0 {
		t.Fatal("terminal provider object was periodically polled")
	}
	var current protocol.WorkItem
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		current, err = store.GetWorkItem(t.Context(), tx.Q(), item.WorkItemID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if current.Revision != 1 || current.Status != "open" {
		t.Fatalf("observation changed planning: %+v", current)
	}
}

func TestDeliveryObservationFreshnessInvalidatesViewsWithoutWakingAgent(t *testing.T) {
	h := newHarness(t)
	item := observerItem(t, h)
	observerHello(t, h, "linux-1", "observer")
	assignment := pollDelivery(t, h, "linux-1", "observer")[0]
	if !accepted(t, reportDelivery(t, h, "linux-1", "observer", assignment, "open", "")) {
		t.Fatal("initial observation rejected")
	}
	first := readObservations(t, h, item.WorkItemID)[0]
	notices, turns := h.count("message_notices"), h.count("outbox WHERE destination='main_thread'")
	h.clock.Advance(5 * time.Minute)
	assignment = pollDelivery(t, h, "linux-1", "observer")[0]
	if !accepted(t, reportDelivery(t, h, "linux-1", "observer", assignment, "open", "")) {
		t.Fatal("unchanged observation rejected")
	}
	if readObservations(t, h, item.WorkItemID)[0].CheckedAt <= first.CheckedAt || h.count("events WHERE kind='work_item.delivery_checked'") != 1 {
		t.Fatal("fresh verification did not invalidate live views")
	}
	if h.count("message_notices") != notices || h.count("outbox WHERE destination='main_thread'") != turns {
		t.Fatal("unchanged verification created conversation activity")
	}
}

func TestDeliveryObservationFailureProjectsAttentionAndRecovers(t *testing.T) {
	h := newHarness(t)
	item := observerItem(t, h)
	observerHello(t, h, "linux-1", "observer")
	checkColumn := func(want string) {
		t.Helper()
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			view, err := views.WorkItem(t.Context(), tx.Q(), item.WorkItemID)
			if err == nil && view.Column != want {
				t.Fatalf("column = %q, want %q", view.Column, want)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	assignment := pollDelivery(t, h, "linux-1", "observer")[0]
	if !accepted(t, reportDelivery(t, h, "linux-1", "observer", assignment, "", "authentication_required")) {
		t.Fatal("failed observation rejected")
	}
	checkColumn("attention")
	h.clock.Advance(5 * time.Minute)
	assignment = pollDelivery(t, h, "linux-1", "observer")[0]
	if !accepted(t, reportDelivery(t, h, "linux-1", "observer", assignment, "open", "")) {
		t.Fatal("recovery rejected")
	}
	checkColumn("unplanned")
}

func TestFault_DeliveryQueryGenerationRejectsOldHostAndRefreshReports(t *testing.T) {
	h := newHarness(t)
	item := observerItem(t, h)
	cfg := testConfig()
	second := cfg.Hosts[0]
	second.HostID = "linux-2"
	cfg.Hosts = append(cfg.Hosts, second)
	h.syncConfig(cfg)
	observerHello(t, h, "linux-1", "first")
	observerHello(t, h, "linux-2", "second")
	old := pollDelivery(t, h, "linux-1", "first")[0]
	h.clock.Advance(2 * time.Minute)
	newer := pollDelivery(t, h, "linux-2", "second")[0]
	if newer.Generation <= old.Generation || newer.Nonce == old.Nonce {
		t.Fatal("query identity reused")
	}
	if accepted(t, reportDelivery(t, h, "linux-1", "first", old, "closed", "")) {
		t.Fatal("late old host overwrote new query")
	}
	if !accepted(t, reportDelivery(t, h, "linux-2", "second", newer, "open", "")) {
		t.Fatal("new query rejected")
	}
	// A manual refresh is a new query generation even while the plan stays put.
	refresh := h.env(protocol.OpRefreshDelivery, protocol.Target{Type: "work_item", ID: item.WorkItemID}, protocol.RefreshDeliveryPayload{DeliveryID: "delivery-1"}, rev(1))
	if op := h.exec(owner, refresh, RefreshDelivery{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	after := pollDelivery(t, h, "linux-1", "first")[0]
	if accepted(t, reportDelivery(t, h, "linux-2", "second", newer, "closed", "")) {
		t.Fatal("replayed completed query applied")
	}
	if !accepted(t, reportDelivery(t, h, "linux-1", "first", after, "open", "")) {
		t.Fatal("manual query rejected")
	}
	// Replaying the owner envelope must not reopen the already-completed query.
	h.exec(owner, refresh, RefreshDelivery{})
	if readObservations(t, h, item.WorkItemID)[0].RefreshPending {
		t.Fatal("owner request replay repeated refresh")
	}
}

func TestDeliveryObserverRequiresCapabilityAndCurrentInstance(t *testing.T) {
	h := newHarness(t)
	observerItem(t, h)
	rt := testRuntime("old")
	h.versionedHello("linux-1", rt)
	if len(pollDelivery(t, h, "linux-1", "old")) != 0 {
		t.Fatal("assigned without capability")
	}
	observerHello(t, h, "linux-1", "new")
	op, err := h.app.Execute(context.Background(), runner("linux-1"), h.env(protocol.OpDeliveriesPoll, protocol.Target{Type: "host", ID: "linux-1"}, protocol.DeliveryPollPayload{InstanceID: "old"}, nil), PollDeliveries{})
	if err == nil && op.Status != protocol.OpFailed {
		t.Fatal("retired instance accepted")
	}
}

func TestDeliverySnoozeIntervalAndExplicitTerminalRefresh(t *testing.T) {
	h := newHarness(t)
	item := observerItem(t, h)
	observerHello(t, h, "linux-1", "observer")
	item.Status = "snoozed"
	item.Revision = 2
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveWorkItem(t.Context(), item, 1, map[string]any{}) }); err != nil {
		t.Fatal(err)
	}
	a := pollDelivery(t, h, "linux-1", "observer")[0]
	accepted(t, reportDelivery(t, h, "linux-1", "observer", a, "open", ""))
	h.clock.Advance(5 * time.Minute)
	if len(pollDelivery(t, h, "linux-1", "observer")) != 0 {
		t.Fatal("snoozed delivery polled at open cadence")
	}
	h.clock.Advance(55 * time.Minute)
	a = pollDelivery(t, h, "linux-1", "observer")[0]
	accepted(t, reportDelivery(t, h, "linux-1", "observer", a, "closed", ""))
	stale := h.exec(owner, h.env(protocol.OpRefreshDelivery, protocol.Target{Type: "work_item", ID: item.WorkItemID}, protocol.RefreshDeliveryPayload{DeliveryID: "delivery-1"}, rev(1)), RefreshDelivery{})
	if stale.Status != protocol.OpFailed || stale.Error.Code != protocol.CodeStaleRevision {
		t.Fatal("stale owner control accepted")
	}
	current := h.exec(owner, h.env(protocol.OpRefreshDelivery, protocol.Target{Type: "work_item", ID: item.WorkItemID}, protocol.RefreshDeliveryPayload{DeliveryID: "delivery-1"}, rev(2)), RefreshDelivery{})
	if current.Status != protocol.OpSucceeded {
		t.Fatal(current)
	}
	a = pollDelivery(t, h, "linux-1", "observer")[0]
	accepted(t, reportDelivery(t, h, "linux-1", "observer", a, "open", ""))
	if got := readObservations(t, h, item.WorkItemID)[0]; got.State != "open" || got.RefreshPending {
		t.Fatal(got)
	}
}

func observerQueue(t *testing.T, h *harness, count int) protocol.WorkItem {
	t.Helper()
	item := observerItem(t, h)
	item.Revision++
	for i := 2; i <= count; i++ {
		item.Deliveries = append(item.Deliveries, protocol.WorkItemDelivery{DeliveryID: fmt.Sprintf("delivery-%d", i), Provider: "github", Repository: "example/project", Number: int64(i)})
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveWorkItem(t.Context(), item, 1, map[string]any{}) }); err != nil {
		t.Fatal(err)
	}
	observerHello(t, h, "linux-1", "observer")
	return item
}

func TestDeliveryPollFairBeyondIntervalCapacity(t *testing.T) {
	h := newHarness(t)
	item := observerQueue(t, h, 12)
	counts := map[string]int{}
	// A five-minute interval fits only ten 30-second polls. Every reference
	// must still be observed repeatedly when the queue is larger than that.
	for i := 0; i < 40; i++ {
		assignments := pollDelivery(t, h, "linux-1", "observer")
		if len(assignments) != 1 {
			t.Fatalf("poll %d: %+v", i, assignments)
		}
		a := assignments[0]
		counts[a.Delivery.DeliveryID]++
		if !accepted(t, reportDelivery(t, h, "linux-1", "observer", a, "open", "")) {
			t.Fatal("report rejected")
		}
		h.clock.Advance(30 * time.Second)
	}
	for _, delivery := range item.Deliveries {
		if counts[delivery.DeliveryID] < 3 {
			t.Fatalf("starved %s: %v", delivery.DeliveryID, counts)
		}
	}
}

func TestDeliveryManualRefreshSharesFairQueue(t *testing.T) {
	h := newHarness(t)
	item := observerQueue(t, h, 12)
	refresh := func(id string) {
		t.Helper()
		op := h.exec(owner, h.env(protocol.OpRefreshDelivery, protocol.Target{Type: "work_item", ID: item.WorkItemID}, protocol.RefreshDeliveryPayload{DeliveryID: id}, rev(item.Revision)), RefreshDelivery{})
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	// Complete an initial round, making the final reference terminal. Its
	// explicit refresh must be reachable even while earlier references recur.
	var last string
	for range item.Deliveries {
		a := pollDelivery(t, h, "linux-1", "observer")[0]
		last = a.Delivery.DeliveryID
		accepted(t, reportDelivery(t, h, "linux-1", "observer", a, "closed", ""))
		h.clock.Advance(30 * time.Second)
	}
	for _, d := range item.Deliveries {
		refresh(d.DeliveryID)
	}
	seen := map[string]bool{}
	for i := 0; i < 24; i++ {
		// Fresh owner requests must not let the first target jump to the front
		// forever, nor move its pending refresh behind newer work repeatedly.
		refresh(item.Deliveries[0].DeliveryID)
		a := pollDelivery(t, h, "linux-1", "observer")[0]
		seen[a.Delivery.DeliveryID] = true
		accepted(t, reportDelivery(t, h, "linux-1", "observer", a, "open", ""))
		h.clock.Advance(30 * time.Second)
	}
	if len(seen) != len(item.Deliveries) || !seen[last] {
		t.Fatalf("manual refresh starvation: %v", seen)
	}
}

func TestFault_DeliveryExpiredQueriesDoNotStarveUntouchedReferences(t *testing.T) {
	h := newHarness(t)
	item := observerQueue(t, h, 12)
	seen := map[string]bool{}
	for range item.Deliveries {
		a := pollDelivery(t, h, "linux-1", "observer")[0]
		seen[a.Delivery.DeliveryID] = true
		// Lost reports leave two-minute reservations that expire before all
		// references can be visited. They must rejoin behind untouched work.
		h.clock.Advance(30 * time.Second)
	}
	if len(seen) != len(item.Deliveries) {
		t.Fatalf("expired queries starved new references: %v", seen)
	}
}
