package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	workerharness "github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/coordination"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type closedToolFixture struct {
	h        *harness
	as       protocol.Assignment
	close    *protocol.Envelope
	resultID string
}

func closedRepairToolFixture(t *testing.T, success bool, representations ...string) closedToolFixture {
	t.Helper()
	h, as := repairPrefixFixture(t)
	if err := h.app.AcknowledgeManagerRepairPrefix(t.Context(), as.RunID, 19, "native", "original-turn", "original-item", "part17part18part19", []int64{17, 18, 19}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.Exec(t.Context(), `DELETE FROM manager_native_requests WHERE run_id=? AND method='close'`, as.RunID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE leases SET status='active' WHERE run_id=?`, as.RunID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	args := json.RawMessage(`{"revision":7}`)
	call := workerharness.ToolCall{ThreadID: "native", TurnID: "original-turn", CallID: "original-call", Tool: "inspect_original", Arguments: args}
	result := workerharness.ToolResult{Success: success, Text: "unchanged original result"}
	if len(representations) > 0 {
		result.Text = representations[0]
	}
	if len(representations) > 1 {
		seedManagerToolOutput(t, h, as, call.Tool, string(args), representations[1], call.TurnID, call.CallID)
	}
	request, _ := json.Marshal(coordination.Request{ThreadID: call.ThreadID, TurnID: call.TurnID, CallID: call.CallID, ToolResult: result})
	id, err := h.app.QueueManagerNative(t.Context(), as.RunID, "tool_result", request)
	if err != nil {
		t.Fatal(err)
	}
	h.runAs(as, protocol.OpManagerNativeClaim, map[string]any{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim})
	h.runAs(as, protocol.OpManagerNativeReply, protocol.ManagerNativeReply{ID: id, Result: json.RawMessage(`null`)}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply})
	closeID, err := h.app.QueueManagerNative(t.Context(), as.RunID, "close", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	h.runAs(as, protocol.OpManagerNativeClaim, map[string]any{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim})
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE leases SET status='lapsed' WHERE run_id=?`, as.RunID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE logical_turns SET status='unknown'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	events := []coordination.Event{{ThreadID: "native", Call: &call}}
	for _, completed := range []bool{false, true} {
		kind, status, toolStatus := workerharness.EventItemStarted, "inProgress", protocol.ToolRunning
		var content []map[string]string
		var succeeded *bool
		output := ""
		if completed {
			kind, status, toolStatus = workerharness.EventItemCompleted, "completed", protocol.ToolCompleted
			succeeded = &success
			output = result.Text
			content = []map[string]string{{"type": "inputText", "text": result.Text}}
			if !success {
				toolStatus = protocol.ToolFailed
			}
		}
		raw, _ := json.Marshal(map[string]any{"id": call.CallID, "type": "dynamicToolCall", "tool": call.Tool, "arguments": args, "status": status, "success": succeeded, "contentItems": content})
		events = append(events, coordination.Event{ThreadID: call.ThreadID, Native: &workerharness.Event{Kind: kind, TurnID: call.TurnID, ItemID: call.CallID, ItemType: "dynamicToolCall", Tool: &workerharness.ToolUse{Name: call.Tool, Input: string(args), Output: output, Status: toolStatus}, Raw: raw}})
	}
	for i, e := range events {
		raw, _ := json.Marshal(e)
		op := h.runAs(as, protocol.OpManagerNativeEvent, protocol.ManagerNativeEvent{Sequence: int64(20 + i), Body: raw}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeEvent})
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	env := h.env(protocol.OpManagerNativeReply, runTarget(as.RunID), protocol.ManagerNativeReply{ID: closeID, Result: json.RawMessage(`null`), LastSequence: 22}, nil)
	return closedToolFixture{h: h, as: as, close: env, resultID: id}
}

func (f closedToolFixture) execute(t *testing.T) (protocol.Operation, error) {
	t.Helper()
	return f.h.app.Execute(t.Context(), runPrincipal(f.as.RunID, f.as.Role), f.close, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply})
}

func (f closedToolFixture) original(t *testing.T) []string {
	t.Helper()
	var out []string
	if err := f.h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		rows, err := tx.Q().QueryContext(t.Context(), `SELECT body_json FROM manager_native_events WHERE run_id=? ORDER BY seq`, f.as.RunID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw string
			if err := rows.Scan(&raw); err != nil {
				return err
			}
			out = append(out, raw)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		var raw, result string
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT payload_json,result_json FROM manager_native_requests WHERE request_id=?`, f.resultID).Scan(&raw, &result); err != nil {
			return err
		}
		out = append(out, raw, result)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func reorderClosedToolEvents(t *testing.T, f closedToolFixture, order [3]int) {
	t.Helper()
	if err := f.h.store.Write(t.Context(), func(tx *store.Tx) error {
		var bodies [3]string
		for i := range bodies {
			if err := tx.Q().QueryRowContext(t.Context(), `SELECT body_json FROM manager_native_events WHERE run_id=? AND seq=?`, f.as.RunID, 20+i).Scan(&bodies[i]); err != nil {
				return err
			}
		}
		for i, source := range order {
			if _, err := tx.Exec(t.Context(), `UPDATE manager_native_events SET body_json=? WHERE run_id=? AND seq=?`, bodies[source], f.as.RunID, 20+i); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestClosedManagerRepairAcceptsStartedBeforeCallback(t *testing.T) {
	f := closedRepairToolFixture(t, true)
	reorderClosedToolEvents(t, f, [3]int{1, 0, 2})
	before := f.original(t)
	if op, err := f.execute(t); err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal("asynchronous started/callback ordering blocked original close", op, err)
	}
	if f.h.countWhere(`SELECT COUNT(*) FROM manager_native_events WHERE run_id=? AND applied=0`, f.as.RunID) != 0 || !reflect.DeepEqual(before, f.original(t)) {
		t.Fatal("asynchronous metadata was not consumed without rewriting its original bytes")
	}
	if f.h.countWhere(`SELECT COUNT(*) FROM native_turn_receipts_pending`) != 1 || f.h.countWhere(`SELECT COUNT(*) FROM logical_turns WHERE status='unknown'`) != 1 {
		t.Fatal("asynchronous metadata changed original business uncertainty")
	}
}

func TestClosedManagerRepairSkipsUnneededHistoryProof(t *testing.T) {
	for _, scenario := range []string{"ordinary_close", "applied_group"} {
		t.Run(scenario, func(t *testing.T) {
			f := closedRepairToolFixture(t, true)
			if err := f.h.store.Write(t.Context(), func(tx *store.Tx) error {
				if _, err := tx.Exec(t.Context(), `UPDATE manager_native_events SET applied=1 WHERE run_id=?`, f.as.RunID); err != nil {
					return err
				}
				// This dependency belongs only to new factual consumption. A
				// historical proof failure must not affect already applied facts.
				if _, err := tx.Exec(t.Context(), `UPDATE run_agent_executions SET pin_json='{' WHERE run_id=?`, f.as.RunID); err != nil {
					return err
				}
				if scenario == "ordinary_close" {
					_, err := tx.Exec(t.Context(), `UPDATE logical_turn_executions SET repair=0`)
					return err
				}
				_, err := tx.Exec(t.Context(), `INSERT INTO manager_native_events(run_id,seq,body_json,applied) VALUES(?,23,'{"thread_id":"native","native":{"Kind":"turn_started","TurnID":"original-turn"}}',0)`, f.as.RunID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			before := f.original(t)
			if scenario == "ordinary_close" {
				if op, err := f.execute(t); err != nil || op.Status != protocol.OpSucceeded {
					t.Fatal("ordinary close queried unnecessary historical proof", op, err)
				}
			} else if err := f.h.store.Write(t.Context(), func(tx *store.Tx) error {
				return consumeClosedManagerRepairFacts(t.Context(), &Exec{Env: f.close}, tx, f.as.RunID)
			}); err != nil {
				t.Fatal("already applied lifecycle queried unnecessary historical proof", err)
			}
			if !reflect.DeepEqual(before, f.original(t)) || f.h.countWhere(`SELECT COUNT(*) FROM events WHERE kind='run.manager_closed_tool_facts_consumed'`) != 0 {
				t.Fatal("skipped historical facts changed bytes or emitted a new audit")
			}
			if scenario == "applied_group" && f.h.countWhere(`SELECT COUNT(*) FROM manager_native_events WHERE run_id=? AND applied=0`, f.as.RunID) != 1 {
				t.Fatal("unrelated pending event was consumed")
			}
		})
	}
}

func TestClosedManagerRepairConsumesOriginalRepliedToolFacts(t *testing.T) {
	f := closedRepairToolFixture(t, true)
	before := f.original(t)
	op, err := f.execute(t)
	if err != nil || op.Status != protocol.OpSucceeded {
		t.Fatalf("original close remains blocked by already replied tool facts: %+v %v", op, err)
	}
	if f.h.countWhere(`SELECT COUNT(*) FROM manager_native_events WHERE run_id=? AND applied=0`, f.as.RunID) != 0 {
		t.Fatal("original tool metadata remains pending")
	}
	if !reflect.DeepEqual(before, f.original(t)) {
		t.Fatal("factual consumption rewrote original event or tool result bytes")
	}
	if f.h.countWhere(`SELECT COUNT(*) FROM native_turn_receipts_pending`) != 1 || f.h.countWhere(`SELECT COUNT(*) FROM logical_turns WHERE status='unknown'`) != 1 || f.h.countWhere(`SELECT COUNT(*) FROM main_turns WHERE status='interrupted'`) != 1 {
		t.Fatal("transport consumption invented business completion")
	}
	if f.h.countWhere(`SELECT COUNT(*) FROM events WHERE kind='run.manager_closed_tool_facts_consumed' AND object_id=?`, f.as.RunID) != 1 {
		t.Fatal("missing exact factual consumption audit")
	}
	reopenArchiveCombinationHarness(t, f.h)
	again, err := f.execute(t)
	if err != nil || !reflect.DeepEqual(again, op) {
		t.Fatal("same original close changed after reopen", again, err)
	}
	if f.h.countWhere(`SELECT COUNT(*) FROM events WHERE kind='run.manager_closed_tool_facts_consumed' AND object_id=?`, f.as.RunID) != 1 {
		t.Fatal("replayed close duplicated metadata consumption")
	}
	stopped := f.h.runAs(f.as, protocol.OpManagerNativeStopped, protocol.ManagerStoppedPayload{Reason: "original owned process stopped", LastSequence: 22}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeStopped})
	if stopped.Status != protocol.OpSucceeded {
		t.Fatal(stopped)
	}
	if f.h.countWhere(`SELECT COUNT(*) FROM native_turn_receipts_pending`) != 1 {
		t.Fatal("stopped transport changed original pending business")
	}
}

func TestClosedManagerRepairPreservesFailedToolResultAndJSONValues(t *testing.T) {
	f := closedRepairToolFixture(t, false)
	if err := f.h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE manager_native_events SET body_json=json_set(body_json,'$.native.Raw.arguments',json('{"revision":7.0}'),'$.native.Tool.Input',' { "revision" : 7e0 } ') WHERE run_id=? AND seq IN (21,22)`, f.as.RunID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	before := f.original(t)
	if op, err := f.execute(t); err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal("answered failed tool or serializer-only JSON change was rejected", op, err)
	}
	if !reflect.DeepEqual(before, f.original(t)) {
		t.Fatal("failed original tool result was reinterpreted or rewritten")
	}
	var audit struct {
		CloseRequestID   string                   `json:"close_request_id"`
		ReceiptRequestID string                   `json:"receipt_request_id"`
		Tools            []closedManagerToolProof `json:"tools"`
	}
	if err := f.h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var raw string
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT body_json FROM events WHERE kind='run.manager_closed_tool_facts_consumed' AND object_id=?`, f.as.RunID).Scan(&raw); err != nil {
			return err
		}
		return json.Unmarshal([]byte(raw), &audit)
	}); err != nil {
		t.Fatal(err)
	}
	var close protocol.ManagerNativeReply
	_ = json.Unmarshal(f.close.Payload, &close)
	if audit.CloseRequestID != close.ID || audit.ReceiptRequestID != f.close.RequestID || audit.CloseRequestID == audit.ReceiptRequestID || len(audit.Tools) != 1 || audit.Tools[0].ToolResultRequestID != f.resultID || len(audit.Tools[0].Facts) != 3 {
		t.Fatal("audit confused native close, receipt or original tool identities", audit)
	}
}

func TestClosedManagerRepairRejectsUnprovenFactsAtomically(t *testing.T) {
	for _, scenario := range []string{"ordinary_turn", "changed_pin", "missing_result", "started_result", "unknown_result", "wrong_result_id", "rejected_close", "wrong_cursor", "unsupported_event", "audit_failure", "callback_after_completion", "completed_before_started"} {
		t.Run(scenario, func(t *testing.T) {
			f := closedRepairToolFixture(t, true)
			wantPending := 3
			if scenario == "callback_after_completion" {
				reorderClosedToolEvents(t, f, [3]int{1, 2, 0})
			} else if scenario == "completed_before_started" {
				reorderClosedToolEvents(t, f, [3]int{0, 2, 1})
			} else if scenario == "wrong_cursor" || scenario == "rejected_close" {
				var close protocol.ManagerNativeReply
				_ = json.Unmarshal(f.close.Payload, &close)
				if scenario == "wrong_cursor" {
					close.LastSequence--
				} else {
					close.Rejected = true
				}
				f.close.Payload, _ = json.Marshal(close)
			} else if scenario == "unsupported_event" {
				raw := json.RawMessage(`{"thread_id":"native","native":{"Kind":"turn_started","TurnID":"original-turn"}}`)
				f.h.runAs(f.as, protocol.OpManagerNativeEvent, protocol.ManagerNativeEvent{Sequence: 23, Body: raw}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeEvent})
				var close protocol.ManagerNativeReply
				_ = json.Unmarshal(f.close.Payload, &close)
				close.LastSequence = 23
				f.close.Payload, _ = json.Marshal(close)
				wantPending = 4
			} else if err := f.h.store.Write(t.Context(), func(tx *store.Tx) error {
				query := map[string]string{
					"ordinary_turn":   `UPDATE logical_turn_executions SET repair=0`,
					"changed_pin":     `UPDATE issue_manager_tasks SET pin_json=json_set(pin_json,'$.agent_revision',99)`,
					"missing_result":  `UPDATE conversation_tool_uses SET native_delivery_id='' WHERE native_delivery_id<>''`,
					"started_result":  `UPDATE conversation_tool_uses SET native_delivery_status='produced' WHERE native_delivery_id<>''`,
					"unknown_result":  `UPDATE conversation_tool_uses SET native_delivery_status='unknown' WHERE native_delivery_id<>''`,
					"wrong_result_id": `UPDATE conversation_tool_uses SET native_run_id='foreign-run' WHERE native_delivery_id<>''`,
					"audit_failure":   `CREATE TRIGGER fail_closed_fact_audit BEFORE INSERT ON events WHEN NEW.kind='run.manager_closed_tool_facts_consumed' BEGIN SELECT RAISE(ABORT,'injected factual audit failure'); END`,
				}[scenario]
				_, err := tx.Exec(t.Context(), query)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if op, err := f.execute(t); err == nil && op.Status == protocol.OpSucceeded {
				t.Fatal("unproven metadata passed the original close", op)
			}
			if got := f.h.countWhere(`SELECT COUNT(*) FROM manager_native_events WHERE run_id=? AND applied=0`, f.as.RunID); got != wantPending {
				t.Fatal("failed close partially consumed its original slice", got, wantPending)
			}
			if f.h.countWhere(`SELECT COUNT(*) FROM events WHERE kind='run.manager_closed_tool_facts_consumed'`) != 0 || f.h.countWhere(`SELECT COUNT(*) FROM native_turn_receipts_pending`) != 1 {
				t.Fatal("failed close committed an audit or changed business uncertainty")
			}
		})
	}
}

func TestClosedManagerToolMetadataMatcherRetainsIdentityAndProjection(t *testing.T) {
	call := closedManagerCall{ThreadID: "thread", TurnID: "turn", CallID: "call", Tool: "original_tool", Arguments: json.RawMessage(`{"a":9007199254740993,"b":2}`)}
	makeEvent := func() closedManagerEvent {
		var e closedManagerEvent
		raw := fmt.Sprintf(`{"thread_id":"thread","native":{"Kind":"item_completed","TurnID":"turn","ItemID":"call","ItemType":"dynamicToolCall","Tool":{"Name":"original_tool","Input":%q,"Output":"original answer","Status":"completed"},"Raw":{"id":"call","type":"dynamicToolCall","tool":"original_tool","arguments":{"b":2.0,"a":9007199254740993},"status":"completed","success":true,"contentItems":[{"type":"inputText","text":"original answer"}]}}}`, string(call.Arguments))
		if err := json.Unmarshal([]byte(raw), &e); err != nil {
			t.Fatal(err)
		}
		return e
	}
	if !closedItemMatches(makeEvent(), call, true, "original answer") {
		t.Fatal("exact decimal values or reordered object changed original arguments")
	}
	for _, scenario := range []string{"thread", "turn", "item", "type", "tool", "input", "raw_id", "raw_type", "raw_tool", "raw_arguments", "raw_status", "raw_success", "raw_output", "projection_output", "projection_status", "extra_call", "usage", "failure", "turn_status", "agent_text"} {
		t.Run(scenario, func(t *testing.T) {
			e := makeEvent()
			switch scenario {
			case "thread":
				e.ThreadID = "foreign"
			case "turn":
				e.Native.TurnID = "foreign"
			case "item":
				e.Native.ItemID = "foreign"
			case "type":
				e.Native.ItemType = "commandExecution"
			case "tool":
				e.Native.Tool.Name = "foreign"
			case "input":
				e.Native.Tool.Input = `{"a":9007199254740992,"b":2}`
			case "projection_output":
				e.Native.Tool.Output = "changed"
			case "projection_status":
				e.Native.Tool.Status = protocol.ToolRunning
			case "extra_call":
				e.Call = &call
			case "usage":
				e.Native.RequestUsage = json.RawMessage(`{}`)
			case "failure":
				e.Native.Failure = json.RawMessage(`{}`)
			case "turn_status":
				e.Native.TurnStatus = "completed"
			case "agent_text":
				e.Native.Text = "unpublished text"
			default:
				var raw map[string]any
				decoder := json.NewDecoder(bytes.NewReader(e.Native.Raw))
				decoder.UseNumber()
				_ = decoder.Decode(&raw)
				switch scenario {
				case "raw_id":
					raw["id"] = "foreign"
				case "raw_type":
					raw["type"] = "mcpToolCall"
				case "raw_tool":
					raw["tool"] = "foreign"
				case "raw_arguments":
					raw["arguments"] = json.RawMessage(`{"a":9007199254740992,"b":2}`)
				case "raw_status":
					raw["status"] = "inProgress"
				case "raw_success":
					raw["success"] = false
				case "raw_output":
					raw["contentItems"] = []map[string]string{{"type": "inputText", "text": "changed"}}
				}
				e.Native.Raw, _ = json.Marshal(raw)
			}
			if closedItemMatches(e, call, true, "original answer") {
				t.Fatal("unproven original item projection was consumed")
			}
		})
	}
	e := makeEvent()
	call.Arguments = json.RawMessage(`null`)
	e.Native.Tool.Input = ""
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(e.Native.Raw, &raw)
	raw["arguments"] = json.RawMessage(`null`)
	e.Native.Raw, _ = json.Marshal(raw)
	if !closedItemMatches(e, call, true, "original answer") {
		t.Fatal("native compact(null) empty input changed a legitimate call")
	}
}
