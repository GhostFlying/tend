// Package rollout reads provider-owned transcripts without starting a provider.
package rollout

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/GhostFlying/tend/internal/protocol"
)

const summaryBytes = 8192
const contentBytes = 64 << 10

type record struct {
	position int
	tool     string
	input    *record
	offset   int64
	size     int
	part     int
	turn     string
	at       time.Time
	kind     string
}
type fileIndex struct {
	inputs    map[string]record
	info      os.FileInfo
	offset    int64
	turn      string
	owned     bool
	records   []record
	supported bool
	proven    bool
	header    bool
	anchor    [32]byte
}
type runIndex struct {
	sources  []protocol.RolloutPreviewSource
	files    []*fileIndex
	revision uint64
}

// Reader retains offsets and call identities only. Native originals are never rewritten or copied.
type Reader struct {
	opened map[string]*os.File
	gate   chan struct{}
	epoch  string
	roots  []string
	runs   map[string]*runIndex
	serial uint64
}

func New(epoch string, roots ...string) *Reader {
	return &Reader{gate: make(chan struct{}, 1), epoch: epoch, roots: roots, runs: map[string]*runIndex{}}
}

func state(s string) protocol.RolloutPreview { return protocol.RolloutPreview{State: s} }

func (r *Reader) Read(ctx context.Context, runID string, sources []protocol.RolloutPreviewSource, q protocol.RolloutPreviewQuery) protocol.RolloutPreview {
	select {
	case r.gate <- struct{}{}:
		defer func() { <-r.gate }()
	case <-ctx.Done():
		return state("timeout")
	}
	// A request reads bodies from the same descriptors whose identities and
	// offsets it validated. Reopening a pathname could cross an atomic replacement.
	r.opened = map[string]*os.File{}
	defer func() {
		for _, f := range r.opened {
			_ = f.Close()
		}
		r.opened = nil
	}()
	if len(sources) == 0 {
		return state("source_unknown")
	}
	index := r.runs[runID]
	encoded, _ := json.Marshal(sources)
	if index == nil {
		if len(r.runs) >= 64 {
			for key := range r.runs {
				delete(r.runs, key)
				break
			}
		}
		r.serial++
		index = &runIndex{revision: r.serial}
		r.runs[runID] = index
	}
	old, _ := json.Marshal(index.sources)
	if !bytes.Equal(old, encoded) {
		index.sources = append([]protocol.RolloutPreviewSource(nil), sources...)
		index.files = make([]*fileIndex, len(sources))
		r.serial++
		index.revision = r.serial
	}
	type located struct {
		source int
		record record
	}
	var records []located
	bases := make([]int, len(sources))
	supported := false
	for i, source := range sources {
		if ctx.Err() != nil {
			return state("timeout")
		}
		if source.Path == "" {
			return state("source_unknown")
		}
		f := r.opened[source.Path]
		var err error
		if f == nil {
			f, err = Open(r.roots, source.Path)
			if err == nil {
				r.opened[source.Path] = f
			}
		}
		if errors.Is(err, os.ErrNotExist) {
			index.files[i] = nil
			return state("missing")
		}
		if err != nil {
			return state("unavailable")
		}
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			return state("unavailable")
		}
		fi := index.files[i]
		if fi == nil || !os.SameFile(fi.info, info) || info.Size() < fi.info.Size() || info.Size() < fi.offset || fingerprint(f, fi.offset) != fi.anchor {
			fi = &fileIndex{info: info}
			index.files[i] = fi
			r.serial++
			index.revision = r.serial
		}
		priorCount := len(fi.records)
		err = scan(ctx, f, fi, runID, source)
		fi.info = info
		fi.anchor = fingerprint(f, fi.offset)
		if i < len(sources)-1 && len(fi.records) != priorCount {
			r.serial++
			index.revision = r.serial
		}
		if ctx.Err() != nil {
			return state("timeout")
		}
		if err != nil {
			return state("format_unsupported")
		}
		if !fi.header || source.TurnID == "" && !fi.proven {
			return state("source_unknown")
		}
		supported = supported || fi.supported
		bases[i] = len(records)
		for _, rec := range fi.records {
			records = append(records, located{i, rec})
		}
	}
	sum := sha256.Sum256([]byte(r.epoch + string(encoded) + strconv.FormatUint(index.revision, 10)))
	generation := hex.EncodeToString(sum[:16])
	if q.Generation != "" && q.Generation != generation {
		return protocol.RolloutPreview{State: "cursor_invalid", Generation: generation}
	}
	if q.Record > 0 {
		if q.Record > int64(len(records)) {
			return state("cursor_invalid")
		}
		loc := records[q.Record-1]
		return r.content(sources[loc.source].Path, loc.record, q, generation)
	}

	page := &protocol.RunRolloutPage{RunID: runID, Logs: []protocol.RunLogView{}, Watermark: int64(len(records))}
	start, end := 0, len(records)
	if q.Before > 0 {
		end = min(end, int(q.Before-1))
	}
	if q.After > 0 {
		start = min(end, int(q.After))
	} else {
		start = max(0, end-100)
	}
	end = min(end, start+100)
	size := 0
	backward := q.After == 0
	count := end - start
	for n := 0; n < count; n++ {
		j := start + n
		if backward {
			j = end - 1 - n
		}
		loc := records[j]
		body, err := r.readRecord(sources[loc.source].Path, loc.record, true)
		if err != nil {
			return state("unavailable")
		}
		if caller := loc.record.input; caller != nil {
			body["preview_input_record"] = bases[loc.source] + caller.position + 1
			body["tool"] = bound(caller.tool)
		}
		raw, _ := json.Marshal(map[string]any{"thread_id": sources[loc.source].SessionID, "turn_id": loc.record.turn, "data": body, "preview_record": j + 1, "preview_more": body["preview_more"], "item_id": body["id"]})
		if size+len(raw) > 1<<20 {
			if backward {
				start = j + 1
			} else {
				end = j
			}
			break
		}
		size += len(raw)
		page.Logs = append(page.Logs, protocol.RunLogView{Seq: int64(j + 1), NativeID: fmt.Sprintf("native:%s:%d:%d", sources[loc.source].SessionID, loc.record.offset, loc.record.part), Kind: loc.record.kind, Body: raw, CreatedAt: loc.record.at})
	}
	if backward {
		slices.Reverse(page.Logs)
	}
	page.Before = int64(start + 1)
	page.After = int64(end)
	if len(page.Logs) == 0 {
		page.Before = q.Before
		page.After = q.After
	}
	page.MoreBefore = start > 0
	page.MoreAfter = end < len(records)
	result := protocol.RolloutPreview{State: "ready", Generation: generation, Page: page}
	if len(records) == 0 {
		result.State = "empty"
		if !supported {
			result.State = "format_unsupported"
		}
	}
	return result
}

