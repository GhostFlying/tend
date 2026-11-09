package app

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func recordHistoricalSettlementPoll(t *testing.T, h *harness, issue string, snapshot *protocol.DeliverySnapshot, kind string) protocol.DeliveryObservation {
	t.Helper()
	h.clock.Advance(time.Second)
	current := canonicalTaskIssue(t, h, issue)
	payload := protocol.IssueRefreshDeliveryPayload{ExpectedAssignmentEpoch: current.AssignmentEpoch, DeliveryID: "pr"}
	if op := h.exec(owner, h.env("issues.refresh_delivery", protocol.Target{Type: "issue", ID: issue}, payload, rev(current.Revision)), IssueRefreshDelivery{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	query := claimBaselineRefresh(t, h)
	if err := h.app.RecordServerDelivery(t.Context(), query.ID, snapshot, kind); err != nil {
		t.Fatal(err)
	}
	return readObservations(t, h, issue)[0]
}

func TestHistoricalDeliverySettlementRetainsKnownConflictAfterFailedPoll(t *testing.T) {
	h, issue, source, execution, batch := historicalDeploymentFixture(t)
	advanceHistoricalDeployment(t, h, issue.IssueID, source, batch)
	h.app.serverDeliveryObservations = true
	matching := readObservations(t, h, issue.IssueID)[0].DeliverySnapshot
	conflict := matching
	conflict.HeadSHA = strings.Repeat("d", 40)
	checked := recordHistoricalSettlementPoll(t, h, issue.IssueID, &conflict, "")
	assertConflict := func() {
		t.Helper()
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			settlement, err := store.DeliveryDeploymentSettlement(t.Context(), tx.Q(), execution, 0)
			if protocol.CodeOf(err) != protocol.CodeStaleRevision || settlement == nil || settlement.Eligible || settlement.InvalidReason != "provider_identity_conflict" {
				t.Fatalf("known provider conflict became eligible: settlement=%+v error=%v", settlement, err)
			}
			view, err := views.DeliveryExecutionAuthority(t.Context(), tx.Q(), execution)
			if err == nil && (view.Settlement == nil || view.Settlement.Eligible || view.Settlement.InvalidReason != "provider_identity_conflict") {
				t.Fatal("worker projection lost the known provider conflict", view)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	assertConflict()
	failed := recordHistoricalSettlementPoll(t, h, issue.IssueID, nil, "timeout")
	if checked.CheckedAt == 0 || failed.CheckedAt != checked.CheckedAt || failed.AttemptedAt <= checked.AttemptedAt || failed.ErrorKind != "timeout" || !reflect.DeepEqual(failed.DeliverySnapshot, checked.DeliverySnapshot) {
		t.Fatal("failed poll did not retain its last real successful snapshot", checked, failed)
	}
	assertConflict()
	op := updateDeliveryForTest(t, h, issue.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: source, Action: "complete", UpgradeID: batch.View.UpgradeID, IssueControlPayload: protocol.IssueControlPayload{Reason: "A poll failure cannot clear a known identity conflict"}})
	if op.Status != protocol.OpFailed || op.Error == nil || op.Error.Code != protocol.CodeStaleRevision || deliveryDutyForTest(t, h, issue.IssueID, source).Status == "complete" {
		t.Fatal("formal settlement erased the unresolved conflict", op)
	}

	// Only a new successful observation can replace those conflicting facts.
	// The later target and maintenance baseline still do not become the source
	// of the original installation or restore current execution authority.
	recordHistoricalSettlementPoll(t, h, issue.IssueID, &matching, "")
	recordHistoricalSettlementPoll(t, h, issue.IssueID, nil, "timeout")
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		view, err := views.DeliveryExecutionAuthority(t.Context(), tx.Q(), execution)
		if err == nil && (view.Valid || view.Settlement == nil || !view.Settlement.Eligible || view.Settlement.UpgradeID != batch.View.UpgradeID || view.Settlement.TargetSHA != batch.View.SourceCommit) {
			t.Fatal("historical settlement confused the later target with its original source", view)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestHistoricalDeliverySettlementDoesNotInventMissingSuccessfulSnapshot(t *testing.T) {
	h, issue, _, execution, batch := historicalDeploymentStateFixture(t, "open")
	h.app.serverDeliveryObservations = true
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		// A legacy completed installation without the typed delivery-task
		// binding cannot establish the open candidate's original merged source.
		batch.DeliveryTaskID = ""
		if err := tx.PutUpgrade(t.Context(), batch); err != nil {
			return err
		}
		row, err := store.GetDeliveryObservation(t.Context(), tx.Q(), issue.IssueID, "pr")
		if err != nil {
			return err
		}
		row.Observation.DeliverySnapshot = protocol.DeliverySnapshot{}
		row.Observation.CheckedAt = 0
		return putDeliveryObservationForTest(tx, t.Context(), row)
	}); err != nil {
		t.Fatal(err)
	}
	failed := recordHistoricalSettlementPoll(t, h, issue.IssueID, nil, "timeout")
	if failed.CheckedAt != 0 || failed.State != "unknown" || failed.ErrorKind != "timeout" {
		t.Fatal("failed first read invented provider facts", failed)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		settlement, err := store.DeliveryDeploymentSettlement(t.Context(), tx.Q(), execution, 0)
		if protocol.CodeOf(err) != protocol.CodeUnknownOutcome || settlement == nil || settlement.Eligible || settlement.InvalidReason != "original_source_unknown" {
			t.Fatalf("missing successful snapshot became proof: settlement=%+v error=%v", settlement, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
