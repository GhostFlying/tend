package app

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func normalizedDeliveryFact(t *testing.T, h *harness) store.DeliveryObservationFactRow {
	t.Helper()
	var fact store.DeliveryObservationFactRow
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		fact, err = store.GetDeliveryObservationFact(t.Context(), tx.Q(), protocol.IssueDelivery{Provider: "github", Repository: "example/project", Number: 7})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return fact
}

func TestDeliveryObservationServerToRunnerFencesLateServerResult(t *testing.T) {
	h := newHarness(t)
	h.app.serverDeliveryObservations = true
	first, second := observerItem(t, h), observerItem(t, h)
	q, err := h.app.ClaimServerDelivery(t.Context())
	if err != nil || q == nil {
		t.Fatal(q, err)
	}
	if err = h.app.RecordServerDelivery(t.Context(), q.ID, &protocol.DeliverySnapshot{State: "open", HeadSHA: "server-head"}, ""); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(6 * time.Minute)
	serverRefreshDelivery(t, h, first)
	old, err := h.app.ClaimServerDelivery(t.Context())
	if err != nil || old == nil {
		t.Fatal(old, err)
	}
	h.app.serverDeliveryObservations = false
	observerHello(t, h, "linux-1", "runner-observer")
	if assignments := pollDelivery(t, h, "linux-1", "runner-observer"); len(assignments) != 0 {
		t.Fatal("runner overlapped original server lease", assignments)
	}
	h.clock.Advance(serverDeliveryLease + time.Second)
	assignments := pollDelivery(t, h, "linux-1", "runner-observer")
	if len(assignments) != 1 {
		t.Fatal(assignments)
	}
	if !accepted(t, reportDelivery(t, h, "linux-1", "runner-observer", assignments[0], "open", "")) {
		t.Fatal("new runner result not accepted")
	}
	before := normalizedDeliveryFact(t, h)
	for _, item := range []protocol.WorkItem{first, second} {
		o := readObservations(t, h, item.WorkItemID)[0]
		if o.HeadSHA != "head" || o.Observer != "runner" || o.RefreshPending {
			t.Fatal("server cache shadowed runner result", o)
		}
	}
	if err = h.app.RecordServerDelivery(t.Context(), old.ID, &protocol.DeliverySnapshot{State: "merged", HeadSHA: "late-head"}, ""); err != nil {
		t.Fatal(err)
	}
	after := normalizedDeliveryFact(t, h)
	if after.FactRevision != before.FactRevision || after.Observation.HeadSHA != "head" {
		t.Fatal("late server read changed current fact", before, after)
	}
	if err = h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var status string
		var late int64
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT status,late_result_at FROM server_delivery_queries WHERE query_id=?`, old.ID).Scan(&status, &late); err != nil {
			return err
		}
		if status != "retired" || late == 0 {
			t.Errorf("original late read lost: status=%q late=%d", status, late)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestServerDeliverySameMillisecondFactsNotifyEachMemberOnce(t *testing.T) {
	h := newHarness(t)
	h.app.serverDeliveryObservations = true
	first, second := observerItem(t, h), observerItem(t, h)
	for attempt, head := range []string{"first", "second", "second"} {
		if attempt > 0 {
			serverRefreshDelivery(t, h, first)
		}
		beforeChanges := h.count("events WHERE kind='work_item.delivery_changed'")
		q, err := h.app.ClaimServerDelivery(t.Context())
		if err != nil || q == nil {
			t.Fatal(attempt, q, err)
		}
		if err = h.app.RecordServerDelivery(t.Context(), q.ID, &protocol.DeliverySnapshot{State: "open", HeadSHA: head}, ""); err != nil {
			t.Fatal(err)
		}
		fact := normalizedDeliveryFact(t, h)
		if fact.FactRevision != int64(attempt+1) {
			t.Fatal("one actual query must write one fact", fact)
		}
		expectedChanges := beforeChanges + 2
		if attempt == 2 {
			expectedChanges = beforeChanges + 1
		}
		if got := h.count("events WHERE kind='work_item.delivery_changed'"); got != expectedChanges {
			t.Fatal("semantic member notifications lost or repeated", attempt, got, expectedChanges)
		}
		for _, item := range []protocol.WorkItem{first, second} {
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				row, err := store.GetDeliveryObservation(t.Context(), tx.Q(), item.WorkItemID, "delivery-1")
				if err != nil {
					return err
				}
				if row.ObservedRevision != fact.FactRevision {
					t.Errorf("member missed fact revision: %+v", row)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestServerDeliveryUsesFrozenQueryBaselineAndStoresOnlyMetadata(t *testing.T) {
	h := newHarness(t)
	h.app.serverDeliveryObservations = true
	item := observerItem(t, h)
	q, err := h.app.ClaimServerDelivery(t.Context())
	if err != nil || q == nil {
		t.Fatal(q, err)
	}
	if fact := normalizedDeliveryFact(t, h); fact.FactRevision != 0 {
		t.Fatal("claim wrote a provider result", fact)
	}
	var baseline string
	var revision int64
	if err = h.store.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT baseline_upgrade_id,baseline_upgrade_revision FROM server_delivery_queries WHERE query_id=?`, q.ID).Scan(&baseline, &revision); err != nil {
			return err
		}
		row, err := store.GetDeliveryObservation(t.Context(), tx.Q(), item.WorkItemID, "delivery-1")
		if err != nil {
			return err
		}
		// A later member-local claim/baseline must not relabel the original read.
		row.QueryBaselineUpgradeID = "later-baseline"
		row.QueryBaselineUpgradeRevision = 17
		return tx.PutDeliveryObservationQuery(t.Context(), row)
	}); err != nil {
		t.Fatal(err)
	}
	if err = h.app.RecordServerDelivery(t.Context(), q.ID, &protocol.DeliverySnapshot{State: "open", HeadSHA: "frozen-head"}, ""); err != nil {
		t.Fatal(err)
	}
	fact := normalizedDeliveryFact(t, h)
	if fact.Observation.BaselineUpgradeID != baseline || fact.Observation.BaselineUpgradeRevision != revision {
		t.Fatal("member relabeled original query baseline", fact)
	}
	if err = h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var raw string
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT query_json FROM delivery_observations WHERE work_item_id=? AND delivery_id='delivery-1'`, item.WorkItemID).Scan(&raw); err != nil {
			return err
		}
		var metadata map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
			return err
		}
		for _, key := range []string{"head_sha", "base_sha", "state", "ci", "merge_sha", "checked_at"} {
			if _, exists := metadata[key]; exists {
				t.Errorf("Issue query retained provider snapshot key %q", key)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestServerDeliveryHandoffRetainsUnselectedRunnerReport(t *testing.T) {
	h := newHarness(t)
	items := make([]protocol.WorkItem, 101)
	last := ""
	for n := range items {
		items[n] = observerItem(t, h)
		if items[n].WorkItemID > last {
			last = items[n].WorkItemID
		}
	}
	// Reserve a runner read only for the last member in the server fanout order.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		for _, item := range items {
			if item.WorkItemID == last {
				continue
			}
			row, err := store.GetDeliveryObservation(t.Context(), tx.Q(), item.WorkItemID, "delivery-1")
			if err != nil {
				return err
			}
			row.NextCheckAt = h.clock.Now().Add(time.Hour).UnixMilli()
			if err = tx.PutDeliveryObservationQuery(t.Context(), row); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	observerHello(t, h, "linux-1", "old-observer")
	assignments := pollDelivery(t, h, "linux-1", "old-observer")
	if len(assignments) != 1 || assignments[0].WorkItemID != last {
		t.Fatal(assignments, last)
	}
	old := assignments[0]
	h.app.serverDeliveryObservations = true
	h.clock.Advance(2*time.Minute + time.Second)
	q, err := h.app.ClaimServerDelivery(t.Context())
	if err != nil || q == nil {
		t.Fatal(q, err)
	}
	if err = h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var selected bool
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT EXISTS(SELECT 1 FROM server_delivery_members WHERE query_id=? AND issue_id=?)`, q.ID, last).Scan(&selected); err != nil {
			return err
		}
		if selected {
			t.Error("fixture did not exercise the unselected 101st member")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = h.app.RecordServerDelivery(t.Context(), q.ID, &protocol.DeliverySnapshot{State: "open", HeadSHA: "current-server"}, ""); err != nil {
		t.Fatal(err)
	}
	before := normalizedDeliveryFact(t, h)
	for _, item := range items {
		if observation := readObservations(t, h, item.WorkItemID)[0]; observation.RefreshPending {
			t.Fatal("unselected pre-handoff runner nonce masked completed server facts", item.WorkItemID, observation)
		}
	}
	if accepted(t, reportDelivery(t, h, "linux-1", "old-observer", old, "merged", "")) {
		t.Fatal("unselected legacy nonce replaced post-handoff facts")
	}
	after := normalizedDeliveryFact(t, h)
	if before.FactRevision != after.FactRevision || after.Observation.HeadSHA != "current-server" {
		t.Fatal(before, after)
	}
	if err = h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var late int64
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT late_report_at FROM retired_runner_delivery_queries WHERE nonce=?`, old.Nonce).Scan(&late); err != nil {
			return err
		}
		if late == 0 {
			t.Error("unselected original nonce report not retained")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// Restore an original pre-normalization query, when separate Issue references
// could be claimed concurrently. The public request fields remain unchanged.
func legacyConcurrentDeliveryQuery(t *testing.T, h *harness, original protocol.DeliveryAssignment, other protocol.WorkItem, delta time.Duration) protocol.DeliveryAssignment {
	t.Helper()
	assignment := original
	assignment.WorkItemID = other.WorkItemID
	assignment.Nonce = h.ids.New(protocol.PrefixRequest)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		row, err := store.GetDeliveryObservation(t.Context(), tx.Q(), original.WorkItemID, original.Delivery.DeliveryID)
		if err != nil {
			return err
		}
		row.Observation.WorkItemID = assignment.WorkItemID
		row.HostID, row.InstanceID, row.Nonce = "linux-2", "second", assignment.Nonce
		row.QueryUntil += delta.Milliseconds()
		if err := tx.PutDeliveryObservationQuery(t.Context(), row); err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `INSERT INTO observation_jobs(issue_id,delivery_id,status,event_id,due_at,updated_at) VALUES(?,?,'claimed','',?,?) ON CONFLICT(issue_id,delivery_id) DO UPDATE SET status='claimed',event_id=''`, assignment.WorkItemID, assignment.Delivery.DeliveryID, h.clock.Now().UnixMilli(), h.clock.Now().UnixMilli())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return assignment
}

func runnerSharedDeliveryFixture(t *testing.T) (*harness, protocol.DeliveryAssignment, protocol.WorkItem) {
	t.Helper()
	h := newHarness(t)
	first, second := observerItem(t, h), observerItem(t, h)
	cfg := testConfig()
	other := cfg.Hosts[0]
	other.HostID = "linux-2"
	cfg.Hosts = append(cfg.Hosts, other)
	h.syncConfig(cfg)
	observerHello(t, h, "linux-1", "first")
	observerHello(t, h, "linux-2", "second")
	assignments := pollDelivery(t, h, "linux-1", "first")
	if len(assignments) != 1 {
		t.Fatal(assignments)
	}
	if assignments[0].WorkItemID == second.WorkItemID {
		second = first
	}
	return h, assignments[0], second
}

func TestRunnerSharedDeliverySerializesNewClaimsAndAcceptsOriginalHistory(t *testing.T) {
	h, old, _ := runnerSharedDeliveryFixture(t)
	if next := pollDelivery(t, h, "linux-2", "second"); len(next) != 0 {
		t.Fatal("same PR received an overlapping physical read", next)
	}
	h.clock.Advance(2*time.Minute + time.Second)
	next := pollDelivery(t, h, "linux-2", "second")
	if len(next) != 1 || next[0].WorkItemID == old.WorkItemID {
		t.Fatal(next)
	}
	if !accepted(t, reportDelivery(t, h, "linux-2", "second", next[0], "merged", "")) {
		t.Fatal("new result rejected")
	}
	before := normalizedDeliveryFact(t, h)
	for range 2 {
		if !accepted(t, reportDelivery(t, h, "linux-1", "first", old, "open", "")) {
			t.Fatal("original valid report lost its existing acceptance semantics")
		}
	}
	after := normalizedDeliveryFact(t, h)
	if after.FactRevision != before.FactRevision || after.Observation.State != "merged" {
		t.Fatal("old report replaced the shared fact", before, after)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var policy bool
		var raw int64
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT accept_late_report,late_report_at FROM retired_runner_delivery_queries WHERE nonce=?`, old.Nonce).Scan(&policy, &raw); err != nil {
			return err
		}
		if !policy || raw == 0 {
			t.Errorf("original report disposition lost: policy=%v at=%d", policy, raw)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerSharedDeliveryOrdersLegacyConcurrentResults(t *testing.T) {
	for _, newerFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "newer-first", false: "older-first"}[newerFirst], func(t *testing.T) {
			h, old, other := runnerSharedDeliveryFixture(t)
			newer := legacyConcurrentDeliveryQuery(t, h, old, other, time.Second)
			reportOld := func() {
				if !accepted(t, reportDelivery(t, h, "linux-1", "first", old, "open", "")) {
					t.Fatal("valid legacy report rejected")
				}
			}
			if !newerFirst {
				reportOld()
				if fact := normalizedDeliveryFact(t, h); fact.FactRevision != 0 {
					t.Fatal("older read gained current authority over a newer in-flight read", fact)
				}
			}
			if !accepted(t, reportDelivery(t, h, "linux-2", "second", newer, "merged", "")) {
				t.Fatal("newer result rejected")
			}
			if newerFirst {
				reportOld()
			}
			if fact := normalizedDeliveryFact(t, h); fact.FactRevision != 1 || fact.Observation.State != "merged" {
				t.Fatal("old Issue query replaced newer PR fact", fact)
			}
		})
	}
}

