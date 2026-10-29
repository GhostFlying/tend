package orca

import (
	"encoding/json"
	"strconv"
	"strings"
)

// These are durable native execution receipts, not the pane's latest state.
// Operations outlive an RPC response and may also outlive a managed turn.
type codexCompletedItem struct {
	Type        string                     `json:"type"`
	ID          string                     `json:"id"`
	Status      string                     `json:"status"`
	Sender      string                     `json:"sender_thread_id"`
	Receivers   []string                   `json:"receiver_thread_ids"`
	AgentStates map[string]json.RawMessage `json:"agents_states"`
	ProcessID   string                     `json:"process_id"`
}

type codexNativeCall struct{ name, process string }
type codexOperations struct {
	calls        map[string]codexNativeCall
	outputs      map[string]bool
	children     map[string]string
	processes    map[string]string
	processCalls map[string]string
	unknown      map[string]bool
}

func newCodexOperations() *codexOperations {
	return &codexOperations{calls: map[string]codexNativeCall{}, outputs: map[string]bool{}, children: map[string]string{}, processes: map[string]string{}, processCalls: map[string]string{}, unknown: map[string]bool{}}
}
func (o *codexOperations) call(id, name string, args json.RawMessage) {
	name = strings.TrimPrefix(name, "functions.")
	c := codexNativeCall{name: name}
	if name == "write_stdin" {
		var p struct {
			SessionID json.RawMessage `json:"session_id"`
		}
		// Function arguments are themselves a JSON string in response_item records.
		var text string
		if json.Unmarshal(args, &text) == nil {
			args = []byte(text)
		}
		if json.Unmarshal(args, &p) == nil {
			c.process = codexProcessID(p.SessionID)
		}
	}
	o.calls[id] = c
}
func codexProcessID(raw json.RawMessage) string {
	var id string
	if json.Unmarshal(raw, &id) == nil {
		return id
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String()
	}
	return ""
}
func (o *codexOperations) output(id string, raw json.RawMessage) {
	if o.outputs[id] {
		return
	}
	o.outputs[id] = true
	c := o.calls[id]
	switch c.name {
	case "spawn_agent":
		var text string
		var result struct {
			AgentID string `json:"agent_id"`
		}
		if json.Unmarshal(raw, &text) == nil && json.Unmarshal([]byte(text), &result) == nil && result.AgentID != "" {
			if _, observed := o.children[result.AgentID]; !observed {
				o.children[result.AgentID] = "pending"
			}
		} else {
			o.unknown[id] = true
		}
	case "exec_command", "write_stdin":
		var text string
		if json.Unmarshal(raw, &text) != nil {
			o.unknown[id] = true
			return
		}
		process, exited := codexExecReceipt(text)
		switch {
		case process != "":
			if o.processes[process] == "settled" && (o.processCalls[process] == id || c.name == "write_stdin") {
				// A delayed yield receipt cannot reopen the same completed command.
				return
			}
			o.processCalls[process] = id
			o.processes[process] = "pending"
			c.process = process
			o.calls[id] = c
		case exited:
			if c.process != "" {
				o.processes[c.process] = "settled"
			}
		default:
			o.unknown[id] = true
		}
	}
}

// Only the execution host's receipt header is evidence. Command stdout below
// Output: cannot announce a process start or end, even if it repeats this text.
func codexExecReceipt(text string) (process string, exited bool) {
	for line := range strings.SplitSeq(text, "\n") {
		if line == "Output:" {
			break
		}
		if id, ok := strings.CutPrefix(line, "Process running with session ID "); ok {
			if n, err := strconv.ParseInt(id, 10, 64); err == nil && n > 0 {
				process = id
			}
		}
		if code, ok := strings.CutPrefix(line, "Process exited with code "); ok {
			if _, err := strconv.Atoi(code); err == nil {
				exited = true
			}
		}
	}
	return process, exited
}
func (o *codexOperations) completed(item codexCompletedItem, session string) {
	switch item.Type {
	case "CollabAgentToolCall":
		if item.Sender != session {
			return
		}
		if item.Status == "completed" || item.Status == "failed" || item.Status == "interrupted" {
			delete(o.unknown, item.ID)
		}
		for _, id := range item.Receivers {
			if id == "" {
				continue
			}
			o.children[id] = codexChildState(item.AgentStates[id])
		}
	case "CommandExecution":
		process := item.ProcessID
		if process == "" {
			process = o.calls[item.ID].process
		}
		if process != "" && o.processCalls[process] == "" {
			o.processCalls[process] = item.ID
		}
		switch item.Status {
		case "completed", "failed", "declined":
			delete(o.unknown, item.ID)
			if process != "" {
				o.processes[process] = "settled"
			}
		default:
			state := "unknown"
			if item.Status == "in_progress" {
				state = "pending"
			}
			if process != "" {
				o.processes[process] = state
			} else {
				o.unknown[item.ID] = true
			}
		}
	}
}
func codexChildState(raw json.RawMessage) string {
	var state string
	if json.Unmarshal(raw, &state) != nil {
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) == nil && len(fields) == 1 {
			for key := range fields {
				state = key
			}
		}
	}
	switch state {
	case "completed", "errored", "shutdown":
		return "settled"
	case "pending_init", "running":
		return "pending"
	default:
		return "unknown"
	}
}
func (o *codexOperations) state() string {
	state := "settled"
	for _, inventory := range []map[string]string{o.children, o.processes} {
		for _, value := range inventory {
			if value == "pending" {
				return "pending"
			}
			if value != "settled" {
				state = "unknown"
			}
		}
	}
	if len(o.unknown) != 0 {
		state = "unknown"
	}
	return state
}
