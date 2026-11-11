package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestOriginalCommandDisplayRoundTripAndRejectsInvalidControls(t *testing.T) {
	for _, display := range []string{
		`sh -c 'printf "中文😀"; echo "$value"'`,
		"  sh\t-c \"a'\"'b" + "'\"c\\\"d\"  ",
		"sh -c \"first\nsecond\\\\third\\$fourth\" ''",
		"sh -c first\\ word\\\ncontinued",
	} {
		args, control, ok := commandDisplay([]byte(display))
		if !ok {
			t.Fatalf("known display rejected: %q", display)
		}
		source, _ := json.Marshal(args)
		encoding, err := commandDisplayPresentation([]byte(display), control)
		if err != nil {
			t.Fatal(err)
		}
		actual, err := encodeOriginal(source, encoding)
		if err != nil || string(actual) != display {
			t.Fatal("argv display changed", err)
		}
		wrong, _ := json.Marshal(append(args, "another argument"))
		if _, err := encodeOriginal(wrong, encoding); err == nil {
			t.Fatal("unconsumed source argv was accepted")
		}
	}
	for _, invalid := range []string{"", " \t", "sh 'open", "sh trailing\\"} {
		if _, _, ok := commandDisplay([]byte(invalid)); ok {
			t.Fatalf("incomplete display accepted: %q", invalid)
		}
	}
	for _, control := range [][]byte{{0, 1, 'x'}, {99, 0}, {0, 9, ' '}, {0x80}} {
		encoding, err := commandDisplayPresentation([]byte("a"), control)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := encodeOriginal([]byte(`["a"]`), encoding); err == nil {
			t.Fatal("invalid/oversized control was accepted")
		}
	}
	for _, encoding := range []string{"argv-display:16777217:bad", "argv-display:1:bad", "unknown-clip", "json-string-clip"} {
		if _, err := encodeOriginal([]byte(`"short"`), encoding); err == nil {
			t.Fatal("unsupported presentation was accepted", encoding)
		}
	}
}

