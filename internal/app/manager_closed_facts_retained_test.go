package app

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func retainClosedManagerTestBody(t *testing.T, f closedToolFixture, query, update string, args ...any) string {
	t.Helper()
	var original string
	if err := f.h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), query, args...).Scan(&original)
	}); err != nil {
		t.Fatal(err)
	}
	// Preserve the exact original JSON, including presentation bytes which must
	// be hashed rather than the smaller retained identity projection.
	original += strings.Repeat(" \n\t", 500)
	staged, err := f.h.store.StageOriginals(t.Context(), "closed-manager-test", []byte(original))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.h.store.Write(staged, func(tx *store.Tx) error {
		retained, err := tx.RetainJSON(staged, original)
		if err != nil {
			return err
		}
		if retained == original {
			t.Fatal("fixture did not retain the original body")
		}
		_, err = tx.Exec(staged, update, append([]any{retained}, args...)...)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return original
}

func TestClosedManagerRepairConsumesRetainedOriginalFacts(t *testing.T) {
	for _, scenario := range []string{"callback", "started", "completed", "all"} {
		t.Run(scenario, func(t *testing.T) {
			f := closedRepairToolFixture(t, true)
			originals := map[int64]string{}
			for i, kind := range []string{"callback", "started", "completed"} {
				if scenario == kind || scenario == "all" {
					seq := int64(20 + i)
					originals[seq] = retainClosedManagerTestBody(t, f,
						`SELECT body_json FROM manager_native_events WHERE run_id=? AND seq=?`,
						`UPDATE manager_native_events SET body_json=? WHERE run_id=? AND seq=?`, f.as.RunID, seq)
				}
			}
			var payload, reply string
			if scenario == "all" {
				payload = retainClosedManagerTestBody(t, f,
					`SELECT payload_json FROM manager_native_requests WHERE request_id=?`,
					`UPDATE manager_native_requests SET payload_json=? WHERE request_id=?`, f.resultID)
				reply = retainClosedManagerTestBody(t, f,
					`SELECT result_json FROM manager_native_requests WHERE request_id=?`,
					`UPDATE manager_native_requests SET result_json=? WHERE request_id=?`, f.resultID)
			}
			before := f.original(t)
			op, err := f.execute(t)
			if err != nil || op.Status != protocol.OpSucceeded {
				t.Fatal("retained original metadata blocked its original close", op, err)
			}
			if f.h.countWhere(`SELECT COUNT(*) FROM manager_native_events WHERE run_id=? AND applied=0`, f.as.RunID) != 0 || !reflect.DeepEqual(before, f.original(t)) {
				t.Fatal("original facts were not consumed without rewriting retained references")
			}
			var audit struct {
				Tools []closedManagerToolProof `json:"tools"`
			}
			if err := f.h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				var raw string
				if err := tx.Q().QueryRowContext(t.Context(), `SELECT body_json FROM events WHERE object_id=? AND kind='run.manager_closed_tool_facts_consumed'`, f.as.RunID).Scan(&raw); err != nil {
					return err
				}
				return store.DecodeJSON(t.Context(), tx.Q(), raw, &audit)
			}); err != nil {
				t.Fatal(err)
			}
			if len(audit.Tools) != 1 || len(audit.Tools[0].Facts) != 3 {
				t.Fatal("missing original tool consumption proof", audit)
			}
			for i, fact := range audit.Tools[0].Facts {
				if fact.Sequence != int64(20+i) {
					t.Fatal("audit changed original event ordering", audit)
				}
				if raw, retained := originals[fact.Sequence]; retained && fact.SHA256 != closedFactHash([]byte(raw)) {
					t.Fatal("audit hashed the retained projection instead of original bytes", fact)
				}
			}
			if scenario == "all" && (audit.Tools[0].ToolResultSHA256 != closedFactHash([]byte(payload)) || audit.Tools[0].ToolResultReplySHA256 != closedFactHash([]byte(reply))) {
				t.Fatal("tool result proof lost original presentation bytes", audit)
			}
			if f.h.countWhere(`SELECT COUNT(*) FROM native_turn_receipts_pending`) != 1 || f.h.countWhere(`SELECT COUNT(*) FROM logical_turns WHERE status='unknown'`) != 1 {
				t.Fatal("transport consumption invented business completion")
			}
			reopenArchiveCombinationHarness(t, f.h)
			again, err := f.execute(t)
			if err != nil || !reflect.DeepEqual(op, again) || f.h.countWhere(`SELECT COUNT(*) FROM events WHERE object_id=? AND kind='run.manager_closed_tool_facts_consumed'`, f.as.RunID) != 1 {
				t.Fatal("original close replay changed after reopening retained files", again, err)
			}
		})
	}
}

func TestClosedManagerRepairRejectsUnavailableRetainedOriginal(t *testing.T) {
	for _, scenario := range []string{"missing", "corrupt"} {
		t.Run(scenario, func(t *testing.T) {
			f := closedRepairToolFixture(t, true)
			original := retainClosedManagerTestBody(t, f,
				`SELECT body_json FROM manager_native_events WHERE run_id=? AND seq=?`,
				`UPDATE manager_native_events SET body_json=? WHERE run_id=? AND seq=?`, f.as.RunID, 22)
			var path, archive string
			var offset int64
			if err := f.h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				var sequence int
				var name string
				if err := tx.Q().QueryRowContext(t.Context(), `PRAGMA database_list`).Scan(&sequence, &name, &path); err != nil {
					return err
				}
				return tx.Q().QueryRowContext(t.Context(), `SELECT archive_id,byte_offset FROM retained_original_bodies WHERE sha256=?`, closedFactHash([]byte(original))).Scan(&archive, &offset)
			}); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(path+".files", "archives", archive)
			if scenario == "missing" {
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
			} else {
				body, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				body[offset] ^= 1
				if err := os.WriteFile(file, body, 0600); err != nil {
					t.Fatal(err)
				}
			}
			before := f.original(t)
			wantError := "open original"
			if scenario == "corrupt" {
				wantError = "integrity mismatch"
			}
			if op, err := f.execute(t); err == nil || !strings.Contains(err.Error(), wantError) {
				t.Fatal("unreadable original was not reported as a file integrity failure", op, err)
			}
			if f.h.countWhere(`SELECT COUNT(*) FROM manager_native_events WHERE run_id=? AND applied=0`, f.as.RunID) != 3 || !reflect.DeepEqual(before, f.original(t)) {
				t.Fatal("failed close changed its original event slice")
			}
			if f.h.countWhere(`SELECT COUNT(*) FROM events WHERE kind='run.manager_closed_tool_facts_consumed'`) != 0 || f.h.countWhere(`SELECT COUNT(*) FROM native_turn_receipts_pending`) != 1 {
				t.Fatal("failed close committed an audit or invented completion")
			}
		})
	}
}
