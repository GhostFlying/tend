package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestBoundedServerStartupRetainsUnknownSummaryLineage(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	if _, err := h.app.BindMainThread(ctx, "main", "old-source", "schema", "old.rollout"); err != nil {
		t.Fatal(err)
	}
	snap, err := h.app.HandoffSnapshot(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := h.app.ClaimSummaryAttempt(ctx, snap, time.Minute.Milliseconds()); err != nil || !claimed {
		t.Fatal(claimed, err)
	}
	if _, err := h.app.SaveHandoffSummary(ctx, store.HandoffSummary{Instance: "main", Snapshot: snap, Body: "legacy partial", FormatVersion: 1}); err != nil {
		t.Fatal(err)
	}
	prep, err := h.app.ChangeArchivePreparation(ctx, protocol.ArchiveControlRequest{RequestID: h.ids.New(protocol.PrefixRequest)}, true)
	if err != nil {
		t.Fatal(err)
	}
	previous := protocol.RecoveryIdentity{PID: 111, StartTicks: 222, LaunchID: "old-server", SHA256: strings.Repeat("a", 64)}
	process := protocol.RecoveryIdentity{PID: 333, StartTicks: 444, LaunchID: "new-server", SHA256: strings.Repeat("b", 64)}
	p := protocol.CommittedMaintenancePlan{MaintenanceID: "bounded-summary", RequestID: h.ids.New(protocol.PrefixRequest), LinkRequestID: h.ids.New(protocol.PrefixRequest), PreparationRevision: prep.Revision, Previous: protocol.UpgradeBinding{InstanceID: "server", ProcessInstanceID: previous.LaunchID, ArtifactSHA256: previous.SHA256}, PreviousProcess: previous, Successor: protocol.UpgradeBinding{UpgradeID: "bounded-summary", InstanceID: "server", ProcessInstanceID: process.LaunchID, ArtifactSHA256: process.SHA256}, ReceiptRecoveryStop: &protocol.CommittedReceiptRecoveryStop{}}
	raw, _ := json.Marshal(p)
	sum := sha256.Sum256(raw)
	auth := protocol.BoundedMaintenanceAuthorization{RequestID: h.ids.New(protocol.PrefixRequest), Role: "server", Source: previous, Target: p.Successor, PlanSHA256: hex.EncodeToString(sum[:]), Reason: "retain unknown predecessor work"}
	b := protocol.BoundedMaintenanceState{Authorization: auth, SourceExit: &protocol.BoundedProcessExit{Process: previous, ConfirmedAtMillis: h.clock.Now().UnixMilli(), Method: "exact_exit"}}
	if _, err := h.app.RegisterBoundedCommittedMaintenance(ctx, p, process, b); err != nil {
		t.Fatal(err)
	}
	// Mutable observation progress cannot change registration request identity.
	b.Phase = "complete"
	b.Outcome = "installed_recovery_pending"
	if _, err := h.app.RegisterBoundedCommittedMaintenance(ctx, p, process, b); err != nil {
		t.Fatal("registration replay changed", err)
	}
	h.clock.Advance(time.Hour)
	if claimed, err := h.app.ClaimSummaryAttempt(ctx, snap, 0); err != nil || claimed {
		t.Fatal("old unknown fork was repeated", claimed, err)
	}
	if _, err := h.app.SaveHandoffSummary(ctx, store.HandoffSummary{Instance: "main", Snapshot: snap, Body: "late result", FormatVersion: 3}); protocol.CodeOf(err) != protocol.CodeUnknownOutcome {
		t.Fatal("old orphan result was published", err)
	}
	if err := h.app.FinishSummaryAttempt(ctx, snap, true); protocol.CodeOf(err) != protocol.CodeUnknownOutcome {
		t.Fatal("old unknown verdict was overwritten", err)
	}
	if _, ok, err := h.app.HandoffSummary(ctx, "main"); err != nil || ok {
		t.Fatal("old protected summary was consumed", ok, err)
	}
	if h.count(`conversation_summary_attempts WHERE status='started'`) != 1 {
		t.Fatal("original unknown summary was rewritten")
	}
	if _, err := h.app.BindMainThread(ctx, "independent-main", "independent-source", "new-schema", "new.rollout"); err != nil {
		t.Fatal(err)
	}
	fresh, err := h.app.HandoffSnapshot(ctx, "independent-main")
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := h.app.ClaimSummaryAttempt(ctx, fresh, 0); claimed || !errors.Is(err, ErrSummaryPaused) {
		t.Fatal("unlinked maintenance admitted new summary work", claimed, err)
	}
	proof := protocol.CommittedMaintenanceReceipt{MaintenanceID: p.MaintenanceID, RequestID: p.RequestID, Previous: p.Previous, Successor: p.Successor, Process: process, Generation: 1, Epoch: process.LaunchID, FirstSequence: 1, SecondSequence: 2}
	if _, err := h.app.LinkCommittedMaintenance(ctx, proof); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.ChangeArchivePreparation(ctx, protocol.ArchiveControlRequest{RequestID: h.ids.New(protocol.PrefixRequest), ExpectedControlRevision: prep.Revision}, false); err != nil {
		t.Fatal(err)
	}
	fresh, err = h.app.HandoffSnapshot(ctx, "independent-main")
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := h.app.ClaimSummaryAttempt(ctx, fresh, 0); err != nil || !claimed {
		t.Fatal("independent source was globally blocked", claimed, err)
	}
	if claimed, err := h.app.ClaimSummaryAttempt(ctx, snap, 0); err != nil || claimed {
		t.Fatal("maintenance release readmitted the original unknown source", claimed, err)
	}
}
