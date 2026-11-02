package rollout

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func line(kind string, p any) string {
	b, _ := json.Marshal(map[string]any{"type": kind, "timestamp": "2026-11-01T10:00:00Z", "payload": p})
	return string(b) + "\n"
}
func fixture(t *testing.T) (string, string, protocol.RolloutPreviewSource) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "rollout-session.jsonl")
	s := protocol.RolloutPreviewSource{SessionID: "session", Path: path}
	return root, path, s
}
func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}
func header() string { return line("session_meta", map[string]any{"id": "session"}) }
func start(turn string) string {
	return line("event_msg", map[string]any{"type": "task_started", "turn_id": turn})
}
func user(run string) map[string]any {
	return map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "[tend run " + run + ", launch launch]"}}}
}
func message(text string) map[string]any {
	return map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text}}}
}
func item(text string) string {
	return line("event_msg", map[string]any{"type": "item_completed", "item": map[string]any{"type": "CommandExecution", "id": text, "command": "echo " + text, "aggregated_output": text, "exit_code": 0}})
}
func read(t *testing.T, r *Reader, s protocol.RolloutPreviewSource, q protocol.RolloutPreviewQuery) protocol.RolloutPreview {
	t.Helper()
	got := r.Read(t.Context(), "run", []protocol.RolloutPreviewSource{s}, q)
	if got.State != "ready" {
		t.Fatalf("state = %+v", got)
	}
	return got
}

