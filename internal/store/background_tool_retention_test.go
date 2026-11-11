package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/blobstore"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func backgroundToolFixture(t *testing.T, s *Store, item, input, output, status string, published bool) string {
	t.Helper()
	parent, batch, message := "parent-"+item, "batch-"+item, "tool-"+item
	mustExec(t, s, `INSERT INTO conversation_messages(message_id,author,body,created_at) VALUES(?,'owner','background',1)`, parent)
	mustExec(t, s, `INSERT INTO background_batches(batch_id,group_key,message_id,status,created_at) VALUES(?,?,?,'completed',1)`, batch, batch, parent)
	mustExec(t, s, `INSERT INTO conversation_messages(message_id,author,body,reply_to,kind,created_at) VALUES(?,'main_agent','',?,'tool_use',2)`, message, parent)
	mustExec(t, s, `INSERT INTO conversation_tool_uses(message_id,thread_id,turn_id,item_id,tool,input,output,status,updated_at) VALUES(?,'thread',?,?,'inspect',?,?,?,2)`, message, "turn-"+item, item, input, output, status)
	raw, err := json.Marshal(map[string]string{"reply_to": parent, "turn_id": "turn-" + item, "item_id": item, "tool": "inspect", "input": input, "output": output, "status": status})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s, `INSERT INTO background_replies(batch_id,thread_id,turn_id,item_id,body,kind,tool_json,status,request_id,created_at,published) VALUES(?,'thread',?,?,'','tool_use',?,?,'original-request',2,?)`, batch, "turn-"+item, item, string(raw), status, published)
	return string(raw)
}

func TestPublishedBackgroundToolRetirementRequiresSameCallAndRecoverableText(t *testing.T) {
	s, _ := openTest(t)
	full := strings.Repeat("x", 8192) + strings.Repeat("中文😀value", 2000)
	ref := retainNativeBody(t, s, full)
	clip := full[:8192] + fmt.Sprintf("\n[truncated: 8192 of %d bytes shown]", len(full))
	for _, mode := range []string{"same", "clip", "wrong-count", "different-input", "different-output", "different-thread", "different-tool", "different-status", "different-reply", "missing-tool", "unpublished", "unknown", "interrupted", "settled-start", "extra-field", "json-format"} {
		t.Run(mode, func(t *testing.T) {
			status := "completed"
			if mode == "unknown" || mode == "interrupted" {
				status = mode
			}
			if mode == "settled-start" {
				status = "interrupted"
			}
			raw := backgroundToolFixture(t, s, mode, `{}`, ref, status, mode != "unpublished")
			var p map[string]string
			if err := json.Unmarshal([]byte(raw), &p); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "clip":
				p["output"] = clip
			case "settled-start":
				p["status"] = "running"
			case "wrong-count":
				p["output"] = full[:8192] + "\n[truncated: 8192 of 999999 bytes shown]"
			case "different-input":
				p["input"] = `{"other":true}`
			case "different-output":
				p["output"] = "independent phase"
			case "different-thread":
				mustExec(t, s, `UPDATE conversation_tool_uses SET thread_id='other' WHERE item_id=?`, mode)
			case "different-tool":
				p["tool"] = "another"
			case "different-status":
				mustExec(t, s, `UPDATE conversation_tool_uses SET status='failed' WHERE item_id=?`, mode)
			case "different-reply":
				p["reply_to"] = "another"
			case "missing-tool":
				mustExec(t, s, `DELETE FROM conversation_tool_uses WHERE item_id=?`, mode)
			case "extra-field":
				p["independent"] = "phase fact"
			case "json-format":
				p["input"] = "{ }"
			}
			encoded, _ := json.Marshal(p)
			mustExec(t, s, `UPDATE background_replies SET tool_json=? WHERE item_id=?`, string(encoded), mode)
			var before, after string
			metadata := `SELECT json_array(seq,batch_id,thread_id,turn_id,item_id,body,kind,status,request_id,created_at,published) FROM background_replies WHERE item_id=?`
			if err := s.read.QueryRowContext(t.Context(), metadata, mode).Scan(&before); err != nil {
				t.Fatal(err)
			}
			var retired bool
			err := s.Write(t.Context(), func(tx *Tx) error {
				var err error
				retired, err = tx.RetirePublishedBackgroundTool(t.Context(), "batch-"+mode, "thread", "turn-"+mode, mode)
				return err
			})
			want := mode == "same" || mode == "clip" || mode == "unknown" || mode == "interrupted" || mode == "settled-start"
			if err != nil || retired != want {
				t.Fatalf("retired=%v want=%v: %v", retired, want, err)
			}
			var got string
			if err := s.read.QueryRowContext(t.Context(), `SELECT tool_json FROM background_replies WHERE item_id=?`, mode).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if want && got != "" || !want && got != string(encoded) {
				t.Fatal("retirement changed unproven data")
			}
			if err := s.read.QueryRowContext(t.Context(), metadata, mode).Scan(&after); err != nil || before != after {
				t.Fatal("publication metadata changed", err)
			}
		})
	}
}

