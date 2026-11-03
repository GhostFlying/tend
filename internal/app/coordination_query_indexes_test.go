package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/store"
)

func TestManagerCloseIndexedCallbackIdentity(t *testing.T) {
	h, _, _, as, _ := reviewManagerAssignment(t)
	ctx := t.Context()
	const history = 128
	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		for i := range history {
			payload, _ := json.Marshal(map[string]string{"thread_id": "thread", "turn_id": "turn", "call_id": fmt.Sprint(i)})
			body, _ := json.Marshal(map[string]any{"call": map[string]string{"ThreadID": "thread", "TurnID": "turn", "CallID": fmt.Sprint(i)}})
			if _, err := tx.Exec(ctx, `INSERT INTO manager_native_requests(request_id,run_id,method,payload_json,status,created_at) VALUES(?,?,'tool_result',?,'completed',?)`, fmt.Sprintf("historical-callback-%d", i), as.RunID, string(payload), i); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO manager_native_events(run_id,seq,body_json) VALUES(?,?,?)`, as.RunID, i+1, string(body)); err != nil {
				return err
			}
		}
		// Neither a different thread/turn nor missing identity may be settled
		// by a callback with the same call ID in the historical run.
		for i, body := range []string{
			`{"call":{"ThreadID":"different","TurnID":"turn","CallID":"0"}}`,
			`{"call":{"ThreadID":"thread","TurnID":"different","CallID":"0"}}`,
			`{"call":{"ThreadID":"thread","TurnID":"turn"}}`,
		} {
			if _, err := tx.Exec(ctx, `INSERT INTO manager_native_events(run_id,seq,body_json) VALUES(?,?,?)`, as.RunID, history+i+1, body); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, indexed := range []bool{false, true} {
		if err := h.store.Write(ctx, func(tx *store.Tx) error {
			if !indexed {
				if _, err := tx.Exec(ctx, `DROP INDEX manager_native_tool_results`); err != nil {
					return err
				}
			} else if _, err := tx.Exec(ctx, `CREATE INDEX manager_native_tool_results ON manager_native_requests(run_id,json_extract(payload_json,'$.thread_id'),json_extract(payload_json,'$.turn_id'),json_extract(payload_json,'$.call_id')) WHERE method='tool_result'`); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE manager_native_events SET applied=0 WHERE run_id=?`, as.RunID); err != nil {
				return err
			}
			start := time.Now()
			if err := settleClosedManagerToolResults(ctx, tx, as.RunID); err != nil {
				return err
			}
			t.Logf("indexed=%v history=%d elapsed=%s", indexed, history, time.Since(start))
			var applied, pending int
			if err := tx.Q().QueryRowContext(ctx, `SELECT SUM(applied),SUM(1-applied) FROM manager_native_events WHERE run_id=?`, as.RunID).Scan(&applied, &pending); err != nil {
				return err
			}
			if applied != history || pending != 3 {
				return fmt.Errorf("indexed=%v callback identity changed: applied=%d pending=%d", indexed, applied, pending)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
		rows, err := tx.Q().QueryContext(ctx, `EXPLAIN QUERY PLAN SELECT 1 FROM manager_native_requests r WHERE r.run_id=? AND r.method='tool_result' AND json_extract(r.payload_json,'$.thread_id')=? AND json_extract(r.payload_json,'$.turn_id')=? AND json_extract(r.payload_json,'$.call_id')=?`, as.RunID, "thread", "turn", "0")
		if err != nil {
			return err
		}
		defer rows.Close()
		found := false
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				return err
			}
			found = found || strings.Contains(detail, "USING INDEX manager_native_tool_results")
		}
		if !found {
			return fmt.Errorf("native close lookup no longer uses the callback identity index")
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
}