func TestOriginalChildrenMigrationReusesMixedPackAndExactClipAfterRestart(t *testing.T) {
	s, path := openTest(t)
	ctx := t.Context()
	insertNativeOriginalFixture(t, s)
	input := strings.Repeat("real input 中文😀\n", 1500)
	args := []string{"sh", "-c", input}
	display := "sh -c '" + input + "'"
	mcp := `{"result":"` + strings.Repeat("MCP正文", 2000) + `"}`
	stdout := strings.Repeat("complete stdout\n", 1500)
	fullTool := strings.Repeat("a", 8191) + strings.Repeat("中文😀", 2500)
	clip := string(historicalToolClip([]byte(fullTool)))
	nestedField := strings.Repeat("nested 字段😀\n", 128)
	inner, _ := json.Marshal(map[string]string{"field": nestedField})
	smallSource, _ := json.Marshal(string(inner))
	if len(nestedField) < retainBodyThreshold || len(smallSource) < retainBodyThreshold || len(smallSource) >= 8192 {
		t.Fatal("fixture does not exercise the small quoted-JSON prefilter boundary")
	}
	source, _ := json.Marshal(map[string]any{"command": args, "result": map[string]any{"structuredContent": map[string]any{"content": mcp}}, "aggregatedOutput": stdout, "tool_output": fullTool})
	if _, err := s.files.AppendOriginal(ctx, "legacy-mixed", []byte("opaque preceding bytes\n")); err != nil {
		t.Fatal(err)
	}
	w, err := s.files.AppendOriginal(ctx, "legacy-mixed", source)
	if err != nil {
		t.Fatal(err)
	}
	smallWindow, err := s.files.AppendOriginal(ctx, "legacy-mixed", smallSource)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(ctx, func(tx *Tx) error {
		if _, err := tx.bindOriginalWindow(ctx, preparedOriginal{Window: w}); err != nil {
			return err
		}
		_, err := tx.bindOriginalWindow(ctx, preparedOriginal{Window: smallWindow})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	event, _ := json.Marshal(map[string]any{"type": "commandExecution", "id": "actual-command", "command": display, "result": map[string]any{"structuredContent": map[string]any{"content": mcp}}, "aggregatedOutput": stdout})
	mustExec(t, s, `INSERT INTO run_logs(run_id,seq,native_id,kind,body_json,created_at) VALUES('body-run',1,'actual-command','item.completed',?,1)`, string(event))
	mustExec(t, s, `INSERT INTO conversation_messages(message_id,author,body,kind,created_at) VALUES('actual-tool','main_agent','','tool_use',1)`)
	mustExec(t, s, `INSERT INTO conversation_tool_uses(message_id,thread_id,turn_id,item_id,tool,input,output,status,updated_at) VALUES('actual-tool','thread','turn','actual-tool','inspect',?,?,'completed',1)`, nestedField, clip)
	staged, first, err := s.PrepareOriginalMigration(ctx, 1)
	if err != nil || len(first.rows) != 0 || len(first.originals) == 0 {
		t.Fatalf("existing child indices must precede body staging: %+v %v", first, err)
	}
	injected := errors.New("interrupt index commit")
	if err := s.Write(staged, func(tx *Tx) error {
		if _, err := tx.ApplyOriginalMigration(staged, first, 1, 2); err != nil {
			return err
		}
		return injected
	}); !errors.Is(err, injected) {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path, testkit.NewClock().Now)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	done := false
	for range 100 {
		if !migrateOriginalBatch(t, s, 1).More {
			done = true
			break
		}
	}
	if !done {
		t.Fatal("bounded child indexing did not complete")
	}
	if err := s.Read(ctx, func(tx *ReadTx) error {
		log, exists, err := RunLogAt(ctx, tx.Q(), "body-run", 1)
		if err != nil || !exists || !SemanticJSONEqual(string(event), log.BodyJSON) {
			t.Fatal("event content or exact command string changed", err)
		}
		u, err := GetToolUse(ctx, tx.Q(), "turn", "actual-tool")
		if err == nil && (u.Input != nestedField || u.Output != clip) {
			t.Fatal("nested original field or historical clip display changed")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if raw, err := s.files.ReadWindow(ctx, w); err != nil || string(raw) != string(source) {
		t.Fatal("the existing original changed", err)
	}
	if raw, err := s.files.ReadWindow(ctx, smallWindow); err != nil || string(raw) != string(smallSource) {
		t.Fatal("the small quoted original changed", err)
	}
	var archives, controls, clipRefs, fieldRefs int
	if err := s.read.QueryRowContext(ctx, `SELECT count(*) FROM retained_original_archives`).Scan(&archives); err != nil || archives != 1 {
		t.Fatal("derived display created new files", archives, err)
	}
	if err := s.read.QueryRowContext(ctx, `SELECT count(*) FROM retained_original_values WHERE encoding LIKE 'argv-display:%'`).Scan(&controls); err != nil || controls != 1 {
		t.Fatal("command did not reuse original argv", controls, err)
	}
	if err := s.read.QueryRowContext(ctx, `SELECT count(*) FROM retained_original_values WHERE encoding='json-string-clip'`).Scan(&clipRefs); err != nil || clipRefs != 1 {
		t.Fatal("clip did not reuse original tool output", clipRefs, err)
	}
	if err := s.read.QueryRowContext(ctx, `SELECT count(*) FROM retained_original_values WHERE encoding LIKE 'json-string-field:%'`).Scan(&fieldRefs); err != nil || fieldRefs != 1 {
		t.Fatal("small quoted JSON field did not reuse its original", fieldRefs, err)
	}
	verified, err := s.VerifyOriginals(ctx, "", 100)
	if err != nil || verified.More || verified.Checked < 6 {
		t.Fatal("window/value readback failed", verified, err)
	}
	// A mutable-row rescan retains the immutable index cursor and returns the
	// same content without creating another body or a new consumer alias.
	staged, rescan, err := s.PrepareOriginalMigration(ctx, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	state, err := s.OriginalStorageState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(staged, func(tx *Tx) error {
		_, err := tx.ApplyOriginalMigration(staged, rescan, state.Revision, 3)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.PrepareOriginalMigration(context.Background(), 0); protocol.CodeOf(err) != protocol.CodeInvalidRequest {
		t.Fatal("index work bypassed the existing batch limit")
	}
}

func TestOriginalStringFieldKeepsExactTextAndRejectsInvalidSelection(t *testing.T) {
	text := strings.Repeat("原文😀\n", 1200)
	inner, _ := json.Marshal(map[string]any{"materials": map[string]any{"messages": []any{map[string]any{"tool": map[string]any{"input": text}}}}})
	raw, _ := json.Marshal(string(inner))
	path, _ := json.Marshal([]string{"materials", "messages", "0", "tool", "input"})
	encoding := stringFieldEncoding + base64.RawURLEncoding.EncodeToString(path)
	got, err := encodeOriginal(raw, encoding)
	if err != nil || string(got) != text {
		t.Fatal("field text changed", err)
	}
	s, _ := openTest(t)
	w, err := s.files.AppendOriginal(t.Context(), "nested-source", raw)
	if err != nil {
		t.Fatal(err)
	}
	aliases := originalStringFields(raw, w, map[string]bool{bodyHash([]byte(text)): true})
	if len(aliases) != 1 || aliases[0].Reference.Encoding != encoding {
		t.Fatal("actual field not indexed")
	}
	if len(originalStringFields(raw, w, map[string]bool{})) != 0 {
		t.Fatal("unused aliases retained")
	}
	if _, err := resolveOriginalReference(t.Context(), s.files, aliases[0].Reference); err != nil {
		t.Fatal(err)
	}
	aliases[0].Reference.SHA256 = strings.Repeat("0", 64)
	if _, err := resolveOriginalReference(t.Context(), s.files, aliases[0].Reference); err == nil {
		t.Fatal("bad output digest accepted")
	}
	for _, path := range [][]string{nil, {"missing"}, {"materials", "messages", "01"}, {"materials", "messages", "-1"}, {"materials", "messages", "1"}, {"materials", "messages"}} {
		b, _ := json.Marshal(path)
		if _, err := encodeOriginal(raw, stringFieldEncoding+base64.RawURLEncoding.EncodeToString(b)); err == nil {
			t.Fatal("invalid field accepted", path)
		}
	}
	for _, source := range [][]byte{[]byte(`"not JSON"`), []byte(`{}`), []byte(`"null"`), []byte(`"[] trailing"`)} {
		if _, err := encodeOriginal(source, encoding); err == nil {
			t.Fatal("invalid field source accepted")
		}
	}
	if _, err := encodeOriginal(raw, stringFieldEncoding+strings.Repeat("A", 8193)); err == nil {
		t.Fatal("oversized path accepted")
	}
}

func TestOriginalIndexPassBuildsNeededRootsOnceAndRetainsOnlyUsedAliases(t *testing.T) {
	s, _ := openTest(t)
	ctx := t.Context()
	insertNativeOriginalFixture(t, s)
	full := strings.Repeat("source 中文😀", 1200)
	clip := string(historicalToolClip([]byte(full)))
	mustExec(t, s, `INSERT INTO conversation_messages(message_id,author,body,kind,created_at) VALUES('clip-owner','main_agent','','tool_use',1)`)
	mustExec(t, s, `INSERT INTO conversation_tool_uses(message_id,thread_id,turn_id,item_id,tool,input,output,status,updated_at) VALUES('clip-owner','thread','turn','clip-item','inspect','',?,'completed',1)`, clip)
	for i := range 24 {
		raw, _ := json.Marshal(map[string]any{"needed": full, "unused": strings.Repeat(fmt.Sprint(i), 9000)})
		w, err := s.files.AppendOriginal(ctx, "many-source-windows", raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Write(ctx, func(tx *Tx) error { _, err := tx.bindOriginalWindow(ctx, preparedOriginal{Window: w}); return err }); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 8 {
		mustExec(t, s, `INSERT INTO conversation_messages(message_id,author,body,kind,created_at) VALUES(?,'main_agent',?,'message',1)`, fmt.Sprint("body-", i), strings.Repeat(fmt.Sprint("new original", i), 1000))
	}

	started := time.Now()
	batches := 0
	for range 100 {
		staged, b, err := s.PrepareOriginalMigration(ctx, 1, true)
		if err != nil {
			t.Fatal(err)
		}
		st, err := s.OriginalStorageState(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Write(staged, func(tx *Tx) error { _, err := tx.ApplyOriginalMigration(staged, b, st.Revision, 1); return err }); err != nil {
			t.Fatal(err)
		}
		batches++
		if !b.more {
			break
		}
	}
	if batches < 24 || s.originalIndexBuilds != 1 {
		t.Fatal("root set rebuilt per batch", batches, s.originalIndexBuilds)
	}
	var n int
	if err := s.read.QueryRowContext(ctx, `SELECT count(*) FROM retained_original_values`).Scan(&n); err != nil || n != 9 {
		t.Fatal("unused aliases retained", n, err)
	}
	t.Logf("index batches=%d root_scans=%d elapsed=%s", batches, s.originalIndexBuilds, time.Since(started))
}
