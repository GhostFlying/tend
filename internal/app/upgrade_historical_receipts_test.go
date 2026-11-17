package app

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// Retain the historical boundary produced by earlier transports: the carrier
// ended, but its original native receipt and delivery did not settle with it.
func historicalParkFixture(t *testing.T) (*harness, protocol.Assignment, store.MainTurn, Delivery, StartUpgrade) {
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
	waitForTask(t, h, original, dependency, 0)
	closeID, err := h.app.QueueManagerNative(t.Context(), original.RunID, "close", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if op := h.runAs(original, protocol.OpManagerNativeClaim, map[string]any{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.runAs(original, protocol.OpManagerNativeReply, protocol.ManagerNativeReply{ID: closeID, Result: json.RawMessage(`null`)}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.run(original.RunID).ObservedState != protocol.RunFinished || h.lease(original.RunID).Status != store.LeaseReleased {
		t.Fatal("physical close did not release the original carrier")
	}
	if pending, err := h.app.NativeReceiptPending(t.Context(), turn.RequestID); err != nil || !pending {
		t.Fatal("physical yield lost the original native receipt responsibility", pending, err)
	}
	if next, ok := h.claimInputRun(runtime); ok {
		t.Fatal("history allocated another Run", next)
	}
	return h, original, turn, delivery, command
}

func TestUpgradePreservesOriginalHeartbeatHistoryAfterPhysicalYield(t *testing.T) {
	for _, state := range []string{"starting", "started", "unknown", "interrupted_pending"} {
		t.Run(state, func(t *testing.T) {
			h, original, turn, delivery, command := historicalParkFixture(t)
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				status := state
				if state == "interrupted_pending" {
					status = store.TurnInterrupted
				}
				_, err := tx.Exec(t.Context(), `UPDATE main_turns SET status=? WHERE request_id=?`, status, turn.RequestID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if op := startPreparedTestUpgrade(h, "historical-park", command); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			upgradeTick(h, ServerObservation{})
			if len(upgradeRead(h).ManagerParks) != 0 {
				t.Fatal("history-only transport acquired a physical park")
			}
			selection, named, err := h.app.HostedRuntimeSelection(t.Context(), original.Manager.ConversationID)
			if err != nil || !named || !selection.ReceiptOnly || selection.RunnerRunID != original.RunID {
				t.Fatal(selection, named, err)
			}
			payload, _ := json.Marshal(map[string]string{"thread_id": turn.ThreadID, "message_id": turn.MessageID, "turn_id": turn.TurnID})
			id, err := h.app.QueueManagerNative(t.Context(), original.RunID, "find", payload)
			if err != nil {
				t.Fatal(err)
			}
			var runtime protocol.RunnerRuntime
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				rt, e := store.GetHostRuntime(t.Context(), tx.Q(), "linux-1")
				runtime = rt.Runtime
				return e
			}); err != nil {
				t.Fatal(err)
			}
			runtime.Sequence++
			response, err := h.app.RuntimeHeartbeat(t.Context(), runner("linux-1"), protocol.HeartbeatPayload{Runtime: &runtime})
			if err != nil || len(response.HistoryReads) != 1 || response.HistoryReads[0].ID != id || response.HistoryReads[0].RunID != original.RunID {
				t.Fatal(response, err)
			}
			if _, err := h.app.QueueManagerNative(t.Context(), original.RunID, "turn", json.RawMessage(`{}`)); err == nil {
				t.Fatal("history regained business authority")
			}
			turn.Status, turn.NativeReconciled = store.TurnInterrupted, true
			if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
				t.Fatal(err)
			}
			if h.countWhere(`SELECT COUNT(*) FROM outbox WHERE outbox_id=? AND request_id=? AND status='delivered'`, delivery.OutboxID, turn.RequestID) != 1 {
				t.Fatal("original delivery did not settle")
			}
			if h.lease(original.RunID).Status != store.LeaseReleased {
				t.Fatal("history revived a lease")
			}
		})
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
			if op := startPreparedTestUpgrade(h, "observation-park", command); op.Status != protocol.OpSucceeded {
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
	h, next, turn, delivery, command := historicalParkFixture(t)
	turn.Status = store.TurnCompleted
	if err := h.app.RecordRolloutEvent(t.Context(), next.Manager.ConversationID, turn.ThreadID, turn.TurnID, "", "native_receipt", json.RawMessage(`{"status":"completed"}`)); err != nil {
		t.Fatal(err)
	}
	if err := h.app.ObserveNativeTurnTerminal(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	if op := startPreparedTestUpgrade(h, "known-terminal-presentation", command); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	upgradeTick(h, ServerObservation{})
	if len(upgradeRead(h).View.ManagerParks) != 0 {
		t.Fatal("known native terminal required output presentation before platform maintenance")
	}
	upgradeTick(h, ServerObservation{})
	if h.run(next.RunID).ObservedState != protocol.RunFinished || h.lease(next.RunID).Status != store.LeaseReleased {
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
	if reader, ok := h.claimInputRun(&runtime); ok {
		t.Fatal("parked receipt read consumed a new business Run", reader)
	}
	selection, named, err := h.app.HostedRuntimeSelection(t.Context(), original.Manager.ConversationID)
	if err != nil || !named || selection.RunnerRunID != original.RunID || !selection.RequireReceiptReader {
		t.Fatal(selection, err)
	}
	if _, err := h.app.QueueManagerNative(t.Context(), original.RunID, "turn", json.RawMessage(`{}`)); err == nil {
		t.Fatal("receipt reader admitted business")
	}
	if retained, err := h.app.NativeReceiptPending(t.Context(), pending); err != nil || !retained {
		t.Fatal("admission settled unknown business", retained, err)
	}
}