func openBackgroundMigrationTest(t *testing.T, version int) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ledger.db")
	db, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	files, err := blobstore.New(path + ".files")
	if err != nil {
		t.Fatal(err)
	}
	s := &Store{path: path, write: db, read: db, files: files, now: testkit.NewClock().Now}
	t.Cleanup(func() { s.Close() })
	if err = s.migrate(t.Context(), version); err != nil {
		t.Fatal(err)
	}
	return s, path
}

func TestPublishedBackgroundMigrationRunsAfterNativePromotionBeforeBodyStaging(t *testing.T) {
	s, path := openBackgroundMigrationTest(t, 118)
	insertNativeOriginalFixture(t, s)
	full := strings.Repeat("a", 8192) + strings.Repeat("中文😀", 3000)
	clip := full[:8192] + fmt.Sprintf("\n[truncated: 8192 of %d bytes shown]", len(full))
	backgroundToolFixture(t, s, "promoted", `{}`, clip, "completed", true)
	payload, _ := json.Marshal(map[string]any{"thread_id": "thread", "turn_id": "turn-promoted", "call_id": "promoted", "tool_result": map[string]any{"Success": true, "Text": full}})
	reply, _ := json.Marshal(protocol.ManagerNativeReply{ID: "native-result"})
	mustExec(t, s, `INSERT INTO manager_native_requests(request_id,run_id,method,payload_json,result_json,status,created_at) VALUES('native-result','body-run','tool_result',?,?,'completed',2)`, string(payload), string(reply))
	unknown := `{"frozen":"unknown original"}`
	mustExec(t, s, `INSERT INTO manager_native_requests(request_id,run_id,method,payload_json,status,created_at) VALUES('unknown-command','body-run','inject',?,'unknown',2)`, unknown)
	if err := s.migrate(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
	var after int
	if err := s.read.QueryRowContext(t.Context(), `SELECT count(*) FROM retained_original_archives`).Scan(&after); err != nil || after != 0 {
		t.Fatal("retirement itself created an archive", after, err)
	}
	var cache, output, native, frozen string
	if err := s.read.QueryRowContext(t.Context(), `SELECT tool_json FROM background_replies WHERE item_id='promoted'`).Scan(&cache); err != nil || cache != "" {
		t.Fatal("published clip survived native promotion", cache, err)
	}
	if err := s.read.QueryRowContext(t.Context(), `SELECT output,native_result_output FROM conversation_tool_uses WHERE item_id='promoted'`).Scan(&output, &native); err != nil || output != full || native != full {
		t.Fatal("full callback was not shared", err)
	}
	if err := s.read.QueryRowContext(t.Context(), `SELECT payload_json FROM manager_native_requests WHERE request_id='unknown-command'`).Scan(&frozen); err != nil || frozen != unknown {
		t.Fatal("unknown command changed", err)
	}
	for range 8 {
		if !migrateOriginalBatch(t, s, 10).More {
			break
		}
	}
	var clipWindows int
	if err := s.read.QueryRowContext(t.Context(), `SELECT count(*) FROM retained_original_bodies WHERE sha256=?`, bodyHash([]byte(clip))).Scan(&clipWindows); err != nil || clipWindows != 0 {
		t.Fatal("retired clip was staged as a new original", clipWindows, err)
	}
	if _, err := os.Stat(filepath.Join(path+".files", "archives", bodyHash([]byte("legacy-tools")))); !os.IsNotExist(err) {
		t.Fatal("body staging created even an unreferenced legacy-tools pack", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(t.Context(), path, testkit.NewClock().Now)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.Read(t.Context(), func(tx *ReadTx) error {
		replies, err := TranscriptReplies(t.Context(), tx.Q(), "parent-promoted", 0, 10)
		if err == nil && (len(replies) != 1 || replies[0].Tool == nil || replies[0].Tool.Output != full) {
			t.Fatal("formal transcript lost its full original")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPublishedBackgroundMigrationCorruptOriginalRollsBack(t *testing.T) {
	s, path := openBackgroundMigrationTest(t, 123)
	first := backgroundToolFixture(t, s, "first", `{}`, "real output", "completed", true)
	backgroundToolFixture(t, s, "corrupt", `{}`, "body", "completed", true)
	// A real retained reference with an absent file must fail, not certify that
	// a cache is redundant because both bodies could not be read.
	missing := retainNativeBody(t, s, strings.Repeat("retained full output", 3000))
	mustExec(t, s, `UPDATE conversation_tool_uses SET output=? WHERE item_id='corrupt'`, missing)
	if err := os.RemoveAll(path + ".files"); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(t.Context(), 0); err == nil {
		t.Fatal("missing original was accepted")
	}
	var version int
	var got string
	if err := s.read.QueryRowContext(t.Context(), `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != 123 {
		t.Fatal("failed migration advanced schema", version, err)
	}
	if err := s.read.QueryRowContext(t.Context(), `SELECT tool_json FROM background_replies WHERE item_id='first'`).Scan(&got); err != nil || got != first {
		t.Fatal("failed migration partially retired an earlier cache", err)
	}
}