func TestRunnerSharedDeliveryEqualLegacyDeadlinesKeepUnknownOrder(t *testing.T) {
	h, old, other := runnerSharedDeliveryFixture(t)
	second := legacyConcurrentDeliveryQuery(t, h, old, other, 0)
	if !accepted(t, reportDelivery(t, h, "linux-1", "first", old, "open", "")) || !accepted(t, reportDelivery(t, h, "linux-2", "second", second, "merged", "")) {
		t.Fatal("valid ambiguous reports were rejected")
	}
	if fact := normalizedDeliveryFact(t, h); fact.FactRevision != 0 {
		t.Fatal("equal deadlines were used to invent an ordering", fact)
	}
	h.clock.Advance(2*time.Minute + time.Second)
	assignments := pollDelivery(t, h, "linux-1", "first")
	if len(assignments) != 1 || !accepted(t, reportDelivery(t, h, "linux-1", "first", assignments[0], "merged", "")) {
		t.Fatal("ordinary bounded read did not recover the ambiguous queries", assignments)
	}
}

func TestRunnerSharedHistoryKeepsOriginalPayloadAndInstanceChecks(t *testing.T) {
	h, old, other := runnerSharedDeliveryFixture(t)
	newer := legacyConcurrentDeliveryQuery(t, h, old, other, time.Second)
	if !accepted(t, reportDelivery(t, h, "linux-2", "second", newer, "merged", "")) {
		t.Fatal("newer result rejected")
	}
	for _, payload := range []protocol.DeliveryReportPayload{
		{InstanceID: "first", WorkItemID: old.WorkItemID, DeliveryID: old.Delivery.DeliveryID, Generation: old.Generation, Nonce: old.Nonce},
		{InstanceID: "first", WorkItemID: old.WorkItemID, DeliveryID: old.Delivery.DeliveryID, Generation: old.Generation, Nonce: old.Nonce, Snapshot: &protocol.DeliverySnapshot{State: "invented"}},
		{InstanceID: "first", WorkItemID: old.WorkItemID, DeliveryID: old.Delivery.DeliveryID, Generation: old.Generation, Nonce: old.Nonce, ErrorKind: "invented"},
	} {
		op := h.exec(runner("linux-1"), h.env(protocol.OpDeliveriesReport, protocol.Target{Type: "host", ID: "linux-1"}, payload, nil), ReportDelivery{})
		if op.Status != protocol.OpFailed || op.Error == nil || op.Error.Code != protocol.CodeInvalidRequest {
			t.Fatal("invalid original payload was accepted as history", op)
		}
	}
	observerHello(t, h, "linux-1", "replacement")
	if op := reportDelivery(t, h, "linux-1", "first", old, "open", ""); op.Status != protocol.OpFailed || op.Error == nil || op.Error.Code != protocol.CodeInvalidTransition {
		t.Fatal("superseded host instance gained history acceptance", op)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var raw int64
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT late_report_at FROM retired_runner_delivery_queries WHERE nonce=?`, old.Nonce).Scan(&raw); err != nil {
			return err
		}
		if raw != 0 {
			t.Errorf("invalid payload recorded as an accepted original report: %d", raw)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerSharedTerminalFactNotifiesEveryReferenceInBoundedBatches(t *testing.T) {
	h := newHarness(t)
	items := make([]protocol.WorkItem, 101)
	for i := range items {
		items[i] = observerItem(t, h)
	}
	observerHello(t, h, "linux-1", "first")
	assignments := pollDelivery(t, h, "linux-1", "first")
	if len(assignments) != 1 || !accepted(t, reportDelivery(t, h, "linux-1", "first", assignments[0], "merged", "")) {
		t.Fatal(assignments)
	}
	if n := h.count("events WHERE kind='work_item.delivery_changed'"); n != 100 {
		t.Fatal("report notification work was not bounded", n)
	}
	if next := pollDelivery(t, h, "linux-1", "first"); len(next) != 0 {
		t.Fatal("terminal fanout started another provider read", next)
	}
	if n := h.count("events WHERE kind='work_item.delivery_changed'"); n != 101 {
		t.Fatal("terminal fact stranded an unconsumed Issue", n)
	}
	h.clock.Advance(time.Hour)
	if next := pollDelivery(t, h, "linux-1", "first"); len(next) != 0 {
		t.Fatal("terminal read restarted", next)
	}
	if fact := normalizedDeliveryFact(t, h); fact.FactRevision != 1 {
		t.Fatal("notification fanout fabricated another provider result", fact)
	}
	assertSharedDeliveryNotices(t, h, items, 1)
}

func assertSharedDeliveryNotices(t *testing.T, h *harness, items []protocol.WorkItem, expected int) {
	t.Helper()
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		for _, item := range items {
			var notices int
			if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM message_notices n JOIN message_work_items m USING(message_id) WHERE n.notice_type='work_item_updated' AND m.work_item_id=?`, item.WorkItemID).Scan(&notices); err != nil {
				return err
			}
			if notices != expected {
				t.Errorf("transition notice lost or repeated for %s: got=%d want=%d", item.WorkItemID, notices, expected)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestServerCachedFanoutPreservesMergeAndRecoveryTransitionNotices(t *testing.T) {
	for _, recovery := range []bool{false, true} {
		t.Run(map[bool]string{false: "merge", true: "error-recovery"}[recovery], func(t *testing.T) {
			h := newHarness(t)
			h.app.serverDeliveryObservations = true
			items := make([]protocol.WorkItem, 101)
			for i := range items {
				items[i] = observerItem(t, h)
			}
			if recovery {
				q, err := h.app.ClaimServerDelivery(t.Context())
				if err != nil || q == nil {
					t.Fatal(q, err)
				}
				if err = h.app.RecordServerDelivery(t.Context(), q.ID, nil, "authentication_required"); err != nil {
					t.Fatal(err)
				}
				if q, err = h.app.ClaimServerDelivery(t.Context()); err != nil || q != nil {
					t.Fatal(q, err)
				}
				assertSharedDeliveryNotices(t, h, items, 1)
				serverRefreshDelivery(t, h, items[0])
			}
			q, err := h.app.ClaimServerDelivery(t.Context())
			if err != nil || q == nil {
				t.Fatal(q, err)
			}
			if err = h.app.RecordServerDelivery(t.Context(), q.ID, &protocol.DeliverySnapshot{State: "merged", HeadSHA: "merged-head"}, ""); err != nil {
				t.Fatal(err)
			}
			if q, err = h.app.ClaimServerDelivery(t.Context()); err != nil || q != nil {
				t.Fatal(q, err)
			}
			expected := 1
			if recovery {
				expected++
			}
			assertSharedDeliveryNotices(t, h, items, expected)
			before := h.count("message_notices WHERE notice_type='work_item_updated'")
			serverRefreshDelivery(t, h, items[0])
			q, err = h.app.ClaimServerDelivery(t.Context())
			if err != nil || q == nil {
				t.Fatal(q, err)
			}
			if err = h.app.RecordServerDelivery(t.Context(), q.ID, &protocol.DeliverySnapshot{State: "merged", HeadSHA: "merged-head"}, ""); err != nil {
				t.Fatal(err)
			}
			if q, err = h.app.ClaimServerDelivery(t.Context()); err != nil || q != nil {
				t.Fatal(q, err)
			}
			if after := h.count("message_notices WHERE notice_type='work_item_updated'"); after != before {
				t.Fatal("checked-only read reused an old transition as a new notice", before, after)
			}
		})
	}
}

func TestRunnerSharedLaterRefreshRetainsTransitionUntilFreshRead(t *testing.T) {
	h, first, second := runnerSharedDeliveryFixture(t)
	serverRefreshDelivery(t, h, second)
	if !accepted(t, reportDelivery(t, h, "linux-1", "first", first, "merged", "")) {
		t.Fatal("original result rejected")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		row, err := store.GetDeliveryObservation(t.Context(), tx.Q(), second.WorkItemID, "delivery-1")
		if err == nil && (!row.RefreshRequested || !row.Observation.RefreshPending || row.ObservedRevision >= row.ChangedRevision) {
			t.Errorf("later refresh consumed a suppressed transition: %+v", row)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	next := pollDelivery(t, h, "linux-1", "first")
	if len(next) != 1 || next[0].WorkItemID != second.WorkItemID {
		t.Fatal("unconsumed notification cursor prevented the actual later refresh", next)
	}
	if !accepted(t, reportDelivery(t, h, "linux-1", "first", next[0], "merged", "")) {
		t.Fatal("fresh result rejected")
	}
	items := []protocol.WorkItem{h.work(first.WorkItemID), second}
	assertSharedDeliveryNotices(t, h, items, 1)
	for range 2 {
		if next := pollDelivery(t, h, "linux-1", "first"); len(next) != 0 {
			t.Fatal("settled terminal fact caused another provider read", next)
		}
	}
	if fact := normalizedDeliveryFact(t, h); fact.FactRevision != 2 || fact.Observation.RefreshPending {
		t.Fatal("fresh read did not settle the original pending transition", fact)
	}
	assertSharedDeliveryNotices(t, h, items, 1)
}

func TestRunnerSharedCoveredRefreshSettlesWithoutAnotherProviderRead(t *testing.T) {
	h := newHarness(t)
	first, second := observerItem(t, h), observerItem(t, h)
	serverRefreshDelivery(t, h, first)
	serverRefreshDelivery(t, h, second)
	observerHello(t, h, "linux-1", "first")
	assignments := pollDelivery(t, h, "linux-1", "first")
	if len(assignments) != 1 || !accepted(t, reportDelivery(t, h, "linux-1", "first", assignments[0], "merged", "")) {
		t.Fatal(assignments)
	}
	items := []protocol.WorkItem{first, second}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		for _, item := range items {
			row, err := store.GetDeliveryObservation(t.Context(), tx.Q(), item.WorkItemID, "delivery-1")
			if err != nil {
				return err
			}
			var status string
			if err := tx.Q().QueryRowContext(t.Context(), `SELECT status FROM observation_jobs WHERE issue_id=? AND delivery_id='delivery-1'`, item.WorkItemID).Scan(&status); err != nil {
				return err
			}
			if row.RefreshRequested || row.Observation.RefreshPending || row.ObservedRevision != row.FactRevision || status != "finished" {
				t.Errorf("shared result left a covered request unsettled: row=%+v job=%s", row, status)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertSharedDeliveryNotices(t, h, items, 1)
	if next := pollDelivery(t, h, "linux-1", "first"); len(next) != 0 {
		t.Fatal("covered refresh re-read the provider", next)
	}
	h.clock.Advance(time.Hour)
	if next := pollDelivery(t, h, "linux-1", "first"); len(next) != 0 {
		t.Fatal("covered terminal request became pending again", next)
	}
	if fact := normalizedDeliveryFact(t, h); fact.FactRevision != 1 || fact.Observation.RefreshPending {
		t.Fatal("notification settlement changed provider facts", fact)
	}
	assertSharedDeliveryNotices(t, h, items, 1)
}

func TestIncrementalPendingFanoutDoesNotStarveOtherPR(t *testing.T) {
	h := newHarness(t)
	makeItem := func(group string, number int64, host string) protocol.WorkItem {
		item := protocol.WorkItem{WorkItemID: h.ids.New(protocol.PrefixWorkItem), Revision: 1, Status: "open", CreatedAt: h.clock.Now().UnixMilli(), UpdatedAt: h.clock.Now().UnixMilli(), IssuePlan: protocol.IssuePlan{Title: "observe", Project: DefaultProject, Intent: "track publication", Deliveries: []protocol.IssueDelivery{{DeliveryID: "delivery-1", Provider: "github", Repository: "example/project", Number: number, HostID: host}}}}
		if err := h.store.Write(t.Context(), func(tx *store.Tx) error { return tx.SaveIssue(t.Context(), item, 0, map[string]any{}) }); err != nil {
			t.Fatal(err)
		}
		return item
	}
	makeItem("a", 7, "linux-1")
	pending := makeItem("a", 7, "linux-2")
	for range 99 {
		makeItem("a", 7, "linux-2")
	}
	observerHello(t, h, "linux-1", "first")
	first := pollDelivery(t, h, "linux-1", "first")
	if len(first) != 1 {
		t.Fatal(first)
	}
	serverRefreshDelivery(t, h, pending)
	if !accepted(t, reportDelivery(t, h, "linux-1", "first", first[0], "merged", "")) {
		t.Fatal("first result rejected")
	}
	unrelated := make([]protocol.WorkItem, 101)
	for i := range unrelated {
		unrelated[i] = makeItem("z", 8, "")
	}
	next := pollDelivery(t, h, "linux-1", "first")
	if len(next) != 1 || next[0].Delivery.Number != 8 {
		t.Fatal(next)
	}
	if !accepted(t, reportDelivery(t, h, "linux-1", "first", next[0], "merged", "")) {
		t.Fatal("unrelated result rejected")
	}
	for range 3 {
		if next := pollDelivery(t, h, "linux-1", "first"); len(next) != 0 {
			t.Fatal("unexpected read", next)
		}
	}
	assertSharedDeliveryNotices(t, h, unrelated, 1)
}

func TestIncrementalClosedRefreshRetainsDeferredMergeNotice(t *testing.T) {
	h := newHarness(t)
	closed := observerItem(t, h)
	closure := protocol.CloseWorkItemPayload{Closure: protocol.IssueClosure{Kind: "declined", Reason: "Owner ended responsibility", Evidence: []protocol.IssueEvidence{h.ownerEvidence()}}}
	if op := h.exec(owner, h.env("work_items.close", workTarget(closed.WorkItemID), closure, rev(closed.Revision)), CloseIssue{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	closed = h.work(closed.WorkItemID)
	serverRefreshDelivery(t, h, closed)
	observerHello(t, h, "linux-1", "first")
	first := pollDelivery(t, h, "linux-1", "first")
	if len(first) != 1 || first[0].WorkItemID != closed.WorkItemID {
		t.Fatal(first)
	}
	later := observerItem(t, h)
	serverRefreshDelivery(t, h, later)
	if !accepted(t, reportDelivery(t, h, "linux-1", "first", first[0], "merged", "")) {
		t.Fatal("original result rejected")
	}
	next := pollDelivery(t, h, "linux-1", "first")
	if len(next) != 1 || next[0].WorkItemID != later.WorkItemID {
		t.Fatal(next)
	}
	if !accepted(t, reportDelivery(t, h, "linux-1", "first", next[0], "merged", "")) {
		t.Fatal("fresh result rejected")
	}
	for range 2 {
		if next := pollDelivery(t, h, "linux-1", "first"); len(next) != 0 {
			t.Fatal(next)
		}
	}
	assertSharedDeliveryNotices(t, h, []protocol.WorkItem{closed}, 2)
}

func closeObserverItem(t *testing.T, h *harness, item protocol.WorkItem) protocol.WorkItem {
	t.Helper()
	closure := protocol.CloseWorkItemPayload{Closure: protocol.IssueClosure{Kind: "declined", Reason: "Owner ended responsibility", Evidence: []protocol.IssueEvidence{h.ownerEvidence()}}}
	if op := h.exec(owner, h.env("work_items.close", workTarget(item.WorkItemID), closure, rev(item.Revision)), CloseIssue{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	return h.work(item.WorkItemID)
}

func TestRunnerClosedCoveredRefreshRetainsDeferredNotification(t *testing.T) {
	h := newHarness(t)
	first := closeObserverItem(t, h, observerItem(t, h))
	second := closeObserverItem(t, h, observerItem(t, h))
	serverRefreshDelivery(t, h, first)
	serverRefreshDelivery(t, h, second)
	observerHello(t, h, "linux-1", "first")
	assignments := pollDelivery(t, h, "linux-1", "first")
	if len(assignments) != 1 {
		t.Fatal(assignments)
	}
	later := observerItem(t, h)
	serverRefreshDelivery(t, h, later)
	if !accepted(t, reportDelivery(t, h, "linux-1", "first", assignments[0], "merged", "")) {
		t.Fatal("original result rejected")
	}
	next := pollDelivery(t, h, "linux-1", "first")
	if len(next) != 1 || next[0].WorkItemID != later.WorkItemID {
		t.Fatal("covered closed request was physically repeated", next)
	}
	if !accepted(t, reportDelivery(t, h, "linux-1", "first", next[0], "merged", "")) {
		t.Fatal("later result rejected")
	}
	for range 2 {
		if next := pollDelivery(t, h, "linux-1", "first"); len(next) != 0 {
			t.Fatal("deferred closed notice restarted a provider read", next)
		}
	}
	assertSharedDeliveryNotices(t, h, []protocol.WorkItem{first, second}, 2)
	assertSharedDeliveryNotices(t, h, []protocol.WorkItem{later}, 1)
}

func TestServerConsumesRunnerDeferredClosedNotificationWithoutProviderRead(t *testing.T) {
	h := newHarness(t)
	// These active references fill the server's ordinary 100-member read batch
	// together with the later refresh, leaving the closed recipient outside it.
	for range 99 {
		item := observerItem(t, h)
		if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
			row, err := store.GetDeliveryObservation(t.Context(), tx.Q(), item.WorkItemID, "delivery-1")
			if err != nil {
				return err
			}
			row.NextCheckAt = h.clock.Now().Add(time.Hour).UnixMilli()
			return tx.PutDeliveryObservationQuery(t.Context(), row)
		}); err != nil {
			t.Fatal(err)
		}
	}
	closed := closeObserverItem(t, h, observerItem(t, h))
	serverRefreshDelivery(t, h, closed)
	observerHello(t, h, "linux-1", "first")
	assignments := pollDelivery(t, h, "linux-1", "first")
	if len(assignments) != 1 || assignments[0].WorkItemID != closed.WorkItemID {
		t.Fatal(assignments)
	}
	later := observerItem(t, h)
	serverRefreshDelivery(t, h, later)
	if !accepted(t, reportDelivery(t, h, "linux-1", "first", assignments[0], "merged", "")) {
		t.Fatal("original runner result rejected")
	}
	h.app.serverDeliveryObservations = true
	q, err := h.app.ClaimServerDelivery(t.Context())
	if err != nil || q == nil {
		t.Fatal("later refresh did not reach the server observer", q, err)
	}
	if err := h.app.RecordServerDelivery(t.Context(), q.ID, &protocol.DeliverySnapshot{State: "merged", HeadSHA: "head"}, ""); err != nil {
		t.Fatal(err)
	}
	assertSharedDeliveryNotices(t, h, []protocol.WorkItem{closed}, 1)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var next int64
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT next_at FROM server_delivery_jobs WHERE provider='github' AND repository='example/project' AND number=7`).Scan(&next); err != nil {
			return err
		}
		if next != int64(9223372036854775807) {
			t.Errorf("fixture did not exercise an otherwise terminal server schedule: %d", next)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if q, err = h.app.ClaimServerDelivery(t.Context()); err != nil || q != nil {
		t.Fatal("deferred notification required another provider read", q, err)
	}
	assertSharedDeliveryNotices(t, h, []protocol.WorkItem{closed}, 2)
	if fact := normalizedDeliveryFact(t, h); fact.FactRevision != 2 || fact.Observation.RefreshPending {
		t.Fatal("notification changed provider facts or started another query", fact)
	}
	if n := h.count("server_delivery_queries"); n != 1 {
		t.Fatal("deferred notice created another provider query record", n)
	}
	// A later independent provider failure cannot notify this closed historical
	// reference after its one explicit notification responsibility is consumed.
	serverRefreshDelivery(t, h, later)
	q, err = h.app.ClaimServerDelivery(t.Context())
	if err != nil || q == nil {
		t.Fatal(q, err)
	}
	if err := h.app.RecordServerDelivery(t.Context(), q.ID, nil, "authentication_required"); err != nil {
		t.Fatal(err)
	}
	assertSharedDeliveryNotices(t, h, []protocol.WorkItem{closed}, 2)
}

func TestNotificationMetadataOnlyProgressCommitsWithoutQuery(t *testing.T) {
	for _, server := range []bool{false, true} {
		t.Run(map[bool]string{false: "runner", true: "server"}[server], func(t *testing.T) {
			h := newHarness(t)
			h.app.serverDeliveryObservations = server
			item := closeObserverItem(t, h, observerItem(t, h))
			serverRefreshDelivery(t, h, item)
			if server {
				q, err := h.app.ClaimServerDelivery(t.Context())
				if err != nil || q == nil {
					t.Fatal(q, err)
				}
				if err := h.app.RecordServerDelivery(t.Context(), q.ID, &protocol.DeliverySnapshot{State: "merged", HeadSHA: "head"}, ""); err != nil {
					t.Fatal(err)
				}
			} else {
				observerHello(t, h, "linux-1", "first")
				assignments := pollDelivery(t, h, "linux-1", "first")
				if len(assignments) != 1 || !accepted(t, reportDelivery(t, h, "linux-1", "first", assignments[0], "merged", "")) {
					t.Fatal(assignments)
				}
			}
			// Recover an already-consumed responsibility flag. The next real
			// Execute has metadata work, but no notice or native query to emit.
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				row, err := store.GetDeliveryObservation(t.Context(), tx.Q(), item.WorkItemID, "delivery-1")
				if err != nil {
					return err
				}
				row.NotificationPending = true
				return tx.PutDeliveryObservationQuery(t.Context(), row)
			}); err != nil {
				t.Fatal(err)
			}
			before := h.count("events WHERE kind IN ('work_item.delivery_changed','work_item.delivery_checked')")
			if server {
				if q, err := h.app.ClaimServerDelivery(t.Context()); err != nil || q != nil {
					t.Fatal(q, err)
				}
			} else if assignments := pollDelivery(t, h, "linux-1", "first"); len(assignments) != 0 {
				t.Fatal(assignments)
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				row, err := store.GetDeliveryObservation(t.Context(), tx.Q(), item.WorkItemID, "delivery-1")
				if err == nil && (row.NotificationPending || row.FactRevision != 1 || row.Observation.RefreshPending) {
					t.Errorf("metadata-only Execute did not persist its actual progress: %+v", row)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if n := h.count("events WHERE kind IN ('work_item.delivery_changed','work_item.delivery_checked')"); n != before {
				t.Fatal("metadata cleanup manufactured a provider event", before, n)
			}
			assertSharedDeliveryNotices(t, h, []protocol.WorkItem{item}, 2)
		})
	}
}
