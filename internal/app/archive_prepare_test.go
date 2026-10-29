package app

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestArchivePrepareRetainsClaimsFactsAndQueuedWorkAcrossRestart(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	r := testRuntime("original")
	h.versionedHello("linux-1", r)
	h.createTask(owner, createTaskPayload{Title: "allocated", Spec: testSpec})
	env := h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{Worker: h.worker("linux-1"), WorkspaceProtocol: 1, Runtime: r}, nil)
	first, ok := h.claimWith("linux-1", env)
	if !ok {
		t.Fatal("no first allocation")
	}
	h.createTask(owner, createTaskPayload{Title: "retained pending", Spec: testSpec})
	request := protocol.ArchiveControlRequest{RequestID: h.ids.New(protocol.PrefixRequest)}
	p, err := h.app.ChangeArchivePreparation(ctx, request, true)
	if err != nil || !p.Prepared || p.Revision != 1 {
		t.Fatal(p, err)
	}
	second, ok := h.claimWith("linux-1", env)
	if !ok || second.RunID != first.RunID {
		t.Fatal("original claim receipt changed")
	}
	next := h.env(protocol.OpRunnerClaim, env.Target, protocol.ClaimPayload{Worker: h.worker("linux-1"), WorkspaceProtocol: 1, Runtime: r}, nil)
	if _, ok := h.claimWith("linux-1", next); ok {
		t.Fatal("new claim passed global preparation before host maintenance")
	}
	r.Sequence++
	beat, err := h.app.RuntimeHeartbeat(ctx, runner("linux-1"), protocol.HeartbeatPayload{Runtime: r, Runs: []protocol.LeaseRef{{RunID: first.RunID, LeaseID: first.LeaseID}}})
	if err != nil || !beat.Runs[0].Renewed {
		t.Fatal(beat, err)
	}
	restarted := New(h.store, h.ids, h.clock.Now)
	if _, err := restarted.BeginOwnerMutation(ctx); protocol.CodeOf(err) != protocol.CodeInvalidTransition {
		t.Fatal("restart reopened writes", err)
	}
	if _, err := restarted.ChangeArchivePreparation(ctx, protocol.ArchiveControlRequest{RequestID: h.ids.New(protocol.PrefixRequest)}, false); protocol.CodeOf(err) != protocol.CodeStaleRevision {
		t.Fatal("stale cancel", err)
	}
	op := h.exec(owner, h.env("hosts.maintenance", protocol.Target{Type: "host", ID: "linux-1"}, map[string]string{"mode": protocol.RunnerMaintenance}, rev(0)), HostMaintenance{})
	if op.Error != nil {
		t.Fatal(op)
	}
	cancelled, err := restarted.ChangeArchivePreparation(ctx, protocol.ArchiveControlRequest{RequestID: h.ids.New(protocol.PrefixRequest), ExpectedControlRevision: 1}, false)
	if err != nil || cancelled.Prepared {
		t.Fatal(cancelled, err)
	}
	replay, err := restarted.ChangeArchivePreparation(ctx, request, true)
	if err != nil || replay != p {
		t.Fatal("prepare did not replay original receipt", replay, err)
	}
	current, _ := restarted.ArchivePreparation(ctx)
	if current.Prepared {
		t.Fatal("replay reapplied old prepare")
	}
	if h.count("tasks") != 2 || h.count("runs") != 1 {
		t.Fatal("preparation changed business work")
	}
	if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
		c, err := store.GetHostControl(ctx, tx.Q(), "linux-1")
		if c.Mode != protocol.RunnerMaintenance {
			t.Fatal("cancel restored host without authorization")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestArchiveStatusRequiresFreshMaintenanceAndRetainsUnknown(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	r := testRuntime("current")
	h.versionedHello("linux-1", r)
	_, err := h.app.ChangeArchivePreparation(ctx, protocol.ArchiveControlRequest{RequestID: h.ids.New(protocol.PrefixRequest)}, true)
	if err != nil {
		t.Fatal(err)
	}
	v, err := h.app.ArchiveOnlineStatus(ctx)
	if err != nil || len(v.Hosts) != 1 || len(v.Hosts[0].Blockers) == 0 {
		t.Fatal(v, err)
	}
	h.exec(owner, h.env("hosts.maintenance", protocol.Target{Type: "host", ID: "linux-1"}, map[string]string{"mode": protocol.RunnerMaintenance}, rev(0)), HostMaintenance{})
	r.Sequence++
	r.Mode = protocol.RunnerReady
	r.AppliedControlRevision = 1
	if _, err := h.app.RuntimeHeartbeat(ctx, runner("linux-1"), protocol.HeartbeatPayload{Runtime: r}); err != nil {
		t.Fatal(err)
	}
	v, err = h.app.ArchiveOnlineStatus(ctx)
	if err != nil || len(v.Hosts[0].Blockers) != 0 || !v.Hosts[0].Fresh {
		t.Fatal(v, err)
	}
	// A persisted unknown external delivery blocks even with an empty run set.
	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		if err := tx.InsertOutbox(ctx, store.OutboxRow{OutboxID: "unknown", RequestID: "unknown", Destination: MainThreadDestination, Operation: OpHandleMessage, PayloadJSON: `{}`}); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE outbox SET status='unknown' WHERE outbox_id='unknown'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	v, err = h.app.ArchiveOnlineStatus(ctx)
	if err != nil || len(v.Blockers) == 0 {
		t.Fatal("unknown hidden", v, err)
	}
	h.clock.Advance(61 * time.Second)
	v, err = h.app.ArchiveOnlineStatus(ctx)
	if err != nil || v.Hosts[0].Fresh {
		t.Fatal("stale accepted", v, err)
	}
}

func TestArchiveGateCountsAdmittedOwnerRequestWithoutHoldingUploadLock(t *testing.T) {
	h := newHarness(t)
	done, err := h.app.BeginOwnerMutation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.app.ChangeArchivePreparation(t.Context(), protocol.ArchiveControlRequest{RequestID: h.ids.New(protocol.PrefixRequest)}, true)
	if err != nil {
		t.Fatal(err)
	}
	v, err := h.app.ArchiveOnlineStatus(t.Context())
	if err != nil || v.ActiveOwnerRequests != 1 {
		t.Fatal(v, err)
	}
	done()
	v, err = h.app.ArchiveOnlineStatus(context.Background())
	if err != nil || v.ActiveOwnerRequests != 0 {
		t.Fatal(v, err)
	}
}

func TestArchivePrepareLeavesPendingMessagesAndDueFollowupsUnexecuted(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	rule := h.followup(w, protocol.FollowupSpec{Kind: "at", At: h.clock.Now().Add(time.Minute).UnixMilli()})
	h.clock.Advance(2 * time.Minute)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		return tx.InsertOutbox(t.Context(), store.OutboxRow{OutboxID: "pending-owner", RequestID: "pending-owner", Destination: MainThreadDestination, Operation: OpHandleMessage, PayloadJSON: `{}`})
	}); err != nil {
		t.Fatal(err)
	}
	p, err := h.app.ChangeArchivePreparation(t.Context(), protocol.ArchiveControlRequest{RequestID: h.ids.New(protocol.PrefixRequest)}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination); err != nil || ok {
		t.Fatal("new hosted delivery sent during preparation", ok, err)
	}
	h.sweepFollowups()
	if len(h.occurrences(rule.RuleID)) != 0 || h.readFollowup(rule.RuleID).Status != "active" {
		t.Fatal("followup was executed or cancelled")
	}
	if _, err := h.app.ChangeArchivePreparation(t.Context(), protocol.ArchiveControlRequest{RequestID: h.ids.New(protocol.PrefixRequest), ExpectedControlRevision: p.Revision}, false); err != nil {
		t.Fatal(err)
	}
	h.sweepFollowups()
	if len(h.occurrences(rule.RuleID)) != 1 {
		t.Fatal("explicit cancel did not resume existing timer")
	}
}

