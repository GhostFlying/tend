package app

import (
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestFault_StoppedManagerUnknownObservationsDoNotBlockSettlement(t *testing.T) {
	for _, method := range []string{"models", "find", "usage", "resume", "interrupt", "close", "tool_result", "release", "turn"} {
		t.Run(method, func(t *testing.T) {
			h, as, pending := stoppedRetainedManager(t)
			resolveRetainedBusinessReceipt(t, h, pending)
			request := h.ids.New(protocol.PrefixRequest)
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				_, err := tx.Exec(t.Context(), `INSERT INTO manager_native_requests(request_id,run_id,method,payload_json,status,result_json,created_at) VALUES(?,?,?,'{}','unknown',NULL,?)`, request, as.RunID, method, h.clock.Now().UnixMilli())
				return err
			}); err != nil {
				t.Fatal(err)
			}
			state, err := h.app.ManagerRunReconciliation(t.Context(), as.TaskID, as.RunID)
			if err != nil {
				t.Fatal(err)
			}
			read := method == "models" || method == "find" || method == "usage"
			if state.CanSettleWaiting != read || state.PendingBusinessReceipts || (state.UnresolvedReadRequests == 1) != read || (state.UnresolvedNativeRequests == 0) != read {
				t.Fatal("observation and side effect were conflated", state)
			}
			payload := managerReconciliationPayload(t, h, as)
			_, err = h.app.Execute(t.Context(), owner, h.env(protocol.OpReconcileManagerRun, taskTarget(as.TaskID), payload, rev(state.TaskRevision)), ReconcileManagerRun{})
			if read && err != nil || !read && protocol.CodeOf(err) != protocol.CodeUnknownOutcome {
				t.Fatal(method, err)
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				var status string
				if err := tx.Q().QueryRowContext(t.Context(), `SELECT status FROM manager_native_requests WHERE request_id=?`, request).Scan(&status); err != nil {
					return err
				}
				if status != "unknown" {
					t.Fatal("settlement changed the unknown query fact", status)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
