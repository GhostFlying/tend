package mainthread

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/dispatch"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestDistinctManualHandoffRefusesUnknownIntentAndUnblocksOwnerChat(t *testing.T) {
	f := newHandoffFixture(t)
	ctx := context.Background()
	hash, err := currentToolsetHash()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.e.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE main_thread_bindings SET toolset_hash=? WHERE instance='main' AND thread_id=?`, hash, f.source.ID())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	f.saveSummary(t)
	f.e.h.Turn = func(th *testkit.FakeThread, id string, _ harness.TurnInput) string {
		th.Say(id, "reply", "Normal owner chat continues after rejected maintenance")
		return "completed"
	}
	f.e.m.cfg.Open = func(context.Context) (harness.Session, error) { return f.session, nil }
	f.e.m.cfg.MaintenanceInterval = time.Hour
	f.e.signal = dispatch.NewSignal()
	f.e.start()
	defer f.e.stop()
	ready, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := f.e.m.waitThread(ready); err != nil {
		t.Fatal(err)
	}
	f.session.mu.Lock()
	f.session.startErr = errors.New("create response lost")
	f.session.mu.Unlock()
	queue := func() {
		f.e.exec(owner, "conversation.handoff", protocol.Target{Type: "conversation"}, protocol.ConversationHandoffPayload{}, app.ConversationHandoff{})
	}
	latestRequest := func() string {
		var id string
		if err := f.e.store.Read(ctx, func(tx *store.ReadTx) error {
			return tx.Q().QueryRowContext(ctx, `SELECT request_id FROM outbox WHERE operation='conversation.handoff' ORDER BY seq DESC LIMIT 1`).Scan(&id)
		}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	queue()
	first := latestRequest()
	eventually(t, "first maintenance outcome becomes unknown", func() bool {
		return f.e.count(`SELECT COUNT(*) FROM outbox WHERE request_id=? AND status='unknown'`, first) == 1
	})
	old, exists, err := f.e.app.GetHandoff(ctx, first)
	if err != nil || !exists || old.Phase != "unknown" {
		t.Fatalf("first unknown intent %+v %v", old, err)
	}
	before, _, _ := f.session.counts()
	// Hold the delivery boundary so reconciliation is exercised before this
	// distinct request can enter Deliver, as after a dispatch crash/restart.
	f.e.m.workMu.Lock()
	locked := true
	defer func() {
		if locked {
			f.e.m.workMu.Unlock()
		}
	}()
	queue()
	second := latestRequest()
	if second == first {
		t.Fatal("owner requests unexpectedly share an ID")
	}
	reconciled := f.e.m.reconcileHandoff(ctx, app.Delivery{RequestID: second, Operation: "conversation.handoff", Payload: []byte(`{}`)})
	if reconciled.Outcome != app.Failed {
		t.Fatalf("new request reconciliation=%+v; must fail rather than alias/retry old unknown intent", reconciled)
	}
	message := f.e.say("Continue ordinary conversation after the blocked handoff")
	f.e.m.workMu.Unlock()
	locked = false
	eventually(t, "second handoff fails without blocking destination", func() bool {
		return f.e.count(`SELECT COUNT(*) FROM outbox WHERE request_id=? AND status='failed'`, second) == 1
	})
	eventually(t, "following owner chat completes", func() bool {
		return f.e.count(`SELECT COUNT(*) FROM main_turns WHERE request_id NOT IN (SELECT request_id FROM logical_turn_executions WHERE repair=1) AND message_id=? AND status='completed'`, message) == 1
	})
	after, _, _ := f.session.counts()
	if before != after {
		t.Fatal("distinct request sent another native create")
	}
	preserved, exists, err := f.e.app.GetHandoff(ctx, first)
	if err != nil || !exists || preserved.Phase != "unknown" || preserved.TargetThreadID != old.TargetThreadID {
		t.Fatalf("old unknown intent rewritten %+v %v", preserved, err)
	}
	if f.e.count(`SELECT COUNT(*) FROM main_thread_handoffs`) != 1 || f.e.count(`SELECT COUNT(*) FROM outbox WHERE request_id=? AND status='unknown'`, first) != 1 {
		t.Fatal("distinct manual request replaced/retried the unknown intent")
	}
	if len(businessStarts(f.e.h)) != 1 {
		t.Fatal("maintenance request started an owner turn or replayed chat")
	}
}

func TestHistoricalUnknownDoesNotBlockFreshSourceManualOrIdleSummary(t *testing.T) {
	f := newHandoffFixture(t)
	ctx := context.Background()
	const body = "HISTORICAL_UNKNOWN_OWNER_BODY: action outcome remains unknown"
	message := f.e.say(body)
	delivery, _, ok, err := f.e.app.NextDelivery(ctx, app.MainThreadDestination)
	if err != nil || !ok {
		t.Fatalf("claim owner delivery: %v %v", ok, err)
	}
	if err := f.e.app.PutMainTurn(ctx, store.MainTurn{RequestID: delivery.RequestID, MessageID: message, ThreadID: "legacy-ancestor", Status: store.TurnStarting}); err != nil {
		t.Fatal(err)
	}
	if err := f.e.app.FinishDelivery(ctx, delivery, app.DeliveryResult{Outcome: app.Unknown, Reason: "start response lost; old native session closed"}); err != nil {
		t.Fatal(err)
	}
	inputs := make(chan string, 4)
	f.session.summaryTurn = func(th *testkit.FakeThread, id string, in harness.TurnInput) string {
		inputs <- in.Text
		th.Say(id, "summary", body+". Preserve this uncertainty and never repeat the action.")
		return "completed"
	}
	target, err := f.e.m.openManagedThread(ctx, f.session)
	if err != nil {
		t.Fatal(err)
	}
	assertCurrentRecoveredThread(t, f, target)
	select {
	case originalInput := <-inputs:
		if !strings.Contains(originalInput, body) || !strings.Contains(originalInput, `"state":"unknown"`) || !strings.Contains(originalInput, `"unconfirmed_owner_input":true`) {
			t.Fatal("actual unknown message state/body omitted from first complete summary input")
		}
	default:
		t.Fatal("startup did not prepare observed unknown history")
	}
	for len(inputs) > 0 {
		<-inputs
	}
	f.e.m.mu.Lock()
	f.e.m.session = f.session
	f.e.m.thread = target
	f.e.m.mu.Unlock()
	manual := f.e.m.Deliver(ctx, app.Delivery{RequestID: "prepare-new-source", Operation: "conversation.handoff", Payload: []byte(`{"prepare_only":true}`)})
	if manual.Outcome != app.Delivered {
		t.Fatalf("historical unknown blocked manual summary: %+v", manual)
	}
	select {
	case text := <-inputs:
		if !strings.Contains(text, body) {
			t.Fatal("manual summary lost preserved unknown-reference facts")
		}
	default:
		t.Fatal("new native source did not receive private summary preparation")
	}
	if err := f.e.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: "new-server-observation", Author: "system", Body: "A later ledger observation", CreatedAt: f.clock.Now().UnixMilli()})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(50 * time.Minute)
	if err := f.e.m.idleSummary(ctx); err != nil {
		t.Fatalf("historical unknown blocked idle summary: %v", err)
	}
	select {
	case text := <-inputs:
		if !strings.Contains(text, body) {
			t.Fatal("idle summary lost unknown facts from previous full summary")
		}
	default:
		t.Fatal("historical unknown prevented idle summary from starting")
	}
	snapshot, err := f.e.app.HandoffSnapshot(ctx, "main")
	if err != nil || snapshot.Pending || snapshot.Active {
		t.Fatalf("historical unknown reported as live work %+v %v", snapshot, err)
	}
	summary, exists, err := f.e.app.HandoffSummary(ctx, "main")
	if err != nil || !exists || !summary.Snapshot.Covers(snapshot) {
		t.Fatalf("summary coverage not current %+v %v", summary, err)
	}
	recorded, exists, err := f.e.app.MainTurn(ctx, delivery.RequestID)
	if err != nil || !exists || recorded.Status != store.TurnStarting || recorded.ThreadID != "legacy-ancestor" {
		t.Fatalf("historical unknown turn changed %+v %v", recorded, err)
	}
	if f.e.count(`SELECT COUNT(*) FROM outbox WHERE request_id=? AND status='unknown' AND attempts=1`, delivery.RequestID) != 1 || len(businessStarts(f.e.h)) != 0 {
		t.Fatal("historical unknown delivery was reset or replayed")
	}
	full, err := f.e.app.HandoffSummaryInput(ctx, "main", 256<<10)
	if err != nil {
		t.Fatal(err)
	}
	if full.PreviousSummary == nil || !strings.Contains(full.PreviousSummary.Body, body) {
		t.Fatal("unknown outcome disappeared from immutable summary baseline")
	}
}

func TestConflictingMaintenanceFailsBeforeWaitingForNativeReadiness(t *testing.T) {
	for _, prepareOnly := range []bool{false, true} {
		name := "full"
		payload := []byte(`{}`)
		if prepareOnly {
			name = "prepare-only"
			payload = []byte(`{"prepare_only":true}`)
		}
		t.Run(name, func(t *testing.T) {
			f := newHandoffFixture(t)
			job, _ := seedPendingRecoveryHandoff(t, f, "unknown")
			f.e.m.mu.Lock()
			notReady := f.e.m.session == nil && f.e.m.thread == nil
			f.e.m.mu.Unlock()
			if !notReady {
				t.Fatal("fixture unexpectedly has a ready native thread")
			}
			before, forksBefore, _ := f.session.counts()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			result := f.e.m.Deliver(ctx, app.Delivery{RequestID: "different-owner-request", Operation: "conversation.handoff", Payload: payload})
			if result.Outcome != app.Failed || ctx.Err() != nil {
				t.Fatalf("conflicting maintenance waited for unavailable native thread: %+v ctx=%v", result, ctx.Err())
			}
			after, forksAfter, _ := f.session.counts()
			if after != before || forksAfter != forksBefore {
				t.Fatal("conflicting request sent a native create/fork")
			}
			saved, exists, err := f.e.app.GetHandoff(context.Background(), job.ID)
			if err != nil || !exists || saved.Phase != "unknown" {
				t.Fatalf("old unknown intent changed %+v %v", saved, err)
			}
			if f.e.count(`SELECT COUNT(*) FROM main_thread_handoffs`) != 1 {
				t.Fatal("conflicting request created another intent")
			}
		})
	}
}

func TestUnreadyMaintenanceRetriesWithoutWaitingAndThenRejectsEarlierIntent(t *testing.T) {
	for _, prepareOnly := range []bool{false, true} {
		name := "full"
		payload := []byte(`{}`)
		if prepareOnly {
			name = "prepare-only"
			payload = []byte(`{"prepare_only":true}`)
		}
		t.Run(name, func(t *testing.T) {
			f := newHandoffFixture(t)
			before, forksBefore, _ := f.session.counts()
			d := app.Delivery{RequestID: "queued-unready-maintenance", Operation: "conversation.handoff", Payload: payload}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			result := f.e.m.Deliver(ctx, d)
			if result.Outcome != app.Retry || ctx.Err() != nil {
				t.Fatalf("unready maintenance blocked instead of deferring to dispatcher: %+v ctx=%v", result, ctx.Err())
			}
			if f.e.count(`SELECT COUNT(*) FROM main_thread_handoffs`) != 0 {
				t.Fatal("unready request recorded a native intent before readiness")
			}
			job, _ := seedPendingRecoveryHandoff(t, f, "unknown")
			retried := f.e.m.Deliver(ctx, d)
			if retried.Outcome != app.Failed || ctx.Err() != nil {
				t.Fatalf("earlier unknown intent did not terminate readiness retry: %+v ctx=%v", retried, ctx.Err())
			}
			after, forksAfter, _ := f.session.counts()
			if before != after || forksBefore != forksAfter {
				t.Fatal("unready maintenance performed a native operation")
			}
			pending, exists, err := f.e.app.GetHandoff(context.Background(), job.ID)
			if err != nil || !exists || pending.Phase != "unknown" {
				t.Fatalf("earlier unknown intent changed %+v %v", pending, err)
			}
		})
	}
}
