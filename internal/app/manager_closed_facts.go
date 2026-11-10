package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math/big"
	"reflect"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type closedManagerCall struct {
	ThreadID, TurnID, CallID, Namespace, Tool string
	Arguments                                 json.RawMessage
}

type closedManagerItem struct {
	Kind, TurnID, ItemID, ItemType, Text, TurnStatus string
	Tool                                             *struct {
		Name, Input, Output string
		Status              protocol.ToolStatus
	}
	Raw                                 json.RawMessage
	RequestUsage, ContextUsage, Failure json.RawMessage
}

type closedManagerEvent struct {
	ThreadID     string `json:"thread_id"`
	Disconnected bool
	Call         *closedManagerCall
	Native       *closedManagerItem
}

type closedManagerFact struct {
	Sequence int64  `json:"sequence"`
	SHA256   string `json:"sha256"`
}

type closedManagerToolProof struct {
	ThreadID              string              `json:"thread_id"`
	TurnID                string              `json:"turn_id"`
	CallID                string              `json:"call_id"`
	RepairRequestID       string              `json:"repair_request_id"`
	ToolResultRequestID   string              `json:"tool_result_request_id"`
	ToolResultSHA256      string              `json:"tool_result_sha256"`
	ToolResultReplySHA256 string              `json:"tool_result_reply_sha256"`
	Facts                 []closedManagerFact `json:"facts"`
}