func TestArchivePreparationConcurrentCASHasOneAcceptedResult(t *testing.T) {
	h := newHarness(t)
	requests := []protocol.ArchiveControlRequest{{RequestID: h.ids.New(protocol.PrefixRequest)}, {RequestID: h.ids.New(protocol.PrefixRequest)}}
	results := make(chan error, 2)
	start := make(chan struct{})
	for _, request := range requests {
		go func() { <-start; _, err := h.app.ChangeArchivePreparation(t.Context(), request, true); results <- err }()
	}
	close(start)
	accepted, stale := 0, 0
	for range requests {
		err := <-results
		if err == nil {
			accepted++
		} else if protocol.CodeOf(err) == protocol.CodeStaleRevision {
			stale++
		} else {
			t.Fatal(err)
		}
	}
	if accepted != 1 || stale != 1 {
		t.Fatal(accepted, stale)
	}
	current, err := h.app.ArchivePreparation(t.Context())
	if err != nil || current.Revision != 1 || !current.Prepared {
		t.Fatal(current, err)
	}
}

func TestArchiveStatusBlocksAdmittedNativeControlButNotPendingInput(t *testing.T) {
	h := newHarness(t)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		return tx.InsertOutbox(t.Context(), store.OutboxRow{OutboxID: "retained-input", RequestID: "retained-input", Destination: MainThreadDestination, Operation: OpHandleMessage, PayloadJSON: `{}`})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.ChangeArchivePreparation(t.Context(), protocol.ArchiveControlRequest{RequestID: h.ids.New(protocol.PrefixRequest)}, true); err != nil {
		t.Fatal(err)
	}
	before, err := h.app.ArchiveOnlineStatus(t.Context())
	if err != nil || len(before.Blockers) != 0 {
		t.Fatal(before, err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		return tx.InsertOutbox(t.Context(), store.OutboxRow{OutboxID: "admitted-stop", RequestID: "admitted-stop", Destination: MainThreadControlDestination, Operation: OpInterruptTurn, PayloadJSON: `{"message_id":"old-turn-input"}`})
	}); err != nil {
		t.Fatal(err)
	}
	after, err := h.app.ArchiveOnlineStatus(t.Context())
	if err != nil || len(after.Blockers) == 0 {
		t.Fatal("already admitted native control was hidden among ordinary pending work", after, err)
	}
}

