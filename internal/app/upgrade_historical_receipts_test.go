package app

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// Retain the historical boundary produced by earlier transports: the carrier
// ended, but its original native receipt and delivery did not settle with it.
func historicalParkFixture(t *testing.T) (*harness, protocol.Assignment, protocol.Assignment, store.MainTurn, Delivery, StartUpgrade) {
	t.Helper()
	h, runtime, original, command := parkUpgradeFixture(t)
	delivery, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+original.Manager.ConversationID)
	if err != nil || !ok {
		t.Fatal(delivery, ok, err)
	}
	turn := startHostedReview(t, h, original.Manager.ConversationID, delivery)
	issue := canonicalTaskIssue(t, h, original.Manager.IssueID)
	dependency := createCanonicalTaskForTest(t, h, issue)
	if op := h.exec(owner, h.env("control.set_mode", taskTarget(dependency), protocol.SetControlModePayload{Mode: protocol.ControlPaused}, rev(h.task(dependency).ControlRevision)), SetControlMode{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	wait := waitForTask(t, h, original, dependency, 0)
	op := h.runAs(original, protocol.OpRunsYieldExternalWait, protocol.YieldExternalWaitPayload{WaitID: wait.WaitID, ThreadID: turn.ThreadID}, YieldExternalWait{})
	if op.Status != protocol.OpSucceeded || h.run(original.RunID).ObservedState != protocol.RunFinished || h.lease(original.RunID).Status != store.LeaseReleased {
		t.Fatal("real physical yield did not release the original carrier", op)
	}
	if pending, err := h.app.NativeReceiptPending(t.Context(), turn.RequestID); err != nil || !pending {
		t.Fatal("physical yield lost the original native receipt responsibility", pending, err)
	}
	// The receipt scheduler admits an actual successor of the same Task; no
	// finished Run credential or lease is revived by this regression.
	next := reviewClaimManager(t, h, runtime)
	if next.RunID == original.RunID || next.TaskID != original.TaskID {
		t.Fatal("missing same-responsibility successor", original, next)
	}
	return h, original, next, turn, delivery, command
}

func TestUpgradeManagerParkPreservesHistoricalReceiptTransport(t *testing.T) {
	for _, state := range []string{"starting", "started", "unknown", "interrupted_pending"} {
		t.Run(state, func(t *testing.T) {
			h, original, next, turn, delivery, command := historicalParkFixture(t)
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				status := state
				if state == "interrupted_pending" {
					status = store.TurnInterrupted
					if _, err := tx.Exec(t.Context(), `INSERT INTO native_turn_receipts_pending(request_id) VALUES(?) ON CONFLICT DO NOTHING`, turn.RequestID); err != nil {
						return err
					}
				}
				_, err := tx.Exec(t.Context(), `UPDATE main_turns SET status=? WHERE request_id=?`, status, turn.RequestID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if op := startTestUpgrade(h, "historical-park", command); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			upgradeTick(h, ServerObservation{})
			batch := upgradeRead(h)
			if len(batch.ManagerParks) != 1 || batch.ManagerParks[0].RunID != next.RunID || batch.View.ManagerParks[0].Phase != "waiting_receipts" || h.countWhere(`SELECT COUNT(*) FROM manager_native_requests WHERE run_id=? AND method='close'`, next.RunID) != 0 {
				t.Fatal("park closed the only carrier for the historical native receipt", batch.View.ManagerParks)
			}
			selected, named, _, err := h.app.HostedRuntimeStartupSelection(t.Context(), next.Manager.ConversationID)
			if err != nil || !named || !selected.ReceiptOnly || selected.RunnerRunID != next.RunID {
				t.Fatal("historical selection did not rebind the captured successor", selected, named, err)
			}
			if allowed, err := h.app.UpgradeParkReceiptRead(t.Context(), next.Manager.ConversationID, next.RunID); err != nil || !allowed {
				t.Fatal("captured successor cannot read original native history", allowed, err)
			}
			if _, err := h.app.QueueManagerNative(t.Context(), original.RunID, "find", json.RawMessage(`{}`)); protocol.CodeOf(err) != protocol.CodeInvalidTransition {
				t.Fatal("historical credential gained new transport authority", err)
			}
			if _, err := h.app.QueueManagerNative(t.Context(), next.RunID, "turn", json.RawMessage(`{}`)); protocol.CodeOf(err) != protocol.CodeInvalidTransition {
				t.Fatal("park receipt carrier gained business authority", err)
			}
			if _, err := h.app.QueueManagerNative(t.Context(), next.RunID, "close", json.RawMessage(`{}`)); protocol.CodeOf(err) != protocol.CodeInvalidTransition {
				t.Fatal("attachment teardown bypassed historical receipt guard", err)
			}
			if h.run(original.RunID).ObservedState != protocol.RunFinished || h.run(next.RunID).ObservedState == protocol.RunFinished {
				t.Fatal("receipt waiting changed physical Run facts")
			}
			// A real native terminal, consumed by the existing history reader,
			// settles the original request, never a replacement delivery.
			turn.Status, turn.NativeReconciled = store.TurnInterrupted, true
			if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
				t.Fatal(err)
			}
			if h.countWhere(`SELECT COUNT(*) FROM outbox WHERE outbox_id=? AND request_id=? AND status='delivered'`, delivery.OutboxID, turn.RequestID) != 1 {
				t.Fatal("original delivery did not settle from its native receipt")
			}
			upgradeTick(h, ServerObservation{})
			batch = upgradeRead(h)
			if batch.View.ManagerParks[0].Phase != "closing" || h.countWhere(`SELECT COUNT(*) FROM manager_native_requests WHERE run_id=? AND method='close' AND request_id=?`, next.RunID, batch.ManagerParks[0].CloseRequestID) != 1 {
				t.Fatal("original park close did not resume after receipt settlement", batch.View.ManagerParks)
			}
			parkCloseForTest(t, h, next)
			upgradeTick(h, ServerObservation{})
			if h.run(next.RunID).ObservedState != protocol.RunFinished || upgradeRead(h).View.ManagerParks[0].Phase != "parked" {
				t.Fatal("proved close failed to release the original park")
			}
		})
	}
}

func TestUpgradeManagerParkHistoricalReceiptScope(t *testing.T) {
	for _, other := range []string{"epoch", "logical_only"} {
		t.Run(other, func(t *testing.T) {
			h, _, next, turn, _, command := historicalParkFixture(t)
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				switch other {
				case "epoch":
					_, err := tx.Exec(t.Context(), `UPDATE runner_coordination_executions SET assignment_epoch=assignment_epoch+1 WHERE execution_id=?`, turn.RequestID)
					return err
				default:
					return nil
				}
			}); err != nil {
				t.Fatal(err)
			}
			if other == "logical_only" {
				turn.Status, turn.NativeReconciled = store.TurnCompleted, true
				if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
					t.Fatal(err)
				}
			}
			if op := startTestUpgrade(h, "scoped-historical-park", command); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			upgradeTick(h, ServerObservation{})
			if upgradeRead(h).View.ManagerParks[0].Phase != "closing" || h.countWhere(`SELECT COUNT(*) FROM manager_native_requests WHERE run_id=? AND method='close'`, next.RunID) != 1 {
				t.Fatal("unrelated native or logical history blocked captured transport")
			}
		})
	}
}