func closedFactHash(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

// JSON values are compared without float64 conversion. Different whitespace,
// object order and exact decimal spellings do not change the original values.
func closedFactJSONEqual(a, b []byte) bool {
	decode := func(raw []byte) (any, bool) {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		var v any
		if d.Decode(&v) != nil {
			return nil, false
		}
		return v, d.Decode(new(any)) == io.EOF
	}
	x, ok := decode(a)
	if !ok {
		return false
	}
	y, ok := decode(b)
	return ok && closedFactValueEqual(x, y)
}

func closedFactValueEqual(a, b any) bool {
	switch x := a.(type) {
	case json.Number:
		y, ok := b.(json.Number)
		if !ok {
			return false
		}
		xr, xok := new(big.Rat).SetString(string(x))
		yr, yok := new(big.Rat).SetString(string(y))
		return xok && yok && xr.Cmp(yr) == 0
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, v := range x {
			other, found := y[k]
			if !found || !closedFactValueEqual(v, other) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i, v := range x {
			if !closedFactValueEqual(v, y[i]) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(a, b)
	}
}

func closedRepairIdentity(ctx context.Context, q store.Querier, run, thread, turn string) (string, error) {
	r, err := store.GetRun(ctx, q, run)
	if err != nil {
		return "", err
	}
	m, err := store.GetIssueManagerTask(ctx, q, r.TaskID)
	if err != nil {
		return "", err
	}
	pin, err := store.GetRunExecution(ctx, q, run)
	if err != nil {
		return "", err
	}
	if r.Role != protocol.RoleIssueManager || pin == nil || m.Pin == nil || !reflect.DeepEqual(pin, m.Pin) || pin.Validate(r.HostID) != nil || m.HostID != r.HostID {
		return "", nil
	}
	ids, err := store.FollowupIDs(ctx, q, `SELECT h.execution_id FROM hosted_executions h JOIN main_turns t ON t.request_id=h.execution_id JOIN logical_turn_executions e ON e.request_id=h.execution_id WHERE json_extract(h.private_snapshot,'$.runner_run_id')=? AND t.thread_id=? AND t.turn_id=? AND e.repair=1 ORDER BY h.execution_id`, run, thread, turn)
	if err != nil || len(ids) != 1 {
		return "", err
	}
	h, err := store.GetHostedExecution(ctx, q, ids[0])
	if err != nil {
		return "", err
	}
	s := h.Selection
	if h.ConversationID != m.ConversationID || h.IssueID != m.IssueID || h.AssignmentEpoch != m.AssignmentEpoch || s.IssueID != m.IssueID || s.AssignmentEpoch != m.AssignmentEpoch || s.Purpose != "issue_manager" || !s.RunnerManaged || s.RunnerRunID != run || s.Identity != pin.Identity || s.ConfigurationHash != pin.PublicSHA256 || s.Definition.LocalID != pin.Identity.LocalID || s.Definition.RuntimeID != pin.Runtime.RuntimeID || s.Definition.Revision != pin.AgentRevision || s.Definition.Model != pin.Profile.Model || s.Definition.Effort != pin.Profile.Effort || s.Runtime.ID != pin.Runtime.RuntimeID || s.Runtime.Revision != pin.Runtime.Revision {
		return "", nil
	}
	return ids[0], nil
}

func closedToolProof(ctx context.Context, q store.Querier, run string, call closedManagerCall) (*closedManagerToolProof, *bool, *string, error) {
	repair, err := closedRepairIdentity(ctx, q, run, call.ThreadID, call.TurnID)
	if err != nil || repair == "" {
		return nil, nil, nil, err
	}
	rows, err := q.QueryContext(ctx, `SELECT request_id,payload_json,result_json,status FROM manager_native_requests WHERE run_id=? AND method='tool_result' AND json_extract(payload_json,'$.thread_id')=? AND json_extract(payload_json,'$.turn_id')=? AND json_extract(payload_json,'$.call_id')=? ORDER BY rowid`, run, call.ThreadID, call.TurnID, call.CallID)
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()
	var proof *closedManagerToolProof
	var success *bool
	var text *string
	for rows.Next() {
		if proof != nil {
			return nil, nil, nil, nil
		}
		var id, raw, status string
		var result *string
		if err := rows.Scan(&id, &raw, &result, &status); err != nil {
			return nil, nil, nil, err
		}
		raw, err = store.ResolveJSON(ctx, q, raw)
		if err != nil {
			return nil, nil, nil, err
		}
		var request struct {
			ThreadID   string `json:"thread_id"`
			TurnID     string `json:"turn_id"`
			CallID     string `json:"call_id"`
			ToolResult *struct {
				Success *bool
				Text    *string
			} `json:"tool_result"`
		}
		var reply protocol.ManagerNativeReply
		if result != nil {
			original, err := store.ResolveJSON(ctx, q, *result)
			if err != nil {
				return nil, nil, nil, err
			}
			result = &original
		}
		if status != "completed" || result == nil || json.Unmarshal([]byte(raw), &request) != nil || request.ToolResult == nil || request.ToolResult.Success == nil || request.ToolResult.Text == nil || request.ThreadID != call.ThreadID || request.TurnID != call.TurnID || request.CallID != call.CallID || json.Unmarshal([]byte(*result), &reply) != nil || reply.ID != id || reply.Error != "" || reply.Rejected {
			return nil, nil, nil, nil
		}
		proof = &closedManagerToolProof{ThreadID: call.ThreadID, TurnID: call.TurnID, CallID: call.CallID, RepairRequestID: repair, ToolResultRequestID: id, ToolResultSHA256: closedFactHash([]byte(raw)), ToolResultReplySHA256: closedFactHash([]byte(*result))}
		success, text = request.ToolResult.Success, request.ToolResult.Text
	}
	return proof, success, text, rows.Err()
}

func closedItemMatches(e closedManagerEvent, call closedManagerCall, success bool, text string) bool {
	n := e.Native
	if e.Disconnected || e.Call != nil || n == nil || e.ThreadID != call.ThreadID || n.TurnID != call.TurnID || n.ItemID != call.CallID || n.ItemType != "dynamicToolCall" || n.Tool == nil || n.Text != "" || n.TurnStatus != "" || presentClosedFact(n.RequestUsage) || presentClosedFact(n.ContextUsage) || presentClosedFact(n.Failure) {
		return false
	}
	var raw struct {
		ID, Type, Tool, Status string
		Arguments              json.RawMessage
		Success                *bool
		ContentItems           []struct{ Type, Text string }
	}
	inputMatches := closedFactJSONEqual([]byte(n.Tool.Input), call.Arguments) || n.Tool.Input == "" && bytes.Equal(bytes.TrimSpace(call.Arguments), []byte("null"))
	if json.Unmarshal(n.Raw, &raw) != nil || raw.ID != call.CallID || raw.Type != "dynamicToolCall" || raw.Tool != call.Tool || n.Tool.Name != call.Tool || !closedFactJSONEqual(raw.Arguments, call.Arguments) || !inputMatches {
		return false
	}
	if n.Kind == "item_started" {
		return raw.Status == "inProgress" && raw.Success == nil && len(raw.ContentItems) == 0 && n.Tool.Status == protocol.ToolRunning && n.Tool.Output == ""
	}
	if n.Kind != "item_completed" || raw.Status != "completed" || raw.Success == nil || *raw.Success != success {
		return false
	}
	var parts []string
	for _, c := range raw.ContentItems {
		if c.Type != "inputText" {
			return false
		}
		parts = append(parts, c.Text)
	}
	wanted := protocol.ToolCompleted
	if !success {
		wanted = protocol.ToolFailed
	}
	return strings.Join(parts, "\n") == text && n.Tool.Output == text && n.Tool.Status == wanted
}

func presentClosedFact(raw json.RawMessage) bool {
	return len(raw) != 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// The caller already verified this exact native close covers the current event
// cursor. Only original, replied internal-repair tools can consume metadata here.
// Unsupported events remain unapplied and retain the existing close guard.
func consumeClosedManagerRepairFacts(ctx context.Context, x *Exec, tx *store.Tx, run string) error {
	var close protocol.ManagerNativeReply
	if json.Unmarshal(x.Env.Payload, &close) != nil || close.ID == "" || close.Error != "" || close.Rejected {
		return nil
	}
	rows, err := tx.Q().QueryContext(ctx, `SELECT seq,body_json,applied FROM manager_native_events WHERE run_id=? ORDER BY seq`, run)
	if err != nil {
		return err
	}
	type row struct {
		seq     int64
		raw     string
		applied bool
		e       closedManagerEvent
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.seq, &r.raw, &r.applied); err != nil {
			rows.Close()
			return err
		}
		r.raw, err = store.ResolveJSON(ctx, tx.Q(), r.raw)
		if err != nil {
			rows.Close()
			return err
		}
		if json.Unmarshal([]byte(r.raw), &r.e) != nil {
			rows.Close()
			return nil
		}
		all = append(all, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var audits []closedManagerToolProof
	callCounts := map[[3]string]int{}
	itemGroups := map[[3]string][]row{}
	for _, r := range all {
		if r.e.Call != nil {
			call := r.e.Call
			callCounts[[3]string{call.ThreadID, call.TurnID, call.CallID}]++
		}
		if native := r.e.Native; native != nil {
			key := [3]string{r.e.ThreadID, native.TurnID, native.ItemID}
			itemGroups[key] = append(itemGroups[key], r)
		}
	}
	for _, r := range all {
		if r.e.Call == nil || r.e.Native != nil || r.e.Disconnected {
			continue
		}
		call := *r.e.Call
		if call.ThreadID == "" || call.TurnID == "" || call.CallID == "" || call.Tool == "" || r.e.ThreadID != call.ThreadID || !json.Valid(call.Arguments) {
			continue
		}
		key := [3]string{call.ThreadID, call.TurnID, call.CallID}
		items := itemGroups[key]
		// Started notifications may precede the callback, but completing the
		// native tool still requires that callback's original replied result.
		if callCounts[key] != 1 || len(items) != 2 || items[0].e.Native.Kind != "item_started" || items[1].e.Native.Kind != "item_completed" || items[1].seq <= r.seq || items[0].applied && items[1].applied {
			continue
		}
		proof, success, text, err := closedToolProof(ctx, tx.Q(), run, call)
		if err != nil {
			return err
		}
		if proof == nil {
			continue
		}
		if !closedItemMatches(items[0].e, call, *success, *text) || !closedItemMatches(items[1].e, call, *success, *text) {
			continue
		}
		for _, fact := range append([]row{r}, items...) {
			proof.Facts = append(proof.Facts, closedManagerFact{Sequence: fact.seq, SHA256: closedFactHash([]byte(fact.raw))})
			if _, err := tx.Exec(ctx, `UPDATE manager_native_events SET applied=1 WHERE run_id=? AND seq=? AND body_json=?`, run, fact.seq, fact.raw); err != nil {
				return err
			}
		}
		audits = append(audits, *proof)
	}
	if len(audits) == 0 {
		return nil
	}
	raw, err := json.Marshal(map[string]any{"close_request_id": close.ID, "receipt_request_id": x.Env.RequestID, "tools": audits})
	if err != nil {
		return err
	}
	_, err = tx.InsertEvent(ctx, store.EventRow{EventID: x.IDs.New(protocol.PrefixEvent), Source: "app", ObjectType: "run", ObjectID: run, Kind: "run.manager_closed_tool_facts_consumed", BodyJSON: string(raw), RequestID: x.Env.RequestID, CreatedAt: x.Now.UnixMilli()})
	return err
}
