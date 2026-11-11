package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/testkit"
)

func retainEventFixture(t *testing.T, s *Store, body string) string {
	t.Helper()
	ctx, err := s.StageEventOriginals(t.Context(), "event-fixture", []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	var stored string
	if err = s.Write(ctx, func(tx *Tx) error { var e error; stored, e = tx.RetainEventJSON(ctx, body); return e }); err != nil {
		t.Fatal(err)
	}
	return stored
}
func resolveEventFixture(t *testing.T, s *Store, stored string) string {
	t.Helper()
	var full string
	if err := s.Read(t.Context(), func(tx *ReadTx) error { var e error; full, e = ResolveJSON(t.Context(), tx.Q(), stored); return e }); err != nil {
		t.Fatal(err)
	}
	return full
}
func archiveCount(t *testing.T, path string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(path+".files", "archives"))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func TestSemanticEventReusesRawWindowWithoutWrapperOrFormattingRules(t *testing.T) {
	s, path := openTest(t)
	nativeArchiveFixture(t, s)
	text := strings.Repeat("原始😀\n", 500)
	raw, _ := json.Marshal(map[string]any{"id": "source", "text": text, "nullable": nil})
	archiveNativeChunk(t, s, "semantic-original", 0, append(append([]byte(nil), raw...), '\n'))
	alternate := `{ "text":` + mustJSONString(t, text) + `, "nullable":null, "id":"source" }`
	body := `{"sequence":1,"thread_id":"thread","native":{"Kind":"item_completed","TurnID":"turn","Raw":` + alternate + `,"ItemID":"","Tool":null,"Failure":null,"Text":` + mustJSONString(t, text) + `}}`
	stored := retainEventFixture(t, s, body)
	if hash, ok := EventOriginalSHA256(stored); !ok || hash != bodyHash([]byte(body)) {
		t.Fatal("original event hash changed")
	}
	if archiveCount(t, path) != 1 {
		t.Fatal("wrapper or rule created another original")
	}
	if !SemanticJSONEqual(resolveEventFixture(t, s, stored), body) {
		t.Fatal("semantic content changed")
	}
	if strings.Contains(stored, text) || strings.Contains(stored, `"Failure":null`) {
		t.Fatal("raw content/defaults duplicated in DB")
	}
	before := archiveCount(t, path)
	reordered := `{"thread_id":"thread","sequence":1,"native":{"TurnID":"turn","Raw":` + string(raw) + `,"Tool":null,"Text":` + mustJSONString(t, text) + `,"ItemID":"","Kind":"item_completed","Failure":null}}`
	if !SemanticJSONEqual(resolveEventFixture(t, s, retainEventFixture(t, s, reordered)), body) {
		t.Fatal("formatting changed event meaning")
	}
	if archiveCount(t, path) != before {
		t.Fatal("key order created a new original")
	}
	var rules int
	if err := s.read.QueryRowContext(t.Context(), `SELECT count(*) FROM retained_original_values WHERE recipe_window_id IS NOT NULL`).Scan(&rules); err != nil || rules != 0 {
		t.Fatal("formatting recipe retained", rules, err)
	}
}

func TestSemanticEventKeepsJSONTextNullAndMissingAcrossRestart(t *testing.T) {
	s, path := openTest(t)
	text := `{ "z": "` + strings.Repeat("tool😀", 500) + `", "a": null }`
	body := `{"native":{"Kind":"item_completed","Raw":null,"Text":"","Tool":{"Input":` + mustJSONString(t, text) + `,"Output":` + mustJSONString(t, text) + `}},"null":null,"array":[]}`
	stored := retainEventFixture(t, s, body)
	if !SemanticJSONEqual(resolveEventFixture(t, s, stored), body) {
		t.Fatal("JSON-looking text or null changed")
	}
	var counts int
	if err := s.read.QueryRowContext(t.Context(), `SELECT count(*) FROM retained_original_values`).Scan(&counts); err != nil || counts != 1 {
		t.Fatal("same opaque text retained twice", counts, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(t.Context(), path, testkit.NewClock().Now)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	full := resolveEventFixture(t, s, stored)
	if !SemanticJSONEqual(full, body) {
		t.Fatal("restart changed semantic content")
	}
	var decoded struct {
		Native struct {
			Tool struct{ Input, Output string }
		}
	}
	if json.Unmarshal([]byte(full), &decoded) != nil || decoded.Native.Tool.Input != text || decoded.Native.Tool.Output != text {
		t.Fatal("text bytes changed")
	}
	if err = s.Read(t.Context(), func(tx *ReadTx) error {
		part, total, e := JSONTextWindow(t.Context(), tx.Q(), stored, 3, 37)
		if e == nil && (part != string([]rune(full)[3:40]) || total != int64(len([]rune(full)))) {
			t.Fatal("Unicode window changed")
		}
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if SemanticJSONEqual(full, strings.Replace(body, `"null":null,`, "", 1)) || SemanticJSONEqual(`{"a":null}`, `{"a":[]}`) {
		t.Fatal("missing/null/array conflated")
	}
}

func TestSemanticEventOriginalPrecedesReferenceAndCorruptionIsError(t *testing.T) {
	s, path := openTest(t)
	text := strings.Repeat("原件", 2000)
	raw, _ := json.Marshal(map[string]any{"id": "provider-item", "text": text})
	body := `{"native":{"Kind":"item_completed","Raw":` + string(raw) + `,"Tool":{"Output":` + mustJSONString(t, text) + `}}}`
	stored := retainEventFixture(t, s, body)
	if !SemanticJSONEqual(resolveEventFixture(t, s, stored), body) {
		t.Fatal("new original changed")
	}
	mustExec(t, s, `INSERT INTO agent_rollout_events(conversation_id,thread_id,kind,data_json,created_at) VALUES('main','thread','sample',?,1)`, stored)
	if err := s.Read(t.Context(), func(tx *ReadTx) error {
		columns, e := originalColumnsFor(t.Context(), tx.Q())
		if e != nil {
			return e
		}
		roots, e := readLegacyRuleRoots(t.Context(), tx.Q(), columns)
		if e == nil && len(roots.compact) == 0 {
			t.Fatal("structured original links omitted from business roots")
		}
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if archiveCount(t, path) != 1 {
		t.Fatal("raw and its text created duplicate originals")
	}
	var archive string
	var length int64
	if err := s.read.QueryRowContext(t.Context(), `SELECT a.archive_id,MAX(w.byte_offset+w.byte_length) FROM retained_original_windows w JOIN retained_original_archives a USING(archive_key) GROUP BY a.archive_id`).Scan(&archive, &length); err != nil {
		t.Fatal(err)
	}
	if length != int64(len(raw)) {
		t.Fatal("text copied outside provider original", length, len(raw))
	}
	file := filepath.Join(path+".files", "archives", archive)
	if err := os.Truncate(file, length-1); err != nil {
		t.Fatal(err)
	}
	if err := s.Read(t.Context(), func(tx *ReadTx) error { _, e := ResolveJSON(t.Context(), tx.Q(), stored); return e }); err == nil {
		t.Fatal("corrupt original reported success")
	}
}

func TestSemanticEventMigrationDoesNotArchiveMachineStateOrAdvanceCoverage(t *testing.T) {
	s, path := openTest(t)
	body := `{ "kind":"running", "attempt":"run", "explicit":null, "array":[] }`
	mustExec(t, s, `INSERT INTO agent_rollout_events(conversation_id,thread_id,kind,data_json,created_at) VALUES('main','thread','state',?,1)`, body)
	for range 10 {
		if !migrateOriginalBatch(t, s, 2).More {
			break
		}
	}
	var stored string
	if err := s.read.QueryRowContext(t.Context(), `SELECT data_json FROM agent_rollout_events`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !SemanticJSONEqual(resolveEventFixture(t, s, stored), body) {
		t.Fatal("machine fact changed")
	}
	if archiveCount(t, path) != 0 {
		t.Fatal("small metadata became an original")
	}
	status, err := s.OriginalStorageState(t.Context())
	if err != nil || status.PendingRows != 0 {
		t.Fatal(status, err)
	}
}
func mustJSONString(t *testing.T, text string) string {
	t.Helper()
	raw, err := json.Marshal(text)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
