package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func repairPrefixFixture(t *testing.T) (*harness, protocol.Assignment) {
	t.Helper()
	h, as, _ := lapsedReplyFixture(t)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		var request, message string
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT request_id,message_id FROM main_turns WHERE thread_id='native' AND turn_id='original-turn'`).Scan(&request, &message); err != nil {
			return err
		}
		logical := h.ids.New(protocol.PrefixRequest)
		if _, err := tx.Exec(t.Context(), `INSERT INTO logical_turns(logical_turn_id,message_id,status,repair_attempts,updated_at) VALUES(?,?,'pending_repair',1,?)`, logical, message, h.clock.Now().UnixMilli()); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO logical_turn_executions(request_id,logical_turn_id,repair) VALUES(?,?,1)`, request, logical)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return h, as
}

func TestManagerRepairPrefixAuditsOnlyConsumedInternalText(t *testing.T) {
	h, as := repairPrefixFixture(t)
	const body = "part17part18part19"
	consumed := []int64{17, 18, 19}
	if err := h.app.AcknowledgeManagerReplyPrefix(t.Context(), as.RunID, 19, "native", "original-turn", "original-item", body, consumed); protocol.CodeOf(err) != protocol.CodeUnknownOutcome {
		t.Fatal("ordinary reply acknowledgement no longer requires durable terminal text", err)
	}
	ack := func() error {
		return h.app.AcknowledgeManagerRepairPrefix(t.Context(), as.RunID, 19, "native", "original-turn", "original-item", body, consumed)
	}
	if err := ack(); err != nil {
		t.Fatal(err)
	}
	if err := ack(); err != nil {
		t.Fatal("same consumed repair prefix was not idempotent", err)
	}
	if h.countWhere(`SELECT COUNT(*) FROM events WHERE object_id=? AND kind='run.manager_repair_prefix_consumed'`, as.RunID) != 1 || h.countWhere(`SELECT COUNT(*) FROM manager_native_events WHERE run_id=? AND seq>=17 AND applied=0`, as.RunID) != 0 || h.countWhere(`SELECT COUNT(*) FROM manager_native_events WHERE run_id=?`, as.RunID) != 19 {
		t.Fatal("repair consumption lost original events or duplicated its audit")
	}
	if h.countWhere(`SELECT COUNT(*) FROM conversation_agent_messages WHERE thread_id='native' AND turn_id='original-turn'`) != 0 || h.countWhere(`SELECT COUNT(*) FROM background_replies`) != 0 || h.countWhere(`SELECT COUNT(*) FROM native_turn_receipts_pending`) != 1 || h.countWhere(`SELECT COUNT(*) FROM main_turns WHERE thread_id='native' AND turn_id='original-turn' AND status='interrupted'`) != 1 {
		t.Fatal("internal repair text was published or business uncertainty was settled")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var raw string
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT body_json FROM events WHERE object_id=? AND kind='run.manager_repair_prefix_consumed'`, as.RunID).Scan(&raw); err != nil {
			return err
		}
		var audit map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &audit); err != nil {
			return err
		}
		var hash string
		if err := json.Unmarshal(audit["prefix_sha256"], &hash); err != nil {
			return err
		}
		sum := sha256.Sum256([]byte(body))
		if hash != hex.EncodeToString(sum[:]) || len(audit) != 6 || audit["body"] != nil || string(audit["consumed_sequences"]) != "[17,18,19]" {
			t.Fatal("repair audit did not retain its exact non-public-text consumption proof", raw)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestManagerRepairPrefixRejectsUnprovenScope(t *testing.T) {
	for _, scenario := range []string{"not_repair", "active_not_repair", "thread", "turn", "item", "run", "body", "missing_delta", "foreign_delta", "duplicate_delta", "epoch", "no_pending", "pin", "not_closing"} {
		t.Run(scenario, func(t *testing.T) {
			h, as := repairPrefixFixture(t)
			run, thread, turn, item, body := as.RunID, "native", "original-turn", "original-item", "part17part18part19"
			consumed := []int64{17, 18, 19}
			switch scenario {
			case "thread":
				thread = "another-thread"
			case "turn":
				turn = "another-turn"
			case "item":
				item = "another-item"
			case "run":
				run = "another-run"
			case "body":
				body = "invented repair text"
			case "missing_delta":
				consumed = []int64{17, 19}
			case "foreign_delta":
				consumed = []int64{16, 17, 18, 19}
			case "duplicate_delta":
				consumed = []int64{17, 18, 19, 19}
			default:
				if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
					query := map[string]string{
						"not_repair":        `UPDATE logical_turn_executions SET repair=0`,
						"active_not_repair": `UPDATE logical_turn_executions SET repair=0`,
						"epoch":             `UPDATE runner_coordination_executions SET assignment_epoch=assignment_epoch+1`,
						"no_pending":        `DELETE FROM native_turn_receipts_pending`,
						"pin":               `UPDATE issue_manager_tasks SET pin_json=json_set(pin_json,'$.agent_revision',99)`,
						"not_closing":       `DELETE FROM manager_native_requests WHERE method='close'`,
					}[scenario]
					if scenario == "active_not_repair" {
						if _, err := tx.Exec(t.Context(), `UPDATE leases SET status='active' WHERE run_id=?`, as.RunID); err != nil {
							return err
						}
					}
					_, err := tx.Exec(t.Context(), query)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			if err := h.app.AcknowledgeManagerRepairPrefix(t.Context(), run, 19, thread, turn, item, body, consumed); err == nil {
				t.Fatal("unproven repair prefix was consumed")
			}
			if h.countWhere(`SELECT COUNT(*) FROM events WHERE kind='run.manager_repair_prefix_consumed'`) != 0 || h.countWhere(`SELECT COUNT(*) FROM manager_native_events WHERE run_id=? AND seq>=17 AND applied=0`, as.RunID) != 3 {
				t.Fatal("rejected repair changed original audit or event state")
			}
		})
	}
}

func TestFault_ManagerRepairPrefixAuditAndAcknowledgementAreAtomic(t *testing.T) {
	h, as := repairPrefixFixture(t)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `CREATE TRIGGER reject_repair_ack BEFORE UPDATE OF applied ON manager_native_events WHEN NEW.applied=1 BEGIN SELECT RAISE(ABORT,'test repair acknowledgement unavailable'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.app.AcknowledgeManagerRepairPrefix(t.Context(), as.RunID, 19, "native", "original-turn", "original-item", "part17part18part19", []int64{17, 18, 19}); err == nil {
		t.Fatal("failed acknowledgement reported success")
	}
	if h.countWhere(`SELECT COUNT(*) FROM events WHERE kind='run.manager_repair_prefix_consumed'`) != 0 || h.countWhere(`SELECT COUNT(*) FROM manager_native_events WHERE run_id=? AND seq>=17 AND applied=0`, as.RunID) != 3 {
		t.Fatal("failed transaction retained an audit without its acknowledgement")
	}
}

func TestManagerRepairPrefixKeepsLaterOriginalDeltaUnapplied(t *testing.T) {
	h, as := repairPrefixFixture(t)
	late := protocol.ManagerNativeEvent{Sequence: 20, Body: json.RawMessage(`{"thread_id":"native","native":{"Kind":"agent_message_delta","TurnID":"original-turn","ItemID":"original-item","Text":"later"}}`)}
	if op := h.runAs(as, protocol.OpManagerNativeEvent, late, ManagerNativeCommand{OperationName: protocol.OpManagerNativeEvent}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := h.app.AcknowledgeManagerRepairPrefix(t.Context(), as.RunID, 19, "native", "original-turn", "original-item", "part17part18part19", []int64{17, 18, 19}); err != nil {
		t.Fatal(err)
	}
	if h.countWhere(`SELECT COUNT(*) FROM manager_native_events WHERE run_id=? AND seq=20 AND applied=0`, as.RunID) != 1 {
		t.Fatal("fixed repair prefix acknowledged a later original event")
	}
}

func TestFault_ActiveClosingManagerConsumesRepairBeforeOriginalCloseReceipt(t *testing.T) {
	h, as := repairPrefixFixture(t)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE leases SET status='active' WHERE run_id=?`, as.RunID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var originalClose string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT request_id FROM manager_native_requests WHERE run_id=? AND method='close'`, as.RunID).Scan(&originalClose)
	}); err != nil {
		t.Fatal(err)
	}
	// These old pending requests already exist in a stopped native process's
	// server queue. New coalescing must not erase or execute their history.
	var queued []string
	for _, method := range []string{"find", "close", "find", "close"} {
		id := h.ids.New(protocol.PrefixRequest)
		queued = append(queued, id)
		if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
			_, err := tx.Exec(t.Context(), `INSERT INTO manager_native_requests(request_id,run_id,method,payload_json,status,created_at) VALUES(?,?,?,'{}','pending',?)`, id, as.RunID, method, h.clock.Now().UnixMilli())
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	started := h.ids.New(protocol.PrefixRequest)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO manager_native_requests(request_id,run_id,method,payload_json,status,created_at) VALUES(?,?,'tool_result','{}','started',?)`, started, as.RunID, h.clock.Now().UnixMilli())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	frozen := h.env(protocol.OpManagerNativeReply, runTarget(as.RunID), protocol.ManagerNativeReply{ID: originalClose, Result: json.RawMessage(`null`), LastSequence: 19}, nil)
	principal := runPrincipal(as.RunID, as.Role)
	if _, err := h.app.Execute(t.Context(), principal, frozen, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply}); protocol.CodeOf(err) != protocol.CodeUnknownOutcome {
		t.Fatal("active close bypassed its unapplied repair prefix", err)
	}
	if err := h.app.AcknowledgeManagerRepairPrefix(t.Context(), as.RunID, 19, "native", "original-turn", "original-item", "part17part18part19", []int64{17, 18, 19}); err != nil {
		t.Fatal("active closing repair cannot consume its fixed original prefix", err)
	}
	if op, err := h.app.Execute(t.Context(), principal, frozen, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply}); err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal("the same immutable physical close receipt did not settle", op, err)
	}
	if h.countWhere(`SELECT COUNT(*) FROM native_turn_receipts_pending`) != 1 || h.countWhere(`SELECT COUNT(*) FROM main_turns WHERE turn_id='original-turn' AND status='interrupted'`) != 1 || h.countWhere(`SELECT COUNT(*) FROM manager_native_requests WHERE method='resume'`) != 1 || h.countWhere(`SELECT COUNT(*) FROM manager_native_requests WHERE method='turn'`) != 0 || h.countWhere(`SELECT COUNT(*) FROM events WHERE kind='run.manager_repair_prefix_consumed'`) != 1 {
		t.Fatal("physical close consumed business uncertainty, started native work or lost its original audit")
	}
	for _, id := range queued {
		reply, ready, err := h.app.ManagerNativeResult(t.Context(), as.RunID, id)
		if err != nil || !ready || !reply.Rejected || reply.Error == "" {
			t.Fatal("old unclaimed request was not explicitly rejected after physical stop", id, reply, ready, err)
		}
	}
	uncertain, ready, err := h.app.ManagerNativeResult(t.Context(), as.RunID, started)
	if err != nil || !ready || uncertain.Rejected || uncertain.Error == "" || h.countWhere(`SELECT COUNT(*) FROM manager_native_requests WHERE request_id=? AND status='unknown'`, started) != 1 {
		t.Fatal("previously claimed callback delivery was treated as unexecuted or complete", uncertain, ready, err)
	}
	if h.countWhere(`SELECT COUNT(*) FROM manager_native_requests WHERE run_id=?`, as.RunID) != 7 || h.countWhere(`SELECT COUNT(*) FROM conversation_agent_messages WHERE thread_id='native'`) != 0 || h.countWhere(`SELECT COUNT(*) FROM background_replies`) != 0 {
		t.Fatal("closing repair removed old request history or published internal prose")
	}
}