func TestHistoryMutationAndDirectResponsesStayInsideOwnedTurns(t *testing.T) {
	root, path, s := fixture(t)
	write(t, path, header()+start("turn-a")+line("history_mutation", map[string]any{"operation": "append", "items": []any{user("run"), message("owned")}})+item("owned-tool")+start("turn-b")+line("response_item", message("foreign"))+item("foreign-tool"))
	r := New("epoch", root)
	got := read(t, r, s, protocol.RolloutPreviewQuery{})
	if got.Page.Logs[0].NativeID == got.Page.Logs[1].NativeID {
		t.Fatal("native mutation items share an identity")
	}
	raw, _ := json.Marshal(got.Page.Logs)
	if !strings.Contains(string(raw), "owned") || strings.Contains(string(raw), "foreign") {
		t.Fatal(string(raw))
	}
	s.TurnID = "turn-b"
	other := read(t, New("epoch", root), s, protocol.RolloutPreviewQuery{})
	raw, _ = json.Marshal(other.Page.Logs)
	if !strings.Contains(string(raw), "foreign") || strings.Contains(string(raw), "owned") {
		t.Fatal(string(raw))
	}
	if got := New("epoch", root).Read(t.Context(), "another-run", []protocol.RolloutPreviewSource{{SessionID: "session", Path: path}}, protocol.RolloutPreviewQuery{}); got.State != "source_unknown" {
		t.Fatal(got)
	}
}
func TestPartialTailAndReplacementInvalidateOnlyTheOldCursor(t *testing.T) {
	root, path, s := fixture(t)
	s.TurnID = "turn"
	body := header() + start("turn") + item("one")
	pending := item("two")
	write(t, path, body+pending[:len(pending)-3])
	r := New("epoch", root)
	a := read(t, r, s, protocol.RolloutPreviewQuery{})
	if len(a.Page.Logs) != 1 {
		t.Fatal(a.Page)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(pending[len(pending)-3:])
	_ = f.Close()
	b := read(t, r, s, protocol.RolloutPreviewQuery{After: a.Page.After, Generation: a.Generation})
	if len(b.Page.Logs) != 1 || b.Generation != a.Generation {
		t.Fatal(b)
	}
	replacement := filepath.Join(root, "new")
	write(t, replacement, body+item("replacement"))
	if err = os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	if got := r.Read(t.Context(), "run", []protocol.RolloutPreviewSource{s}, protocol.RolloutPreviewQuery{After: b.Page.After, Generation: b.Generation}); got.State != "cursor_invalid" {
		t.Fatal(got)
	}
	c := read(t, r, s, protocol.RolloutPreviewQuery{})
	write(t, path, header()+start("turn"))
	if got := r.Read(t.Context(), "run", []protocol.RolloutPreviewSource{s}, protocol.RolloutPreviewQuery{Generation: c.Generation}); got.State != "cursor_invalid" {
		t.Fatal(got)
	}
	_ = os.Remove(path)
	if got := r.Read(t.Context(), "run", []protocol.RolloutPreviewSource{s}, protocol.RolloutPreviewQuery{}); got.State != "missing" {
		t.Fatal(got)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if got := r.Read(t.Context(), "run", []protocol.RolloutPreviewSource{s}, protocol.RolloutPreviewQuery{}); got.State != "missing" {
		t.Fatal("missing native root was not reported as missing", got)
	}
}
func TestExternalContentIsPagedAndConfinedToConfirmedArtifactLinks(t *testing.T) {
	root, path, s := fixture(t)
	s.TurnID = "turn"
	art := strings.TrimSuffix(path, ".jsonl") + ".artifacts"
	if err := os.MkdirAll(art, 0700); err != nil {
		t.Fatal(err)
	}
	original := strings.Repeat("中文 output\n", 15000)
	write(t, filepath.Join(art, "output.txt"), original)
	write(t, path, header()+start("turn")+line("event_msg", map[string]any{"type": "item_completed", "item": map[string]any{"type": "McpToolCall", "id": "call", "tool": "command", "result": map[string]any{"artifact_path": "output.txt"}}}))
	r := New("epoch", root)
	a := read(t, r, s, protocol.RolloutPreviewQuery{})
	var combined strings.Builder
	offset := int64(0)
	for {
		page := read(t, r, s, protocol.RolloutPreviewQuery{Generation: a.Generation, Record: 1, Reference: 1, Offset: offset})
		if len(page.Content.Text) > contentBytes {
			t.Fatal("oversized detail")
		}
		combined.WriteString(page.Content.Text)
		offset = page.Content.NextOffset
		if !page.Content.More {
			break
		}
	}
	if combined.String() != original {
		t.Fatal("external output lost or duplicated bytes")
	}
	_ = os.Remove(filepath.Join(art, "output.txt"))
	if got := r.Read(t.Context(), "run", []protocol.RolloutPreviewSource{s}, protocol.RolloutPreviewQuery{Generation: a.Generation, Record: 1, Reference: 1}); got.State != "missing_reference" {
		t.Fatal(got)
	}
	outside := filepath.Join(t.TempDir(), "secret")
	write(t, outside, "secret")
	if err := os.Symlink(outside, filepath.Join(art, "output.txt")); err != nil {
		t.Skip(err)
	}
	if got := r.Read(t.Context(), "run", []protocol.RolloutPreviewSource{s}, protocol.RolloutPreviewQuery{Record: 1, Reference: 1}); got.State != "missing_reference" {
		t.Fatal("escaped artifact directory", got)
	}
}
func TestLocatorRequiresNativeHeaderAndRejectsPathEscapes(t *testing.T) {
	root, path, s := fixture(t)
	write(t, path, line("session_meta", map[string]any{"id": "foreign"}))
	if found, err := Locate(t.Context(), []string{root}, "session"); err != nil || found != "" {
		t.Fatal(found, err)
	}
	write(t, path, line("session_meta", map[string]any{"id": "session", "instructions": strings.Repeat("native context ", 8000)}))
	if found, err := Locate(t.Context(), []string{root}, "session"); err != nil || found != path {
		t.Fatal(found, err)
	}
	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	write(t, outside, header()+start("turn")+item("secret"))
	s.Path = outside
	s.TurnID = "turn"
	if got := New("epoch", root).Read(t.Context(), "run", []protocol.RolloutPreviewSource{s}, protocol.RolloutPreviewQuery{}); got.State == "ready" {
		t.Fatal("read arbitrary path")
	}
	if _, err := Open([]string{root}, outside); err == nil {
		t.Fatal("opened outside root")
	}
}
func TestPagingIsBoundedAndUnknownRecordsRemainExplicit(t *testing.T) {
	root, path, s := fixture(t)
	s.TurnID = "turn"
	body := header() + start("turn")
	for range 125 {
		body += item("output")
	}
	body += line("event_msg", map[string]any{"type": "item_completed", "item": map[string]any{"type": "FutureNativeTool"}})
	body += line("event_msg", map[string]any{"type": "future_native_event"})
	write(t, path, body)
	r := New("epoch", root)
	last := read(t, r, s, protocol.RolloutPreviewQuery{})
	if len(last.Page.Logs) != 100 || !last.Page.MoreBefore {
		t.Fatal(last.Page)
	}
	first := read(t, r, s, protocol.RolloutPreviewQuery{Before: last.Page.Before, Generation: last.Generation})
	if len(first.Page.Logs) != 27 || first.Page.MoreBefore {
		t.Fatal(first.Page)
	}
	raw, _ := json.Marshal(last.Page.Logs[len(last.Page.Logs)-1])
	if !strings.Contains(string(raw), "unknownFormat") {
		t.Fatal(string(raw))
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got := r.Read(ctx, "run", []protocol.RolloutPreviewSource{s}, protocol.RolloutPreviewQuery{}); got.State != "timeout" {
		t.Fatal(got)
	}
}

func TestMultipleSourcesPreserveOrderAndRestartInvalidatesCursors(t *testing.T) {
	root, path, first := fixture(t)
	first.TurnID = "turn"
	write(t, path, header()+start("turn")+item("first"))
	second := protocol.RolloutPreviewSource{SessionID: "resumed", TurnID: "resumed-turn", Path: filepath.Join(root, "rollout-resumed.jsonl")}
	write(t, second.Path, line("session_meta", map[string]any{"id": "resumed"})+start("resumed-turn")+item("second"))
	reader := New("instance", root)
	sources := []protocol.RolloutPreviewSource{first, second}
	got := reader.Read(t.Context(), "run", sources, protocol.RolloutPreviewQuery{})
	if got.State != "ready" || len(got.Page.Logs) != 2 || !strings.Contains(string(got.Page.Logs[0].Body), "first") || !strings.Contains(string(got.Page.Logs[1].Body), "second") {
		t.Fatal(got)
	}
	if next := New("restarted", root).Read(t.Context(), "run", sources, protocol.RolloutPreviewQuery{Generation: got.Generation}); next.State != "cursor_invalid" {
		t.Fatal(next)
	}
}
func TestArtifactRootCannotEscapeAndArgumentsAreNotAssociations(t *testing.T) {
	root, path, s := fixture(t)
	s.TurnID = "turn"
	art := strings.TrimSuffix(path, ".jsonl") + ".artifacts"
	outside := t.TempDir()
	write(t, filepath.Join(outside, "secret"), "private")
	if err := os.Symlink(outside, art); err != nil {
		t.Skip(err)
	}
	p := map[string]any{"type": "McpToolCall", "id": "call", "result": map[string]any{"artifact_path": "secret"}, "arguments": map[string]any{"artifact_path": "unrelated"}}
	if refs := references(p, path); len(refs) != 1 || refs[0] != "secret" {
		t.Fatal(refs)
	}
	write(t, path, header()+start("turn")+line("event_msg", map[string]any{"type": "item_completed", "item": p}))
	got := New("instance", root).Read(t.Context(), "run", []protocol.RolloutPreviewSource{s}, protocol.RolloutPreviewQuery{Record: 1, Reference: 1})
	if got.State != "missing_reference" {
		t.Fatal(got)
	}
}
func TestRecordByteBudgetStillMakesProgress(t *testing.T) {
	root, path, s := fixture(t)
	s.TurnID = "turn"
	var body strings.Builder
	body.WriteString(header() + start("turn"))
	for range 120 {
		body.WriteString(line("response_item", message(strings.Repeat("large output ", 1000))))
	}
	write(t, path, body.String())
	reader := New("instance", root)
	var after int64
	for {
		got := reader.Read(t.Context(), "run", []protocol.RolloutPreviewSource{s}, protocol.RolloutPreviewQuery{After: after})
		if got.State != "ready" || len(got.Page.Logs) == 0 || got.Page.After <= after {
			t.Fatal("pagination stalled", got)
		}
		raw, _ := json.Marshal(got)
		if len(raw) > (1<<20)+64<<10 {
			t.Fatal("unbounded page", len(raw))
		}
		after = got.Page.After
		if !got.Page.MoreAfter {
			break
		}
	}
	if after != 120 {
		t.Fatal(after)
	}
}

func TestUnknownHistoryRewriteDoesNotExposeAnotherRunsSnapshot(t *testing.T) {
	root, path, s := fixture(t)
	s.TurnID = "turn"
	write(t, path, header()+start("turn")+line("history_mutation", map[string]any{"operation": "replace", "items": []any{message("foreign history secret")}}))
	reader := New("instance", root)
	page := read(t, reader, s, protocol.RolloutPreviewQuery{})
	detail := read(t, reader, s, protocol.RolloutPreviewQuery{Generation: page.Generation, Record: 1})
	if strings.Contains(detail.Content.Text, "foreign history secret") || !strings.Contains(detail.Content.Text, "ownership is unconfirmed") {
		t.Fatal(detail.Content.Text)
	}
}

func TestBeforeByteLimitedPagesAreContiguousWithTheCurrentWindow(t *testing.T) {
	root, path, s := fixture(t)
	s.TurnID = "turn"
	var body strings.Builder
	body.WriteString(header() + start("turn"))
	for range 200 {
		body.WriteString(line("response_item", message(strings.Repeat("large output ", 1000))))
	}
	write(t, path, body.String())
	reader := New("instance", root)
	latest := read(t, reader, s, protocol.RolloutPreviewQuery{})
	if latest.Page.After != 200 || latest.Page.MoreAfter {
		t.Fatal("latest omitted tail", latest.Page)
	}
	total := len(latest.Page.Logs)
	page := latest
	for page.Page.MoreBefore {
		earlier := read(t, reader, s, protocol.RolloutPreviewQuery{Before: page.Page.Before, Generation: page.Generation})
		if earlier.Page.After+1 != page.Page.Before {
			t.Fatal("unreachable history gap", earlier.Page.After, page.Page.Before)
		}
		total += len(earlier.Page.Logs)
		page = earlier
	}
	if total != 200 {
		t.Fatal("lost history", total)
	}
	forward := read(t, reader, s, protocol.RolloutPreviewQuery{After: page.Page.After, Generation: page.Generation})
	if forward.Page.Before != page.Page.After+1 {
		t.Fatal("forward gap", forward.Page)
	}
}

func TestCustomToolCallInputIsAvailableBeforeAndAfterItsOutput(t *testing.T) {
	root, path, s := fixture(t)
	s.TurnID = "turn"
	patch := strings.Repeat("*** Begin Patch\n", 6000)
	write(t, path, header()+start("turn")+line("response_item", map[string]any{"type": "custom_tool_call", "name": "apply_patch", "call_id": "call", "input": patch})+line("response_item", map[string]any{"type": "custom_tool_call_output", "call_id": "call", "output": "done"}))
	reader := New("instance", root)
	page := read(t, reader, s, protocol.RolloutPreviewQuery{})
	detail := read(t, reader, s, protocol.RolloutPreviewQuery{Record: 1, Generation: page.Generation, Field: "input"})
	if !strings.HasPrefix(patch, detail.Content.Text) || detail.Content.Text == "" || !detail.Content.More {
		t.Fatal(detail)
	}
	if !strings.Contains(string(page.Page.Logs[0].Body), "arguments") {
		t.Fatal(string(page.Page.Logs[0].Body))
	}
}

func TestTerminalInteractionRetainsItsNativeCommandAndStdin(t *testing.T) {
	root, path, s := fixture(t)
	s.TurnID = "turn"
	write(t, path, header()+start("turn")+line("event_msg", map[string]any{"type": "item_completed", "item": map[string]any{"type": "TerminalInteraction", "id": "terminal-event", "call_id": "command", "command": "interactive-check", "stdin": "continue", "process_id": "process", "duration_ms": 12}}))
	reader := New("instance", root)
	page := read(t, reader, s, protocol.RolloutPreviewQuery{})
	if strings.Contains(string(page.Page.Logs[0].Body), "unknownFormat") {
		t.Fatal("known terminal interaction was rejected")
	}
	detail := read(t, reader, s, protocol.RolloutPreviewQuery{Generation: page.Generation, Record: 1, Field: "input"})
	if !strings.Contains(detail.Content.Text, "interactive-check") || !strings.Contains(detail.Content.Text, "continue") {
		t.Fatal(detail.Content.Text)
	}
}

func TestHistoryMutationRetainsOuterToolCallsAsWellAsNestedNativeCommands(t *testing.T) {
	root, path, s := fixture(t)
	s.TurnID = "turn"
	write(t, path, header()+start("turn")+line("history_mutation", map[string]any{"operation": "append", "items": []any{map[string]any{"type": "custom_tool_call", "name": "exec", "call_id": "outer", "input": "nested_command()"}}})+item("nested")+line("history_mutation", map[string]any{"operation": "append", "items": []any{map[string]any{"type": "custom_tool_call_output", "call_id": "outer", "output": "outer result"}}}))
	reader := New("instance", root)
	page := read(t, reader, s, protocol.RolloutPreviewQuery{})
	if len(page.Page.Logs) != 3 {
		t.Fatal("outer native calls were omitted", page.Page)
	}
	input := read(t, reader, s, protocol.RolloutPreviewQuery{Record: 1, Generation: page.Generation, Field: "input"})
	output := read(t, reader, s, protocol.RolloutPreviewQuery{Record: 3, Generation: page.Generation, Field: "output"})
	if input.Content.Text != "nested_command()" || output.Content.Text != "outer result" {
		t.Fatal(input, output)
	}
}

func TestEmptyEarlierSourceBecomingReadableInvalidatesExistingRecordCursors(t *testing.T) {
	root, path, first := fixture(t)
	first.TurnID = "turn"
	write(t, path, header()+start("turn"))
	second := protocol.RolloutPreviewSource{SessionID: "later", TurnID: "later-turn", Path: filepath.Join(root, "rollout-later.jsonl")}
	write(t, second.Path, line("session_meta", map[string]any{"id": "later"})+start("later-turn")+item("later record"))
	sources := []protocol.RolloutPreviewSource{first, second}
	reader := New("instance", root)
	initial := reader.Read(t.Context(), "run", sources, protocol.RolloutPreviewQuery{})
	if initial.State != "ready" || initial.Page.Watermark != 1 {
		t.Fatal(initial)
	}
	write(t, path, header()+start("turn")+item("earlier record"))
	if got := reader.Read(t.Context(), "run", sources, protocol.RolloutPreviewQuery{Generation: initial.Generation, Record: 1}); got.State != "cursor_invalid" {
		t.Fatal("old cursor returned a different source", got)
	}
}
func TestToolInputRemainsReadableWhenItsOutputIsInALaterWindow(t *testing.T) {
	root, path, s := fixture(t)
	s.TurnID = "turn"
	var body strings.Builder
	body.WriteString(header() + start("turn"))
	body.WriteString(line("response_item", map[string]any{"type": "custom_tool_call", "call_id": "outer", "name": "exec", "input": "original outer input"}))
	for range 150 {
		body.WriteString(item("nested"))
	}
	body.WriteString(line("response_item", map[string]any{"type": "custom_tool_call_output", "call_id": "outer", "output": "finished"}))
	write(t, path, body.String())
	reader := New("instance", root)
	page := read(t, reader, s, protocol.RolloutPreviewQuery{})
	if page.Page.Before <= 1 {
		t.Fatal("fixture did not split input/output windows")
	}
	var envelope struct {
		Data struct {
			Input int `json:"preview_input_record"`
		}
	}
	if err := json.Unmarshal(page.Page.Logs[len(page.Page.Logs)-1].Body, &envelope); err != nil || envelope.Data.Input != 1 {
		t.Fatal(envelope, err)
	}
	input := read(t, reader, s, protocol.RolloutPreviewQuery{Generation: page.Generation, Record: page.Page.After, Field: "input"})
	if input.Content.Text != "original outer input" {
		t.Fatal(input.Content.Text)
	}
}

func TestAtomicReplacementCannotChangeTheTurnOfProjectedBodies(t *testing.T) {
	root, path, source := fixture(t)
	source.TurnID = "turn-a"
	makeBody := func(turn, text string) string {
		var body strings.Builder
		body.WriteString(header() + start(turn))
		for range 1000 {
			body.WriteString(line("response_item", message(text)))
		}
		return body.String()
	}
	a, b := filepath.Join(root, "original-a"), filepath.Join(root, "original-b")
	write(t, a, makeBody("turn-a", "OWNED!!"))
	write(t, b, makeBody("turn-b", "FOREIGN"))
	if err := os.Link(a, path); err != nil {
		t.Skip(err)
	}
	reader := New("epoch", root)
	initial := read(t, reader, source, protocol.RolloutPreviewQuery{})
	if len(initial.Page.Logs) != 100 {
		t.Fatal(initial.Page)
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for n := 0; ; n++ {
			select {
			case <-stop:
				return
			default:
			}
			original := a
			if n%2 == 1 {
				original = b
			}
			temporary := filepath.Join(root, "replacement")
			if os.Link(original, temporary) == nil {
				_ = os.Rename(temporary, path)
				_ = os.Remove(temporary)
			}
		}
	}()
	defer func() { close(stop); <-done }()
	for range 100 {
		result := reader.Read(t.Context(), "run", []protocol.RolloutPreviewSource{source}, protocol.RolloutPreviewQuery{})
		if result.Page == nil {
			continue
		}
		for _, record := range result.Page.Logs {
			if strings.Contains(string(record.Body), "FOREIGN") {
				t.Fatal("replacement turn was returned under the validated turn", string(record.Body))
			}
		}
	}
}
