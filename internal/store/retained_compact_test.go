package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/blobstore"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestCompactOriginalMigratesLegacyRuleWithoutChangingPaginationOrCoverage(t *testing.T) {
	s, path := openTest(t)
	ctx := t.Context()
	nativeArchiveFixture(t, s)
	source := retainedSample(t)
	archiveNativeChunk(t, s, "legacy-rule", 0, []byte(`{"payload":`+source+"}\n"))
	body := `{"kind":"old-tool","payload":` + source + `}`
	p, err := s.prepareOriginalRecipe(ctx, "old-rule", []byte(body))
	if err != nil || p == nil {
		t.Fatal(err)
	}
	ref, err := json.Marshal(p.Reference)
	if err != nil {
		t.Fatal(err)
	}
	old, err := json.Marshal(map[string]any{legacyRetainedMarker: p.Reference})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s, `INSERT INTO retained_body_recipes(sha256,recipe_json) VALUES(?,?)`, bodyHash([]byte(body)), string(ref))
	mustExec(t, s, `INSERT INTO agent_rollout_events(conversation_id,thread_id,kind,data_json,created_at) VALUES('main','thread','sample',?,1)`, string(old))
	// Also verify that source digests/summary coverage are not revised merely by
	// replacing a body representation. This assistant text is a real source.
	mustExec(t, s, `INSERT INTO conversation_messages(message_id,author,body,created_at) VALUES('legacy-message','main_agent',?,1)`, string(old))
	var version int64
	if err = s.read.QueryRowContext(ctx, `SELECT version FROM conversation_coverage`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	staged, batch, err := s.PrepareOriginalMigration(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	aborted := errors.New("restart before reference commit")
	if err = s.Write(staged, func(tx *Tx) error {
		if _, err := tx.ApplyOriginalMigration(staged, batch, 1, 2); err != nil {
			return err
		}
		return aborted
	}); !errors.Is(err, aborted) {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path, testkit.NewClock().Now)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for range 12 {
		if !migrateOriginalBatch(t, s, 1).More {
			break
		}
	}
	status, err := s.OriginalStorageState(ctx)
	if err != nil || status.PendingRows != 0 || len(status.Fields) < 13 {
		t.Fatalf("coverage=%+v err=%v", status, err)
	}
	var rules, rewriting int
	var after int64
	if err = s.read.QueryRowContext(ctx, `SELECT count(*) FROM retained_body_recipes`).Scan(&rules); err != nil || rules != 0 {
		t.Fatal(rules, err)
	}
	if err = s.read.QueryRowContext(ctx, `SELECT rewriting FROM retained_body_migration`).Scan(&rewriting); err != nil || rewriting != 0 {
		t.Fatal(rewriting, err)
	}
	if err = s.read.QueryRowContext(ctx, `SELECT version FROM conversation_coverage`).Scan(&after); err != nil || after != version {
		t.Fatalf("physical migration changed coverage %d -> %d: %v", version, after, err)
	}
	err = s.Read(ctx, func(tx *ReadTx) error {
		page, err := AgentRollout(ctx, tx.Q(), "main", "", 0)
		if err != nil {
			return err
		}
		if len(page.Records) != 1 || page.Next != 1 || page.Records[0].Data != string([]rune(body)[:16000]) {
			t.Fatal("history pagination changed")
		}
		text, err := GetMessage(ctx, tx.Q(), "legacy-message")
		if err == nil && text.Body != body {
			t.Fatal("source content changed")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var preview string
	if err = s.read.QueryRowContext(ctx, `SELECT body_preview FROM conversation_messages WHERE message_id='legacy-message'`).Scan(&preview); err != nil || preview != string([]rune(body)[:160]) {
		t.Fatal("legacy marker leaked into preview", err)
	}
	var refJSON string
	if err = s.read.QueryRowContext(ctx, `SELECT data_json FROM agent_rollout_events`).Scan(&refJSON); err != nil || len(refJSON) > 512 || strings.Contains(refJSON, `"parts"`) {
		t.Fatal("embedded rule retained", len(refJSON), err)
	}
	var hashBytesCount int
	if err = s.read.QueryRowContext(ctx, `SELECT count(*) FROM retained_original_windows WHERE typeof(sha256)<>'blob' OR length(sha256)<>32 OR typeof(canonical_sha256)<>'blob'`).Scan(&hashBytesCount); err != nil || hashBytesCount != 0 {
		t.Fatal("noncompact hash", hashBytesCount, err)
	}
	// The rule pack is part of the ordinary backup roots, not a second body file.
	id := testkit.NewIDGen(testkit.NewClock(), 81).New(protocol.PrefixRequest)
	if _, err = s.BackupManagers(ctx, id); err != nil {
		t.Fatal(err)
	}
	backup, err := Open(ctx, filepath.Join(path+".manager-backups", id, "ledger.db"), testkit.NewClock().Now)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if err = os.RemoveAll(path + ".files"); err != nil {
		t.Fatal(err)
	}
	err = backup.Read(ctx, func(tx *ReadTx) error {
		full, err := ResolveJSON(ctx, tx.Q(), refJSON)
		if err == nil && full != body {
			t.Fatal("backup rule changed")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCompactToolTextReusesJSONStringWindowAndRejectsCorruptRule(t *testing.T) {
	s, path := openTest(t)
	ctx := t.Context()
	nativeArchiveFixture(t, s)
	text := strings.Repeat("tool 中文😀\n", 3000)
	encoded, _ := json.Marshal(text)
	line := []byte(`{"output":` + string(encoded) + "}\n")
	archiveNativeChunk(t, s, "tool-raw-text", 0, line)
	retained := retainNativeBody(t, s, text)
	if len(retained) > 100 {
		t.Fatal("tool original stored inline")
	}
	entries, err := os.ReadDir(filepath.Join(path+".files", "archives"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatal("existing rollout text was copied into another archive", len(entries))
	}
	raw := `{"envelope":` + string(encoded) + `}`
	compact := retainNativeBody(t, s, raw)
	var w blobstore.Window
	var stored string
	err = s.Read(ctx, func(tx *ReadTx) error {
		var id int64
		if err := tx.Q().QueryRowContext(ctx, `SELECT recipe_window_id FROM retained_original_values WHERE sha256=?`, hashBytes(bodyHash([]byte(raw)))).Scan(&id); err != nil {
			return err
		}
		var err error
		w, err = loadOriginalWindow(ctx, tx.Q(), id)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(path+".files", "archives", w.Archive)
	bytes, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	bytes[w.Offset] = '!'
	if err = os.WriteFile(file, bytes, 0600); err != nil {
		t.Fatal(err)
	}
	err = s.Read(ctx, func(tx *ReadTx) error { var e error; stored, e = ResolveJSON(ctx, tx.Q(), compact); return e })
	if err == nil || stored != "" {
		t.Fatal("corrupt rule returned a fake body", err)
	}
}

func TestCompactToolAcceptsNonJSONTextStartingWithObject(t *testing.T) {
	s, _ := openTest(t)
	raw := "{\"message\":\"an unescaped\nline followed by text" + strings.Repeat("plain tool output", 1000)
	retained := retainNativeBody(t, s, raw)
	if len(retained) > 100 {
		t.Fatal("raw non-JSON tool text not compacted")
	}
}