func TestArchivePreparationPersistsAcrossActualStoreCloseAndReopen(t *testing.T) {
	clock := testkit.NewClock()
	ids := testkit.NewIDGen(clock, 779)
	path := filepath.Join(t.TempDir(), "server.db")
	first, err := store.Open(t.Context(), path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	a := New(first, ids, clock.Now)
	if _, err := a.ChangeArchivePreparation(t.Context(), protocol.ArchiveControlRequest{RequestID: ids.New(protocol.PrefixRequest)}, true); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := store.Open(t.Context(), path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	restarted := New(second, ids, clock.Now)
	p, err := restarted.ArchivePreparation(t.Context())
	if err != nil || !p.Prepared || p.Revision != 1 {
		t.Fatal("restart lost preparation", p, err)
	}
	if _, err := restarted.BeginOwnerMutation(t.Context()); protocol.CodeOf(err) != protocol.CodeInvalidTransition {
		t.Fatal("restart reopened owner admission", err)
	}
}

func TestArchiveStatusRetainsKnownJournalUnknownAfterHostRetirement(t *testing.T) {
	h := newHarness(t)
	r := testRuntime("retired-with-effects")
	r.PendingOperations = 1
	if op := h.versionedHello("linux-1", r); op.Error != nil {
		t.Fatal(op)
	}
	if _, err := h.app.ChangeArchivePreparation(t.Context(), protocol.ArchiveControlRequest{RequestID: h.ids.New(protocol.PrefixRequest)}, true); err != nil {
		t.Fatal(err)
	}
	// Normal owner configuration retirement is not a journal reconciliation.
	h.syncConfig(ConfigPayload{})
	v, err := h.app.ArchiveOnlineStatus(t.Context())
	if err != nil || len(v.Blockers) == 0 {
		t.Fatal("retirement hid a known runner effect", v, err)
	}
}

func TestArchivePrepareRetainsResultWithoutEventFollowupUntilCancel(t *testing.T) {
	h := newHarness(t)
	taskID, accept := h.toAwaitingAcceptance()
	plan := workPlan()
	plan.Stages[0].TaskIDs = []string{taskID}
	w := h.createWork(plan)
	rule := h.followup(w, protocol.FollowupSpec{Kind: "condition", Condition: &protocol.FollowupCondition{Kind: "tasks_completed", TaskIDs: []string{taskID}}})
	before := h.readFollowup(rule.RuleID)
	// Retain an already-fired, now-expired occurrence with its actual unknown
	// delivery identity. Cancel must not use it as a retry channel.
	original := h.followup(w, protocol.FollowupSpec{Kind: "at", At: h.clock.Now().UnixMilli(), ExpiresAt: h.clock.Now().Add(time.Second).UnixMilli()})
	originalOccurrences := h.occurrences(original.RuleID)
	if len(originalOccurrences) != 1 || originalOccurrences[0].Status != "unknown" {
		t.Fatal("expected the existing unconfirmed delivery", originalOccurrences)
	}
	h.clock.Advance(2 * time.Second)
	prepared, err := h.app.ChangeArchivePreparation(t.Context(), protocol.ArchiveControlRequest{RequestID: h.ids.New(protocol.PrefixRequest)}, true)
	if err != nil {
		t.Fatal(err)
	}
	if op := h.submitVerdict(accept, protocol.VerdictPassed); op.Status != protocol.OpSucceeded {
		t.Fatal("original acceptance result was refused", op)
	}
	if got := h.task(taskID); got.Lifecycle != protocol.TaskCompleted {
		t.Fatal("original completion fact was not retained", got.Lifecycle)
	}
	if got := h.occurrences(rule.RuleID); len(got) != 0 {
		t.Errorf("result event admitted a new occurrence during prepare: %+v", got)
	}
	if got := h.readFollowup(rule.RuleID); !reflect.DeepEqual(got, before) {
		t.Errorf("result event advanced the prepared rule: before=%+v after=%+v", before, got)
	}
	if _, err := h.app.ChangeArchivePreparation(t.Context(), protocol.ArchiveControlRequest{RequestID: h.ids.New(protocol.PrefixRequest), ExpectedControlRevision: prepared.Revision}, false); err != nil {
		t.Fatal(err)
	}
	h.sweepFollowups()
	h.sweepFollowups()
	if got := h.occurrences(rule.RuleID); len(got) != 1 {
		t.Fatal("cancel did not recheck the retained result exactly once", got)
	}
	if got := h.occurrences(original.RuleID); !reflect.DeepEqual(got, originalOccurrences) {
		t.Fatal("cancel replayed or replaced the original expired unknown occurrence", got)
	}
}

func TestArchiveCancelRechecksUnsatisfiedConditionOnce(t *testing.T) {
	h := newHarness(t)
	taskID, _ := h.toAwaitingAcceptance()
	plan := workPlan()
	plan.Stages[0].TaskIDs = []string{taskID}
	w := h.createWork(plan)
	rule := h.followup(w, protocol.FollowupSpec{Kind: "condition", Condition: &protocol.FollowupCondition{Kind: "tasks_completed", TaskIDs: []string{taskID}}})
	before := h.readFollowup(rule.RuleID)
	prepared, err := h.app.ChangeArchivePreparation(t.Context(), protocol.ArchiveControlRequest{RequestID: h.ids.New(protocol.PrefixRequest)}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.ChangeArchivePreparation(t.Context(), protocol.ArchiveControlRequest{RequestID: h.ids.New(protocol.PrefixRequest), ExpectedControlRevision: prepared.Revision}, false); err != nil {
		t.Fatal(err)
	}
	h.sweepFollowups()
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var due int64
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT next_due FROM followup_rules WHERE rule_id=?`, rule.RuleID).Scan(&due); err != nil {
			return err
		}
		if due != before.ExpiresAt || due <= h.clock.Now().UnixMilli() {
			t.Errorf("unsatisfied condition remains hot after recheck: due=%d expiry=%d", due, before.ExpiresAt)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	h.sweepFollowups()
	if got := h.readFollowup(rule.RuleID); !reflect.DeepEqual(got, before) || len(h.occurrences(rule.RuleID)) != 0 {
		t.Fatal("cancel changed the unsatisfied public rule or created an occurrence", got)
	}
}