func TestUpgradeManagerParkAllocatesHistoryReaderAfterPhysicalYield(t *testing.T) {
	h, runtime, original, command := parkUpgradeFixture(t)
	delivery, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+original.Manager.ConversationID)
	if err != nil || !ok {
		t.Fatal(delivery, ok, err)
	}
	turn := startHostedReview(t, h, original.Manager.ConversationID, delivery)
	issue := canonicalTaskIssue(t, h, original.Manager.IssueID)
	dependency := createCanonicalTaskForTest(t, h, issue)
	if op := h.exec(owner, h.env("control.set_mode", taskTarget(dependency), protocol.SetControlModePayload{Mode: protocol.ControlPaused}, rev(h.task(dependency).ControlRevision)), SetControlMode{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	wait := waitForTask(t, h, original, dependency, 0)
	if op := startTestUpgrade(h, "yield-during-park", command); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.runAs(original, protocol.OpRunsYieldExternalWait, protocol.YieldExternalWaitPayload{WaitID: wait.WaitID, ThreadID: turn.ThreadID}, YieldExternalWait{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	upgradeTick(h, ServerObservation{})
	if upgradeRead(h).View.ManagerParks[0].Phase != "waiting_receipts" {
		t.Fatal("physical yield silently completed the native turn")
	}
	reader := reviewClaimManager(t, h, runtime)
	if reader.RunID == original.RunID || reader.TaskID != original.TaskID || !reader.Manager.ReceiptOnly {
		t.Fatal("maintenance did not admit an actual original-history reader", reader)
	}
	if allowed, err := h.app.UpgradeParkReceiptRead(t.Context(), original.Manager.ConversationID, reader.RunID); err != nil || !allowed {
		t.Fatal("new original reader cannot attach during maintenance", allowed, err)
	}
	if allowed, err := h.app.UpgradeParkReceiptRead(t.Context(), "main", reader.RunID); err != nil || allowed {
		t.Fatal("reader crossed conversation scope", allowed, err)
	}
	if _, err := h.app.QueueManagerNative(t.Context(), reader.RunID, "turn", json.RawMessage(`{}`)); protocol.CodeOf(err) != protocol.CodeInvalidTransition {
		t.Fatal("maintenance reader started business", err)
	}
	if h.run(original.RunID).ObservedState != protocol.RunFinished || h.lease(original.RunID).Status != store.LeaseReleased {
		t.Fatal("new reader revived old physical execution")
	}
	turn.Status, turn.NativeReconciled = store.TurnInterrupted, true
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	upgradeTick(h, ServerObservation{})
	if upgradeRead(h).View.ManagerParks[0].Phase != "parked" {
		t.Fatal("actual historical terminal did not finish original park")
	}
}

func TestUpgradeManagerParkIgnoresUnknownObservationResults(t *testing.T) {
	for _, method := range []string{"models", "find", "usage"} {
		t.Run(method, func(t *testing.T) {
			h, _, as, command := parkUpgradeFixture(t)
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				_, err := tx.Exec(t.Context(), `INSERT INTO manager_native_requests(request_id,run_id,method,payload_json,status,created_at) VALUES(?,?,?,'{}','unknown',?)`, h.ids.New(protocol.PrefixRequest), as.RunID, method, h.clock.Now().UnixMilli())
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if op := startTestUpgrade(h, "observation-park", command); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			upgradeTick(h, ServerObservation{})
			if upgradeRead(h).View.ManagerParks[0].Phase != "closing" {
				t.Fatal("pure observation unknown blocked physical park")
			}
		})
	}
}

func TestUpgradeManagerParkDoesNotRequireSuccessfulPresentationOfKnownTerminal(t *testing.T) {
	h, _, next, turn, delivery, command := historicalParkFixture(t)
	turn.Status = store.TurnCompleted
	if err := h.app.RecordRolloutEvent(t.Context(), next.Manager.ConversationID, turn.ThreadID, turn.TurnID, "", "native_receipt", json.RawMessage(`{"status":"completed"}`)); err != nil {
		t.Fatal(err)
	}
	if err := h.app.ObserveNativeTurnTerminal(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	if op := startTestUpgrade(h, "known-terminal-presentation", command); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	upgradeTick(h, ServerObservation{})
	if upgradeRead(h).View.ManagerParks[0].Phase != "closing" {
		t.Fatal("known native terminal required output presentation before platform maintenance")
	}
	parkCloseForTest(t, h, next)
	upgradeTick(h, ServerObservation{})
	if h.run(next.RunID).ObservedState != protocol.RunFinished || h.lease(next.RunID).Status != store.LeaseReleased || upgradeRead(h).View.ManagerParks[0].Phase != "parked" {
		t.Fatal("known terminal projection failure stranded a physical carrier or slot")
	}
	if pending, err := h.app.NativeReceiptPending(t.Context(), turn.RequestID); err != nil || !pending {
		t.Fatal("presentation retry was discarded", pending, err)
	}
	if h.countWhere(`SELECT COUNT(*) FROM outbox WHERE outbox_id=? AND status='sending'`, delivery.OutboxID) != 1 {
		t.Fatal("unpresented output claimed delivery")
	}
}

func TestUpgradeManagerParkAdmitsProvedStoppedReceiptReader(t *testing.T) {
	h, original, pending := stoppedRetainedManager(t)
	payload := receiptReaderPayload(t, h, original)
	var runtime protocol.RunnerRuntime
	hostID := h.run(original.RunID).HostID
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		host, err := store.GetHostRuntime(t.Context(), tx.Q(), hostID)
		if err != nil {
			return err
		}
		runtime = host.Runtime
		// The physical-stop fixture already includes an earlier maintenance
		// lineage. Supply only an in-progress window here; the other regression
		// admits the entire window through StartUpgrade and actual Yield.
		return tx.PutUpgrade(t.Context(), store.UpgradeRow{ParkIdleManagers: true, Hosts: []store.UpgradeHost{{HostID: hostID}}, View: protocol.UpgradeView{UpgradeID: "proved-reader-during-park", ReleaseID: "fixture", Status: "running", Phase: "server_draining", CreatedAt: h.clock.Now(), UpdatedAt: h.clock.Now()}})
	}); err != nil {
		t.Fatal(err)
	}
	op, err := h.app.Execute(t.Context(), owner, h.env(protocol.OpReconcileManagerRun, taskTarget(original.TaskID), payload, rev(h.task(original.TaskID).ControlRevision)), ReconcileManagerRun{})
	if err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal("park blocked the original physical proof's receipt reader", op, err)
	}
	reader := reviewClaimManager(t, h, &runtime)
	if reader.RunID == original.RunID || reader.TaskID != original.TaskID {
		t.Fatal("reader revived the old carrier", reader)
	}
	if allowed, err := h.app.UpgradeParkReceiptRead(t.Context(), reader.Manager.ConversationID, reader.RunID); err != nil || !allowed {
		t.Fatal("original facts inaccessible during park", allowed, err)
	}
	if _, err := h.app.QueueManagerNative(t.Context(), reader.RunID, "turn", json.RawMessage(`{}`)); protocol.CodeOf(err) != protocol.CodeInvalidTransition && protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("reader admitted new business", err)
	}
	if retained, err := h.app.NativeReceiptPending(t.Context(), pending); err != nil || !retained {
		t.Fatal("admission settled unknown business", retained, err)
	}
}
