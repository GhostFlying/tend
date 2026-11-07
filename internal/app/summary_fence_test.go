package app

import (
	"errors"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestSummaryClaimHonorsMainWorkFences(t *testing.T) {
	for _, kind := range []string{"upgrade", "park", "archive", "forward-startup", "maintenance-startup"} {
		t.Run(kind, func(t *testing.T) {
			h := newHarness(t)
			ctx := t.Context()
			if _, err := h.app.BindMainThread(ctx, "main", "source", "old-tools", "source.rollout"); err != nil {
				t.Fatal(err)
			}
			snap, err := h.app.HandoffSnapshot(ctx, "main")
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "archive":
				_, err = h.app.ChangeArchivePreparation(ctx, protocol.ArchiveControlRequest{RequestID: h.ids.New(protocol.PrefixRequest)}, true)
			case "forward-startup":
				WithForwardRecoveryStartup("pending-forward")(h.app)
			case "maintenance-startup":
				WithCommittedMaintenanceStartup("pending-maintenance")(h.app)
			default:
				row := store.UpgradeRow{View: protocol.UpgradeView{UpgradeID: "upgrade", ReleaseID: "release", Revision: 1, Status: "running", Phase: "server_ready"}}
				if kind == "park" {
					row.ParkIdleManagers = true
					row.View.Status, row.View.Phase = "attention", "runners"
				}
				err = h.store.Write(ctx, func(tx *store.Tx) error { return tx.PutUpgrade(ctx, row) })
			}
			if err != nil {
				t.Fatal(err)
			}
			if paused, err := h.app.UpgradePausesMain(ctx); err != nil || !paused {
				t.Fatalf("fixture did not fence main work: paused=%t err=%v", paused, err)
			}
			claimed, err := h.app.ClaimSummaryAttempt(ctx, snap, time.Minute.Milliseconds())
			if claimed || !errors.Is(err, ErrSummaryPaused) {
				t.Fatalf("fenced summary admitted or pause reason lost: claimed=%t err=%v", claimed, err)
			}
			if h.count("conversation_summary_attempts") != 0 || h.count("events WHERE kind='conversation.summary_started'") != 0 {
				t.Fatal("fenced claim created durable summary work")
			}
		})
	}
}

func TestSummaryAdmittedBeforeFenceRetainsRealCompletion(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	if _, err := h.app.BindMainThread(ctx, "main", "source", "old-tools", "source.rollout"); err != nil {
		t.Fatal(err)
	}
	snap, err := h.app.HandoffSnapshot(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := h.app.ClaimSummaryAttempt(ctx, snap, 0); err != nil || !claimed {
		t.Fatalf("initial claim=%t err=%v", claimed, err)
	}
	row := store.UpgradeRow{View: protocol.UpgradeView{UpgradeID: "upgrade", ReleaseID: "release", Revision: 1, Status: "running", Phase: "server_ready"}}
	if err := h.store.Write(ctx, func(tx *store.Tx) error { return tx.PutUpgrade(ctx, row) }); err != nil {
		t.Fatal(err)
	}
	if claimed, err := h.app.ClaimSummaryAttempt(ctx, snap, time.Minute.Milliseconds()); claimed || !errors.Is(err, ErrSummaryPaused) {
		t.Fatalf("fence allowed a repeated admission: claimed=%t err=%v", claimed, err)
	}
	if h.count("events WHERE kind='conversation.summary_started'") != 1 {
		t.Fatal("fenced repeat changed the admitted attempt")
	}
	snap.Summary = &store.SummaryCoverage{Complete: true, Method: "single", Batches: 1}
	if _, err := h.app.SaveHandoffSummary(ctx, store.HandoffSummary{Instance: "main", Snapshot: snap, Body: "Real completed summary", FormatVersion: 3}); err != nil {
		t.Fatal("fence rejected original summary result", err)
	}
	if err := h.app.FinishSummaryAttempt(ctx, snap, true); err != nil {
		t.Fatal("fence rejected original terminal receipt", err)
	}
	if h.count("conversation_summary_attempts WHERE status='succeeded'") != 1 || h.count("conversation_summaries") != 1 {
		t.Fatal("original summary facts were lost")
	}
}

func TestSummaryClaimWaitsForFenceWriterAndUsesCommittedState(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	if _, err := h.app.BindMainThread(ctx, "main", "source", "tools", "source.rollout"); err != nil {
		t.Fatal(err)
	}
	snap, err := h.app.HandoffSnapshot(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	fenceWritten, commitFence := make(chan struct{}), make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		writerDone <- h.store.Write(ctx, func(tx *store.Tx) error {
			err := tx.PutUpgrade(ctx, store.UpgradeRow{View: protocol.UpgradeView{UpgradeID: "upgrade", ReleaseID: "release", Revision: 1, Status: "running", Phase: "server_ready"}})
			close(fenceWritten)
			select {
			case <-commitFence:
				return err
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	<-fenceWritten
	claimDone := make(chan error, 1)
	go func() {
		claimed, err := h.app.ClaimSummaryAttempt(ctx, snap, 0)
		if claimed && err == nil {
			err = errors.New("summary admitted after fence writer")
		}
		claimDone <- err
	}()
	close(commitFence)
	if err := <-writerDone; err != nil {
		t.Fatal(err)
	}
	if err := <-claimDone; !errors.Is(err, ErrSummaryPaused) {
		t.Fatal("claim did not observe the committed fence", err)
	}
	if h.count("conversation_summary_attempts") != 0 {
		t.Fatal("queued claim created summary work")
	}
}

func TestSummaryClaimPreservesFenceReadErrors(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	if _, err := h.app.BindMainThread(ctx, "main", "source", "tools", "source.rollout"); err != nil {
		t.Fatal(err)
	}
	snap, err := h.app.HandoffSnapshot(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `ALTER TABLE upgrades RENAME TO unavailable_upgrades`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if claimed, err := h.app.ClaimSummaryAttempt(ctx, snap, 0); claimed || err == nil || errors.Is(err, ErrSummaryPaused) {
		t.Fatalf("fence read error was hidden: claimed=%t err=%v", claimed, err)
	}
	if h.count("conversation_summary_attempts") != 0 {
		t.Fatal("failed fence read admitted a summary")
	}
}

func TestSummaryClaimRetainsUpgradeReleaseSemantics(t *testing.T) {
	for _, kind := range []string{"runners", "attention-before-candidate", "completed"} {
		t.Run(kind, func(t *testing.T) {
			h := newHarness(t)
			ctx := t.Context()
			if _, err := h.app.BindMainThread(ctx, "main", "source", "tools", "source.rollout"); err != nil {
				t.Fatal(err)
			}
			row := store.UpgradeRow{View: protocol.UpgradeView{UpgradeID: "upgrade", ReleaseID: "release", Revision: 1, Status: "running", Phase: "runners"}}
			switch kind {
			case "attention-before-candidate":
				row.View.Status, row.View.Phase = "attention", "server_ready"
			case "completed":
				row.View.Status, row.View.Phase = "completed", "completed"
			}
			if err := h.store.Write(ctx, func(tx *store.Tx) error { return tx.PutUpgrade(ctx, row) }); err != nil {
				t.Fatal(err)
			}
			snap, err := h.app.HandoffSnapshot(ctx, "main")
			if err != nil {
				t.Fatal(err)
			}
			if claimed, err := h.app.ClaimSummaryAttempt(ctx, snap, 0); err != nil || !claimed {
				t.Fatalf("released upgrade still fenced summary: claimed=%t err=%v", claimed, err)
			}
		})
	}
}