type nativeRow struct {
	Type      string          `json:"type"`
	Timestamp time.Time       `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

// fingerprint detects replacement/truncate-and-regrow without rereading an entire transcript.
func fingerprint(f *os.File, offset int64) [32]byte {
	head := make([]byte, min(offset, 1024))
	_, _ = f.ReadAt(head, 0)
	tail := make([]byte, min(offset, 4096))
	_, _ = f.ReadAt(tail, offset-int64(len(tail)))
	return sha256.Sum256(append(head, tail...))
}

const maxNativeLine = 16 << 20

func scan(ctx context.Context, f *os.File, index *fileIndex, runID string, source protocol.RolloutPreviewSource) error {
	if _, err := f.Seek(index.offset, io.SeekStart); err != nil {
		return err
	}
	reader := bufio.NewReaderSize(f, 64<<10)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var line []byte
		for {
			part, err := reader.ReadSlice('\n')
			line = append(line, part...)
			if len(line) > maxNativeLine {
				return errors.New("native record exceeds parsing limit")
			}
			if errors.Is(err, io.EOF) {
				return nil
			} // A partial tail is never committed.
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			if err != nil {
				return err
			}
			break
		}
		var row nativeRow
		if err := json.Unmarshal(line, &row); err != nil {
			return err
		}
		var payload map[string]any
		if err := json.Unmarshal(row.Payload, &payload); err != nil {
			return err
		}
		typ, _ := payload["type"].(string)
		if row.Type == "session_meta" {
			if payload["id"] != source.SessionID {
				return errors.New("native session identity mismatch")
			}
			index.header, index.supported = true, true
		}
		if !index.header {
			return errors.New("native session header missing")
		}
		turn, _ := payload["turn_id"].(string)
		if (row.Type == "event_msg" && (typ == "task_started" || typ == "turn_started")) || (row.Type == "turn_context" && turn != "" && turn != index.turn) {
			index.turn = turn
			index.owned = false
		}
		if source.TurnID != "" {
			index.owned = index.turn == source.TurnID
		}
		marker := containsRunInput(row, payload, runID)
		if source.TurnID == "" && marker {
			index.owned = true
		}
		add := func(kind string, part int) {
			rec := record{position: len(index.records), offset: index.offset, size: len(line), part: part, turn: index.turn, at: row.Timestamp, kind: kind}
			item := payload
			if part >= 0 {
				items, _ := payload["items"].([]any)
				item, _ = items[part].(map[string]any)
			}
			call, _ := item["call_id"].(string)
			key := index.turn + "\x00" + call
			if call != "" {
				switch item["type"] {
				case "function_call", "custom_tool_call":
					if index.inputs == nil {
						index.inputs = map[string]record{}
					}
					rec.tool, _ = item["name"].(string)
					index.inputs[key] = rec
				case "function_call_output", "custom_tool_call_output":
					if caller, ok := index.inputs[key]; ok {
						rec.input = &caller
					}
				}
			}
			index.records = append(index.records, rec)
		}
		if index.owned {
			index.proven = true
			switch row.Type {
			case "event_msg":
				switch typ {
				case "item_completed":
					add("item.completed", -1)
				case "item_started":
					add("item.started", -1)
				case "agent_message":
					add("item.completed", -1)
				case "task_complete", "turn_complete":
					add("turn.completed", -1)
				case "turn_aborted", "error":
					add("turn.failed", -1)
				case "task_started", "turn_started", "token_count", "thread_settings_applied", "agent_reasoning", "agent_reasoning_raw_content":
				default:
					add("native.unknown", -1)
				}
			case "response_item":
				switch typ {
				case "message", "function_call", "custom_tool_call", "function_call_output", "custom_tool_call_output", "reasoning":
					add("item.completed", -1)
				default:
					add("native.unknown", -1)
				}
			case "history_mutation":
				if payload["operation"] != "append" {
					add("native.unknown", -2)
					break
				}
				items, _ := payload["items"].([]any)
				for i, item := range items {
					m, _ := item.(map[string]any)
					switch m["type"] {
					case "message", "reasoning", "function_call", "custom_tool_call", "function_call_output", "custom_tool_call_output":
						add("item.completed", i)
					case "trae_extra_info":
						// Provider bookkeeping, not an agent action.
					default:
						add("native.unknown", i)
					}
				}
			case "session_meta", "turn_context", "world_state", "token_usage_record":
			default:
				add("native.unknown", -1)
			}
		}
		index.offset += int64(len(line))
	}
}
func containsRunInput(row nativeRow, p map[string]any, runID string) bool {
	if row.Type == "history_mutation" {
		items, _ := p["items"].([]any)
		for _, item := range items {
			if m, ok := item.(map[string]any); ok && containsRunInput(nativeRow{Type: "response_item"}, m, runID) {
				return true
			}
		}
		return false
	}
	if row.Type != "response_item" || p["role"] != "user" {
		return false
	}
	parts, _ := p["content"].([]any)
	for _, part := range parts {
		m, _ := part.(map[string]any)
		text, _ := m["text"].(string)
		if strings.HasPrefix(text, "[tend run "+runID+", launch ") {
			return true
		}
	}
	return false
}

func (r *Reader) nativeRecord(path string, rec record) (nativeRow, map[string]any, []byte, error) {
	f := r.opened[path]
	if f == nil {
		return nativeRow{}, nil, nil, errors.New("native snapshot descriptor is unavailable")
	}
	var err error
	raw := make([]byte, rec.size)
	if _, err = f.ReadAt(raw, rec.offset); err != nil {
		return nativeRow{}, nil, nil, err
	}
	var row nativeRow
	if err = json.Unmarshal(raw, &row); err != nil {
		return row, nil, nil, err
	}
	var p map[string]any
	if err = json.Unmarshal(row.Payload, &p); err != nil {
		return row, nil, nil, err
	}
	if rec.part == -2 {
		// Snapshot/rewrite mutations may contain other runs' history. Keep only
		// a format diagnostic until their per-item attribution is understood.
		p = map[string]any{"operation": p["operation"], "diagnostic": "history body omitted: item ownership is unconfirmed"}
		raw, _ = json.Marshal(map[string]any{"type": row.Type, "timestamp": row.Timestamp, "payload": p})
	}
	if rec.part >= 0 {
		items, _ := p["items"].([]any)
		if rec.part >= len(items) {
			return row, nil, nil, errors.New("native record changed")
		}
		p, _ = items[rec.part].(map[string]any)
	}
	return row, p, raw, nil
}
func (r *Reader) readRecord(path string, rec record, bounded bool) (map[string]any, error) {
	row, p, _, err := r.nativeRecord(path, rec)
	if err != nil {
		return nil, err
	}
	data := p
	if item, ok := p["item"].(map[string]any); ok {
		data = item
	}
	data = normalize(data)
	if row.Type == "event_msg" && (p["type"] == "task_complete" || p["type"] == "turn_complete") {
		data = map[string]any{"type": "turnComplete", "text": p["last_agent_message"], "error": p["error"], "status": p["status"]}
	}
	if rec.kind == "turn.failed" {
		data = map[string]any{"type": "executionError", "error": p["message"], "text": p["reason"], "status": "failed"}
	}
	if rec.kind == "native.unknown" {
		data = map[string]any{"type": "unknownFormat", "format": row.Type, "text": "原生记录格式尚未支持，可查看原始记录。"}
	}
	refs := references(p, path)
	if len(refs) > 0 {
		data["preview_references"] = r.referenceViews(path, refs)
		data["preview_more"] = true
	}
	if bounded {
		data = boundedRecord(data)
	}
	return data, nil
}

func normalize(p map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range p {
		out[k] = v
	}
	typ, _ := p["type"].(string)
	switch typ {
	case "message":
		if p["role"] == "assistant" {
			out["type"] = "agentMessage"
			out["text"] = textContent(p["content"])
		} else {
			out = map[string]any{"type": "userMessage", "text": textContent(p["content"])}
			if p["role"] != "user" {
				out["type"] = "contextMessage"
			}
		}
	case "agent_message":
		out["type"] = "agentMessage"
		out["text"] = p["message"]
	case "function_call", "custom_tool_call":
		out["type"] = "dynamicToolCall"
		out["tool"] = p["name"]
		out["arguments"] = p["arguments"]
		if typ == "custom_tool_call" {
			out["arguments"] = p["input"]
		}
		out["status"] = "running"
		out["id"] = p["call_id"]
	case "function_call_output", "custom_tool_call_output":
		out["type"] = "dynamicToolCall"
		out["result"] = p["output"]
		out["status"] = "completed"
		out["id"] = p["call_id"]
	case "TerminalInteraction", "terminalInteraction":
		out["type"], out["tool"], out["status"] = "dynamicToolCall", "终端交互", "completed"
		out["arguments"] = map[string]any{"command": p["command"], "stdin": p["stdin"], "process_id": p["process_id"]}
		delete(out, "command")
	case "reasoning":
		out = map[string]any{"type": "reasoning", "id": p["id"], "text": textContent(p["summary"])}
	default:
		if typ != "" {
			out["type"] = strings.ToLower(typ[:1]) + typ[1:]
		}
	}
	switch out["type"] {
	case "agentMessage", "userMessage", "contextMessage", "reasoning", "commandExecution", "mcpToolCall", "dynamicToolCall", "webSearch", "imageView", "fileChange", "collabAgentToolCall", "contextCompaction":
	default:
		out["format"], out["type"] = typ, "unknownFormat"
		out["text"] = "原生记录格式尚未支持，可查看原始记录。"
	}
	for _, pair := range [][2]string{{"aggregated_output", "aggregatedOutput"}, {"exit_code", "exitCode"}, {"duration_ms", "durationMs"}, {"content_items", "contentItems"}} {
		if v, ok := out[pair[0]]; ok {
			out[pair[1]] = v
			delete(out, pair[0])
		}
	}
	if command, ok := out["command"].([]any); ok {
		var parts []string
		for _, x := range command {
			parts = append(parts, fmt.Sprint(x))
		}
		out["command"] = strings.Join(parts, " ")
	}
	if duration, ok := out["duration"].(map[string]any); ok {
		secs, _ := duration["secs"].(float64)
		nanos, _ := duration["nanos"].(float64)
		out["durationMs"] = secs*1000 + nanos/1e6
	}
	return out
}
func textContent(v any) string {
	parts, _ := v.([]any)
	var result []string
	for _, p := range parts {
		if m, ok := p.(map[string]any); ok {
			if text, ok := m["text"].(string); ok {
				result = append(result, text)
			}
		}
	}
	return strings.Join(result, "\n")
}

func bound(v any) any {
	switch x := v.(type) {
	case string:
		if len(x) > summaryBytes {
			end := summaryBytes
			for !utf8.RuneStart(x[end]) {
				end--
			}
			return x[:end] + "\n…（完整内容见记录详情）"
		}
		return x
	case map[string]any:
		out := map[string]any{}
		for k, v := range x {
			out[k] = bound(v)
		}
		raw, _ := json.Marshal(x)
		if len(raw) > summaryBytes {
			out["preview_more"] = true
		}
		return out
	case []any:
		out := make([]any, min(len(x), 100))
		for i := range out {
			out[i] = bound(x[i])
		}
		return out
	default:
		return v
	}
}

func boundedRecord(data map[string]any) map[string]any {
	out := bound(data).(map[string]any)
	raw, _ := json.Marshal(out)
	if len(raw) <= 32<<10 {
		return out
	}
	limited := map[string]any{"preview_more": true}
	for _, key := range []string{"type", "id", "status", "exitCode", "durationMs", "preview_references"} {
		if value, ok := out[key]; ok {
			limited[key] = value
		}
	}
	for _, key := range []string{"text", "command", "tool", "arguments", "query", "path", "result", "aggregatedOutput", "error"} {
		value, ok := out[key]
		if !ok {
			continue
		}
		encoded, _ := json.Marshal(value)
		if len(encoded) > 1024 {
			end := 1024
			for !utf8.RuneStart(encoded[end]) {
				end--
			}
			limited[key] = string(encoded[:end]) + "…"
		} else {
			limited[key] = value
		}
	}
	return limited
}
