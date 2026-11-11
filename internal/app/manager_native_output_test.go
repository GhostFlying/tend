package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func seedManagerToolOutput(t *testing.T, h *harness, as protocol.Assignment, tool, input, output string, native ...string) {
	t.Helper()
	turn, item := "turn", "call"
	if len(native) == 2 {
		turn, item = native[0], native[1]
	}
	ctx, err := h.store.StageOriginals(t.Context(), "native-tool-original", []byte(input), []byte(output))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		id := h.ids.New(protocol.PrefixMessage)
		if _, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: id, ConversationID: as.Manager.ConversationID, Author: "agent", Kind: "tool_use", CreatedAt: h.clock.Now().UnixMilli()}); err != nil {
			return err
		}
		return tx.PutToolUse(ctx, store.ToolUseRow{MessageID: id, ThreadID: "native", TurnID: turn, ItemID: item, Tool: tool, Input: input, Output: output, Status: "completed"})
	}); err != nil {
		t.Fatal(err)
	}
}

func TestManagerDeliveredToolResultSharesOnlyEquivalentOutput(t *testing.T) {
	padding, _ := json.Marshal(strings.Repeat("x", 2048))
	original := `{"value":` + string(padding) + `,"phase":"final","count":9007199254740993}`
	equivalent := " {\n \"count\":9007199254740993,\"phase\":\"final\",\"value\":" + string(padding) + " } "
	for _, mode := range []string{"exact", "json_format", "produced", "phase_difference"} {
		t.Run(mode, func(t *testing.T) {
			h, _, _, as, _ := reviewManagerAssignment(t)
			seedManagerToolOutput(t, h, as, "inspect_original", `{}`, original)
			text, delivered := equivalent, true
			switch mode {
			case "exact":
				text = original
			case "produced":
				delivered = false
			case "phase_difference":
				text = strings.Replace(equivalent, `"final"`, `"waiting"`, 1)
			}
			payload := mustJSON(t, map[string]any{"thread_id": "native", "turn_id": "turn", "call_id": "call", "tool_result": map[string]any{"Success": true, "Text": text}})
			ctx, err := h.store.StageOriginals(t.Context(), "pending-callback", []byte(text))
			if err != nil {
				t.Fatal(err)
			}
			if err := h.store.Write(ctx, func(tx *store.Tx) error {
				return tx.PutManagerToolResult(ctx, as.RunID, "actual-command", string(payload), delivered, h.clock.Now().UnixMilli())
			}); err != nil {
				t.Fatal(err)
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				var output, native string
				if err := tx.Q().QueryRowContext(t.Context(), `SELECT output,native_result_output FROM conversation_tool_uses WHERE turn_id='turn' AND item_id='call'`).Scan(&output, &native); err != nil {
					return err
				}
				shared := mode == "exact" || mode == "json_format"
				if (output == native) != shared {
					t.Fatal("callback did not preserve the required original/reference distinction", mode, output, native)
				}
				full, err := store.ResolveJSON(t.Context(), tx.Q(), native)
				if err == nil && (shared && full != original || !shared && full != text) {
					t.Fatal("native result changed actual text or provider representation")
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func rejectedManagerTaskRead(t *testing.T) (*harness, protocol.Assignment, protocol.Assignment, string) {
	t.Helper()
	h, runtime, issue, as, _ := reviewManagerAssignment(t)
	op := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, canonicalOwnerTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Original read target", Spec: testSpec}, rev(issue.Revision)), CanonicalOwnerTaskCreate{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var created struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(op.Result, &created); err != nil || created.TaskID == "" {
		t.Fatal(op, err)
	}
	if paused := h.exec(owner, h.env("control.set_mode", taskTarget(created.TaskID), protocol.SetControlModePayload{Mode: protocol.ControlPaused}, rev(h.task(created.TaskID).ControlRevision)), SetControlMode{}); paused.Status != protocol.OpSucceeded {
		t.Fatal(paused)
	}
	var detail protocol.TaskDetail
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		detail, err = views.Task(t.Context(), tx.Q(), created.TaskID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	input := string(mustJSON(t, map[string]any{"task_id": created.TaskID}))
	seedManagerToolOutput(t, h, as, "tasks_read", input, "")
	text := string(mustJSON(t, map[string]any{"ok": true, "result": detail}))
	payload := mustJSON(t, map[string]any{"thread_id": "native", "turn_id": "turn", "call_id": "call", "tool_result": map[string]any{"Success": true, "Text": text}})
	id, err := h.app.QueueManagerNative(t.Context(), as.RunID, "tool_result", payload)
	if err != nil {
		t.Fatal(err)
	}
	h.runAs(as, protocol.OpManagerNativeClaim, protocol.ManagerNativeClaimPayload{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim})
	if op := h.runAs(as, protocol.OpManagerNativeReply, protocol.ManagerNativeReply{ID: id, Rejected: true, Error: "original native turn is closed"}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if _, ready, err := h.app.ManagerNativeResult(t.Context(), as.RunID, id); err != nil || !ready {
		t.Fatal(ready, err)
	}
	if h.countWhere(`SELECT COUNT(*) FROM conversation_tool_uses WHERE native_delivery_status=? AND native_result_output=''`, store.NativeDeliveryRejectedCurrentTask) != 1 {
		t.Fatal("definitely undelivered ordinary read retained a response snapshot")
	}
	call := map[string]any{"ThreadID": "native", "TurnID": "turn", "CallID": "call", "Tool": "tasks_read", "Arguments": json.RawMessage(input)}
	usage := json.RawMessage(`{"thread_id":"native","native":{"Kind":"request_usage"}}`)
	h.runAs(as, protocol.OpManagerNativeEvent, protocol.ManagerNativeEvent{Sequence: 1, Body: usage}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeEvent})
	body := mustJSON(t, map[string]any{"thread_id": "native", "call": call})
	h.runAs(as, protocol.OpManagerNativeEvent, protocol.ManagerNativeEvent{Sequence: 2, Body: body}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeEvent})
	h.runAs(as, protocol.OpManagerNativeStopped, protocol.ManagerStoppedPayload{Reason: "original process exited", LastSequence: 2}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeStopped})
	reader := reviewClaimManager(t, h, runtime)
	if reader.TaskID != as.TaskID {
		t.Fatal("recovery changed original responsibility", reader)
	}
	return h, as, reader, created.TaskID
}

func TestRejectedNativeTaskReadRebuildsCurrentScopedBusinessView(t *testing.T) {
	h, origin, reader, target := rejectedManagerTaskRead(t)
	if op := h.exec(owner, h.env("tasks.rate", taskTarget(target), protocol.RateTaskPayload{Rating: protocol.RatingP1}, rev(h.task(target).ControlRevision)), RateTask{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	before, actions, events := h.task(target), h.count("plan_actions"), h.count("events")
	for range 2 {
		raw, err := h.app.ManagerRetainedToolResult(t.Context(), reader.RunID, origin.RunID, "native", "turn", "call")
		if err != nil {
			t.Fatal(err)
		}
		var envelope struct {
			Success bool
			Text    string
		}
		var result struct {
			OK               bool                `json:"ok"`
			Result           protocol.TaskDetail `json:"result"`
			Observation      string              `json:"observation"`
			OriginalDelivery string              `json:"original_delivery"`
		}
		if json.Unmarshal(raw, &envelope) != nil || json.Unmarshal([]byte(envelope.Text), &result) != nil || !envelope.Success || !result.OK || result.Observation != "current" || result.OriginalDelivery != "rejected" || result.Result.TaskID != target || result.Result.Rating != protocol.RatingP1 {
			t.Fatal("rebuilt read claimed a historical snapshot or lost current state", string(raw))
		}
	}
	if h.task(target) != before || h.count("plan_actions") != actions || h.count("events") != events {
		t.Fatal("current read replay performed a business mutation")
	}
	issue := canonicalTaskIssue(t, h, reader.Manager.IssueID)
	replacement := protocol.RequestIssueManagerPayload{CategoryID: protocol.DefaultIssueManagerCategory, Replace: true, ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Transfer the original continuing responsibility"}
	if op := h.exec(owner, h.env(protocol.OpIssueRequestOwner, protocol.Target{Type: "issue", ID: issue.IssueID}, replacement, rev(issue.Revision)), RequestIssueOwner{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if _, err := h.app.ManagerRetainedToolResult(t.Context(), reader.RunID, origin.RunID, "native", "turn", "call"); protocol.CodeOf(err) != protocol.CodeForbidden && protocol.CodeOf(err) != protocol.CodeNotFound {
		t.Fatal("historical Run recovered read authority after reassignment", err)
	}
}

func TestManagerToolTextEquivalencePreservesActualValues(t *testing.T) {
	for _, pair := range [][2]string{
		{`{"value":null}`, `{}`}, {`{"value":null}`, `{"value":[]}`},
		{`{"value":"waiting"}`, `{"value":"completed"}`},
		{`{"value":9007199254740993}`, `{"value":9007199254740992}`},
		{`{"value":1}`, `{"value":1.0}`}, {`{"value":1}`, `{"value":1} {}`},
	} {
		if store.SemanticJSONEqual(pair[0], pair[1]) || sameClosedToolText(pair[0], pair[1]) {
			t.Fatal("different business values were folded", pair)
		}
	}
}

func TestUncertainAndUniqueNativeReadResultsKeepExactText(t *testing.T) {
	for _, kind := range []string{"produced", "unknown", "rejected_unique_text", "rejected_other_tool"} {
		t.Run(kind, func(t *testing.T) {
			h, _, _, as, _ := reviewManagerAssignment(t)
			tool := "tasks_read"
			if kind == "rejected_other_tool" {
				tool = "unclassified_tool"
			}
			input := string(mustJSON(t, map[string]any{"task_id": as.TaskID}))
			seedManagerToolOutput(t, h, as, tool, input, "")
			text := string(mustJSON(t, map[string]any{"ok": true, "result": map[string]any{"task_id": as.TaskID}}))
			if kind == "rejected_unique_text" {
				text = "Independent text which is not a current business view"
			}
			payload := mustJSON(t, map[string]any{"thread_id": "native", "turn_id": "turn", "call_id": "call", "tool_result": map[string]any{"Success": true, "Text": text}})
			id, err := h.app.QueueManagerNative(t.Context(), as.RunID, "tool_result", payload)
			if err != nil {
				t.Fatal(err)
			}
			if kind != "produced" {
				h.runAs(as, protocol.OpManagerNativeClaim, protocol.ManagerNativeClaimPayload{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim})
				reply := protocol.ManagerNativeReply{ID: id, Error: "original outcome is unresolved", Rejected: kind != "unknown"}
				if op := h.runAs(as, protocol.OpManagerNativeReply, reply, ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				var raw, state string
				if err := tx.Q().QueryRowContext(t.Context(), `SELECT native_result_output,native_delivery_status FROM conversation_tool_uses WHERE turn_id='turn' AND item_id='call'`).Scan(&raw, &state); err != nil {
					return err
				}
				full, err := store.ResolveJSON(t.Context(), tx.Q(), raw)
				if err == nil && (full != text || state == store.NativeDeliveryRejectedCurrentTask) {
					t.Fatal("uncertain or unique original result was retired", full, state)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestClosedManagerRepairUsesSharedJSONResultAndOriginalUploadHashes(t *testing.T) {
	original := `{"phase":"done","items":[null],"count":9007199254740993}`
	formatted := " { \"items\" : [null], \"count\" : 9007199254740993, \"phase\" : \"done\" } "
	f := closedRepairToolFixture(t, true, formatted, original)
	before := f.original(t)
	if op, err := f.execute(t); err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal("shared JSON representation blocked original close", op, err)
	}
	if err := f.h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var raw string
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT body_json FROM events WHERE object_id=? AND kind='run.manager_closed_tool_facts_consumed'`, f.as.RunID).Scan(&raw); err != nil {
			return err
		}
		var audit struct {
			Tools []closedManagerToolProof `json:"tools"`
		}
		if err := store.DecodeJSON(t.Context(), tx.Q(), raw, &audit); err != nil {
			return err
		}
		if len(audit.Tools) != 1 || len(audit.Tools[0].Facts) != 3 {
			t.Fatal("original callback evidence is missing", audit)
		}
		for i, fact := range audit.Tools[0].Facts {
			stored := before[19+i]
			want, derived := store.EventOriginalSHA256(stored)
			if !derived {
				full, err := store.ResolveJSON(t.Context(), tx.Q(), stored)
				if err != nil {
					return err
				}
				want = closedFactHash([]byte(full))
			}
			if fact.Sequence != int64(20+i) || fact.SHA256 != want {
				t.Fatal("shared result changed original upload hash or order", fact, want)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
