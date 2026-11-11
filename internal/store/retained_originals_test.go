package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func retainedSample(t *testing.T) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"type": "sample", "thread_id": "original-thread", "text": strings.Repeat("中文😀abc", 6000)})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
func insertNativeOriginalFixture(t *testing.T, s *Store) {
	t.Helper()
	insertTask(t, s, "body-task")
	if err := insertAction(t, s, "body-action", "body-task", "body-effect"); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s, `INSERT INTO runs(run_id,task_id,action_id,generation,role,desired_state,observed_state,created_at,updated_at) VALUES('body-run','body-task','body-action',1,'issue_manager','run','running',0,0)`)
}
func migrateOriginalBatch(t *testing.T, s *Store, limit int) protocol.OriginalMigrationResult {
	t.Helper()
	state, err := s.OriginalStorageState(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx, batch, err := s.PrepareOriginalMigration(t.Context(), limit)
	if err != nil {
		t.Fatal(err)
	}
	var result protocol.OriginalMigrationResult
	err = s.Write(ctx, func(tx *Tx) error {
		var err error
		result, err = tx.ApplyOriginalMigration(ctx, batch, state.Revision, 1)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestOriginalImportIndexesWholeRolloutWithoutFragmentCopies(t *testing.T) {
	s, path := openTest(t)
	ctx := t.Context()
	body := retainedSample(t)
	line := []byte(`{"type":"response_item","payload":` + body + "}\n")
	obj, err := s.files.Put(ctx, bytes.NewReader(line))
	if err != nil {
		t.Fatal(err)
	}
	err = s.Write(ctx, func(tx *Tx) error {
		return tx.InsertConversationAttachment(ctx, ConversationAttachmentRow{ConversationAttachment: protocol.ConversationAttachment{FileID: "original-upload", Name: "rollout.jsonl", MediaType: "application/jsonl", Size: obj.Size, SHA256: obj.SHA256}})
	})
	if err != nil {
		t.Fatal(err)
	}
	p := protocol.OriginalImport{FileID: "original-upload", SourceKey: "native-session", Limit: 1}
	batch, err := s.PrepareOriginalImport(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	err = s.Write(ctx, func(tx *Tx) error {
		result, err := tx.ApplyOriginalImport(ctx, batch, 1, 1)
		if err == nil && (result.More || result.Next != int64(len(line)) || result.IndexedBodies < 2) {
			t.Fatalf("import=%+v", result)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	staged, err := s.StageOriginals(ctx, "new-event-stream", []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	err = s.Write(staged, func(tx *Tx) error {
		encoded, err := tx.RetainJSON(staged, body)
		if err != nil {
			return err
		}
		if len(encoded) > 1000 {
			t.Fatalf("body retained inline: %d", len(encoded))
		}
		_, err = tx.Exec(staged, `INSERT INTO agent_rollout_events(conversation_id,thread_id,turn_id,item_id,kind,data_json,created_at) VALUES('main','native-session','turn','item','item_completed',?,1)`, encoded)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(path+".files", "archives")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("import created fragment archive: %v", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(ctx, path, testkit.NewClock().Now)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	err = restored.Read(ctx, func(tx *ReadTx) error {
		page, err := AgentRollout(ctx, tx.Q(), "main", "", 0)
		if err != nil {
			return err
		}
		if len(page.Records) != 1 || !page.Records[0].Truncated || page.Records[0].Characters != int64(len([]rune(body))) {
			t.Fatalf("page=%+v", page)
		}
		window, err := AgentRolloutWindow(ctx, tx.Q(), "main", 1, 15999)
		if err != nil {
			return err
		}
		want := string([]rune(body)[15999:31999])
		if window.Text != want || window.Next != 31999 {
			t.Fatalf("Unicode window changed: next=%d", window.Next)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	verified, err := restored.VerifyOriginals(ctx, "", 100)
	if err != nil || verified.Checked < 2 || verified.More {
		t.Fatalf("verify=%+v err=%v", verified, err)
	}
}

func TestOriginalMigrationRestartPreservesPendingCommandAndUnknown(t *testing.T) {
	s, path := openTest(t)
	ctx := t.Context()
	insertNativeOriginalFixture(t, s)
	payload := retainedSample(t)
	mustExec(t, s, `INSERT INTO manager_native_requests(request_id,run_id,method,payload_json,status,created_at) VALUES('native-command','body-run','inject',?,'unknown',1)`, payload)
	staged, batch, err := s.PrepareOriginalMigration(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("interruption before commit")
	err = s.Write(staged, func(tx *Tx) error {
		if _, err := tx.ApplyOriginalMigration(staged, batch, 1, 2); err != nil {
			return err
		}
		return injected
	})
	if !errors.Is(err, injected) {
		t.Fatal(err)
	}
	var saved string
	if err = s.read.QueryRowContext(ctx, `SELECT payload_json FROM manager_native_requests`).Scan(&saved); err != nil || saved != payload {
		t.Fatalf("rollback changed input: %v", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path, testkit.NewClock().Now)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for range 8 {
		if !migrateOriginalBatch(t, reopened, 1).More {
			break
		}
	}
	state, err := reopened.OriginalStorageState(ctx)
	if err != nil || state.PendingRows != 0 || state.MigratedRows != 1 || state.Bodies < 1 {
		t.Fatalf("state=%+v error=%v", state, err)
	}
	err = reopened.Read(ctx, func(tx *ReadTx) error {
		var status, compact string
		if err := tx.Q().QueryRowContext(ctx, `SELECT status,payload_json FROM manager_native_requests`).Scan(&status, &compact); err != nil {
			return err
		}
		if status != "unknown" || len(compact) > 1000 {
			t.Fatal("unknown intent lost or body retained inline")
		}
		full, err := ResolveJSON(ctx, tx.Q(), compact)
		if err == nil && full != payload {
			t.Fatal("frozen input changed")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestOriginalMigrationCorruptionRollsBackAndBackupRestores(t *testing.T) {
	s, path := openTest(t)
	ctx := t.Context()
	body := retainedSample(t)
	mustExec(t, s, `INSERT INTO agent_rollout_events(conversation_id,thread_id,kind,data_json,created_at) VALUES('main','original-thread','sample',?,1)`, body)
	staged, batch, err := s.PrepareOriginalMigration(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	prepared := staged.Value(preparedOriginalKey{}).(map[string]preparedOriginal)[bodyHash([]byte(body))]
	archive := filepath.Join(path+".files", "archives", prepared.Window.Archive)
	original, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	damaged := append([]byte(nil), original...)
	damaged[0] = '!'
	if err = os.WriteFile(archive, damaged, 0600); err != nil {
		t.Fatal(err)
	}
	err = s.Write(staged, func(tx *Tx) error { _, err := tx.ApplyOriginalMigration(staged, batch, 1, 1); return err })
	if err == nil {
		t.Fatal("corrupt original accepted")
	}
	var stored string
	if err = s.read.QueryRowContext(ctx, `SELECT data_json FROM agent_rollout_events`).Scan(&stored); err != nil || stored != body {
		t.Fatalf("body discarded after failed verification: %v", err)
	}
	if err = os.WriteFile(archive, original, 0600); err != nil {
		t.Fatal(err)
	}
	migrateOriginalBatch(t, s, 1)
	clock := testkit.NewClock()
	id := testkit.NewIDGen(clock, 41).New(protocol.PrefixRequest)
	receipt, err := s.BackupManagers(ctx, id)
	if err != nil || receipt.Objects != 1 {
		t.Fatalf("backup=%+v err=%v", receipt, err)
	}
	// The backup is self-contained, including exact original prefixes. Appending
	// or removing live originals does not change the immutable saved snapshot.
	backupPath := filepath.Join(path+".manager-backups", id, "ledger.db")
	backup, err := Open(ctx, backupPath, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if err = os.Remove(archive); err != nil {
		t.Fatal(err)
	}
	if _, err = s.VerifyOriginals(ctx, "", 10); err == nil {
		t.Fatal("missing live original reported as complete")
	}
	if _, err = backup.VerifyOriginals(ctx, "", 10); err != nil {
		t.Fatal(err)
	}
	err = backup.Read(ctx, func(tx *ReadTx) error {
		page, err := AgentRollout(ctx, tx.Q(), "main", "", 0)
		if err != nil {
			return err
		}
		if page.Records[0].Characters != int64(len([]rune(body))) {
			t.Fatal("backup original differs")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLiteralReservedOriginalMarkerCannotBecomeAFileReference(t *testing.T) {
	s, _ := openTest(t)
	ctx := t.Context()
	literal := `{"tend:retained-json:v1":{"archive":"not-a-real-reference"},"text":"ordinary owner input"}`
	staged, err := s.StageOriginals(ctx, "literal-input", []byte(literal))
	if err != nil {
		t.Fatal(err)
	}
	err = s.Write(staged, func(tx *Tx) error {
		encoded, err := tx.RetainJSON(staged, literal)
		if err != nil {
			return err
		}
		if encoded == literal {
			t.Fatal("reserved input not escaped")
		}
		restored, err := ResolveJSON(staged, tx.Q(), encoded)
		if err != nil {
			return err
		}
		if restored != literal {
			t.Fatal("literal marker changed meaning")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestOriginalBodyStorageSpaceAccounting(t *testing.T) {
	s, path := openTest(t)
	ctx := t.Context()
	insertNativeOriginalFixture(t, s)
	payload := retainedSample(t)
	for i := range 8 {
		id := fmt.Sprintf("native-command-%d", i)

		mustExec(t, s, `INSERT INTO manager_native_requests(request_id,run_id,method,payload_json,status,created_at) VALUES(?,'body-run','inject',?,'unknown',1)`, id, payload)
	}
	if _, err := s.write.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	for range 8 {
		result := migrateOriginalBatch(t, s, 10)
		if !result.More {
			break
		}
	}
	elapsed := time.Since(started)
	if _, err := s.write.ExecContext(ctx, `VACUUM`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.write.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	var fileBytes int64
	if err = filepath.WalkDir(path+".files", func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			fileBytes += info.Size()
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if after.Size()+fileBytes >= before.Size() {
		t.Fatalf("space accounting failed: beforeDB=%d afterDB=%d originals=%d", before.Size(), after.Size(), fileBytes)
	}
	t.Logf("synthetic fixture: before_db_bytes=%d after_db_bytes=%d originals_bytes=%d backup_bytes=0 total_after_bytes=%d migration_elapsed=%s; no production size claim", before.Size(), after.Size(), fileBytes, after.Size()+fileBytes, elapsed)
}

func TestOriginalNativeReplyMigrationSharesExactResultAndPreservesLongErrors(t *testing.T) {
	s, _ := openTest(t)
	ctx := t.Context()
	insertNativeOriginalFixture(t, s)
	reply := protocol.ManagerNativeReply{ID: "models-command", Result: json.RawMessage(`{"models":[{"id":"model","description":"` + strings.Repeat("description", 2000) + `"}]}`), Error: strings.Repeat("original native failure ", 100)}
	raw, err := json.Marshal(reply)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s, `INSERT INTO manager_native_requests(request_id,run_id,method,payload_json,result_json,status,created_at) VALUES('models-command','body-run','models','{}',?,'unknown',1)`, string(raw))
	for range 4 {
		result := migrateOriginalBatch(t, s, 1)
		if !result.More {
			break
		}
	}
	state, err := s.OriginalStorageState(ctx)
	if err != nil || state.PendingRows != 0 || state.Bodies < 1 || state.MigratedRows != 1 {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	err = s.Read(ctx, func(tx *ReadTx) error {
		var stored string
		if err := tx.Q().QueryRowContext(ctx, `SELECT result_json FROM manager_native_requests WHERE request_id='models-command'`).Scan(&stored); err != nil {
			return err
		}
		if len(stored) > 1000 {
			t.Fatal("raw error or results copied into projection")
		}

		var restored protocol.ManagerNativeReply
		if err = DecodeJSON(ctx, tx.Q(), stored, &restored); err != nil {
			return err
		}
		if string(restored.Result) != string(reply.Result) || restored.Error != reply.Error {
			t.Fatal("models array or original error lost")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
