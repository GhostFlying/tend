package mainthread

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func fenceSummaryUpgrade(t *testing.T, f *handoffFixture) {
	t.Helper()
	ctx := t.Context()
	row := store.UpgradeRow{View: protocol.UpgradeView{UpgradeID: "summary-upgrade", ReleaseID: "release", Revision: 1, Status: "running", Phase: "server_ready"}}
	if err := f.e.store.Write(ctx, func(tx *store.Tx) error { return tx.PutUpgrade(ctx, row) }); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareOnlyFenceRaceRetainsOriginalDelivery(t *testing.T) {
	f := newHandoffFixture(t)
	ctx := t.Context()
	seedIdleConversation(t, f)
	f.e.exec(owner, "conversation.handoff", protocol.Target{Type: "conversation"}, protocol.ConversationHandoffPayload{PrepareOnly: true}, app.ConversationHandoff{})
	original, err := f.e.app.ConversationMaintenance(ctx, "main")
	if err != nil || original == nil {
		t.Fatalf("original owner request missing: %+v err=%v", original, err)
	}
	delivery, _, ok, err := f.e.app.NextDelivery(ctx, app.MainThreadDestination)
	if err != nil || !ok {
		t.Fatalf("claim preparation delivery: ok=%t err=%v", ok, err)
	}
	if paused, err := f.e.app.UpgradePausesMain(ctx); err != nil || paused {
		t.Fatalf("initial delivery fence check: paused=%t err=%v", paused, err)
	}
	fenceSummaryUpgrade(t, f)
	// Exercise the preparation body after the delivery's earlier check passed.
	f.e.m.workMu.Lock()
	result := f.e.m.deliverPreparedSummary(ctx, f.session, f.source, delivery.RequestID)
	f.e.m.workMu.Unlock()
	if result.Outcome != app.Retry {
		t.Fatalf("pause became a terminal preparation result: %+v", result)
	}
	if err := f.e.app.FinishDelivery(ctx, delivery, result); err != nil {
		t.Fatal(err)
	}
	maintenance, err := f.e.app.ConversationMaintenance(ctx, "main")
	if err != nil || maintenance == nil || maintenance.RequestID != original.RequestID || maintenance.Status != "pending" {
		t.Fatalf("original preparation did not remain pending: %+v err=%v", maintenance, err)
	}
	_, forks, _ := f.session.counts()
	if forks != 0 || f.e.count(`SELECT COUNT(*) FROM conversation_summary_attempts`) != 0 {
		t.Fatal("paused preparation created a model attempt")
	}
	if err := f.e.store.Write(ctx, func(tx *store.Tx) error {
		row, err := store.GetUpgrade(ctx, tx.Q(), "summary-upgrade")
		if err != nil {
			return err
		}
		row.View.Status, row.View.Phase = "completed", "completed"
		return tx.PutUpgrade(ctx, row)
	}); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(result.RetryAfter)
	retried, _, ok, err := f.e.app.NextDelivery(ctx, app.MainThreadDestination)
	if err != nil || !ok || retried.RequestID != delivery.RequestID || retried.OutboxID != delivery.OutboxID {
		t.Fatalf("preparation changed request on retry: %+v ok=%t err=%v", retried, ok, err)
	}
	result = f.e.m.Deliver(ctx, retried)
	if result.Outcome != app.Delivered {
		t.Fatalf("original preparation did not recover: %+v", result)
	}
	if err := f.e.app.FinishDelivery(ctx, retried, result); err != nil {
		t.Fatal(err)
	}
	maintenance, err = f.e.app.ConversationMaintenance(ctx, "main")
	if err != nil || maintenance == nil || maintenance.RequestID != original.RequestID || maintenance.Status != "delivered" {
		t.Fatalf("original owner request did not finish: %+v err=%v", maintenance, err)
	}
	_, forks, _ = f.session.counts()
	if forks != 1 || f.e.count(`SELECT COUNT(*) FROM conversation_summary_attempts WHERE status='succeeded'`) != 1 {
		t.Fatal("known unstarted preparation did not run exactly once after release")
	}
}

func TestIdleSummaryFenceRaceSkipsWithoutFailure(t *testing.T) {
	f := newHandoffFixture(t)
	seedIdleConversation(t, f)
	f.clock.Advance(time.Hour)
	fenced := false
	// The idle clock is read after the fast fence check and before claim.
	f.e.m.cfg.Now = func() time.Time {
		fenceSummaryUpgrade(t, f)
		fenced = true
		return f.clock.Now()
	}
	if err := f.e.m.idleSummary(t.Context()); err != nil {
		t.Fatal("fence race became an idle summary error", err)
	}
	_, forks, _ := f.session.counts()
	if !fenced || forks != 0 || f.e.count(`SELECT COUNT(*) FROM conversation_summary_attempts`) != 0 {
		t.Fatalf("idle race admitted summary work: fenced=%t forks=%d", fenced, forks)
	}
	f.e.m.mu.Lock()
	active := f.e.m.summaryActive
	f.e.m.mu.Unlock()
	if active {
		t.Fatal("skipped summary retained an active operation")
	}
}

func TestAdmittedSummaryStillBlocksDrainUntilRealCompletion(t *testing.T) {
	f := newHandoffFixture(t)
	seedIdleConversation(t, f)
	f.clock.Advance(time.Hour)
	f.e.m.cfg.SummaryTimeout = 10 * time.Second
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	finished := make(chan error, 1)
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	f.session.summaryTurn = func(th *testkit.FakeThread, id string, _ harness.TurnInput) string {
		close(started)
		select {
		case <-release:
			th.Say(id, "summary", "Real admitted summary completed after the fence.")
			return "completed"
		case <-ctx.Done():
			return ""
		}
	}
	go func() {
		defer close(done)
		finished <- f.e.m.idleSummary(ctx)
	}()
	defer func() {
		cancel()
		unblock()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("admitted summary did not finish cleanup")
		}
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("summary did not start", ctx.Err())
	}
	fenceSummaryUpgrade(t, f)
	if drained, err := f.e.m.UpgradeDrainStatus(ctx); err != nil || drained {
		t.Fatalf("active summary was treated as drained: drained=%t err=%v", drained, err)
	}
	unblock()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal("fence rejected admitted summary completion", err)
		}
	case <-ctx.Done():
		t.Fatal("summary did not complete", ctx.Err())
	}
	if drained, err := f.e.m.UpgradeDrainStatus(ctx); err != nil || !drained {
		t.Fatalf("settled summary retained drain blocker: drained=%t err=%v", drained, err)
	}
	if f.e.count(`SELECT COUNT(*) FROM conversation_summary_attempts WHERE status='succeeded'`) != 1 || f.e.count(`SELECT COUNT(*) FROM conversation_summaries WHERE format_version=3`) != 1 {
		t.Fatal("admitted summary's real terminal facts were not saved")
	}
}

func TestSummaryInputReclaimInsideFenceDoesNotStartNewSnapshot(t *testing.T) {
	f := newHandoffFixture(t)
	ctx := t.Context()
	prior, err := f.e.app.HandoffSnapshot(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := f.e.app.ClaimSummaryAttempt(ctx, prior, time.Minute.Milliseconds()); err != nil || !claimed {
		t.Fatal(claimed, err)
	}
	seedIdleConversation(t, f)
	input, err := f.e.app.HandoffSnapshot(ctx, "main")
	if err != nil || prior.Covers(input) {
		t.Fatalf("input did not advance: %+v err=%v", input, err)
	}
	fenceSummaryUpgrade(t, f)
	if claimed, err := f.e.m.reclaimSummaryInput(ctx, prior, input); claimed || !errors.Is(err, app.ErrSummaryPaused) {
		t.Fatalf("new snapshot was admitted or lost pause reason: claimed=%t err=%v", claimed, err)
	}
	if f.e.count(`SELECT COUNT(*) FROM conversation_summary_attempts WHERE source_version=? AND status='failed'`, prior.Version) != 1 || f.e.count(`SELECT COUNT(*) FROM conversation_summary_attempts WHERE source_version=?`, input.Version) != 0 {
		t.Fatal("reclaim changed the wrong source attempt")
	}
	_, forks, _ := f.session.counts()
	if forks != 0 {
		t.Fatal("reclaim started a model call")
	}
}

func TestFencedStartupHandoffNeverResendsUnknownNativeRequest(t *testing.T) {
	for _, stage := range []string{"create", "inject"} {
		t.Run(stage, func(t *testing.T) {
			f := newHandoffFixture(t)
			fenceSummaryUpgrade(t, f)
			failure := errors.New("native result unconfirmed")
			if stage == "create" {
				f.session.startErr = failure
			} else {
				f.session.injectErr = failure
			}
			if _, err := f.e.m.openManagedThread(t.Context(), f.session); err == nil {
				t.Fatal("uncertain native request became a successful handoff")
			}
			before, _, _ := f.session.counts()
			if _, err := f.e.m.openManagedThread(t.Context(), f.session); err == nil {
				t.Fatal("uncertain handoff was silently retried")
			}
			after, forks, _ := f.session.counts()
			binding, _, err := f.e.app.MainThreadBinding(t.Context(), "main")
			if err != nil || binding.ThreadID != f.source.ID() || binding.Generation != 1 || before != after || forks != 0 {
				t.Fatalf("unknown request replayed or source changed: before=%d after=%d forks=%d binding=%+v err=%v", before, after, forks, binding, err)
			}
			job, exists, err := f.e.app.PendingHandoff(t.Context(), "main")
			if err != nil || !exists || job.Phase != "unknown" || job.SourceThreadID != f.source.ID() {
				t.Fatalf("unknown handoff facts changed: %+v err=%v", job, err)
			}
		})
	}
}

func TestStartupHandoffInsideFenceUsesFactsWithoutNewSummary(t *testing.T) {
	for _, cache := range []string{"missing", "stale", "current"} {
		t.Run(cache, func(t *testing.T) {
			f := newHandoffFixture(t)
			if cache == "stale" {
				f.saveSummary(t)
			}
			seedIdleConversation(t, f)
			if cache == "current" {
				f.saveSummary(t)
			}
			fenceSummaryUpgrade(t, f)
			target, err := f.e.m.openManagedThread(t.Context(), f.session)
			if err != nil {
				t.Fatal(err)
			}
			starts, forks, _ := f.session.counts()
			if starts != 2 || forks != 0 || f.e.count(`SELECT COUNT(*) FROM conversation_summary_attempts`) != 0 {
				t.Fatalf("upgrade startup admitted a model summary: starts=%d forks=%d", starts, forks)
			}
			injected := strings.Join(f.e.h.Injected(target.ID()), "\n")
			for _, want := range []string{f.source.ID(), "rollout:" + f.source.ID(), `"ledger_facts":`, `"ancestors":`} {
				if !strings.Contains(injected, want) {
					t.Errorf("handoff lost retained reference %q", want)
				}
			}
			if cache == "missing" {
				if !strings.Contains(injected, `"summary_available":false`) {
					t.Fatal("missing summary was not explicit")
				}
			} else {
				coverage := `"summary_covers_snapshot":false`
				if cache == "current" {
					coverage = `"summary_covers_snapshot":true`
				}
				if !strings.Contains(injected, "CACHED_SUMMARY_MARKER") || !strings.Contains(injected, coverage) {
					t.Fatal("cached summary coverage was lost or promoted")
				}
			}
			binding, _, err := f.e.app.MainThreadBinding(t.Context(), "main")
			if err != nil || binding.ThreadID != target.ID() || binding.Generation != 2 || len(binding.Ancestors) != 1 || binding.Ancestors[0].ThreadID != f.source.ID() {
				t.Fatalf("confirmed handoff lost original binding: %+v err=%v", binding, err)
			}
		})
	}
}
