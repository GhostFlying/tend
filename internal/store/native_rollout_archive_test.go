package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func nativeArchiveFixture(t *testing.T, s *Store) {
	t.Helper()
	insertNativeOriginalFixture(t, s)
	mustExec(t, s, `UPDATE runs SET host_id='archive-host',observed_state='stopped',desired_state='stop' WHERE run_id='body-run'`)
}
func archiveNativeChunk(t *testing.T, s *Store, source string, offset int64, data []byte) protocol.NativeRolloutArchiveReceipt {
	t.Helper()
	p, err := s.PrepareNativeRollout(t.Context(), "archive-host", protocol.NativeRolloutChunk{RunID: "body-run", SourceID: source, ThreadID: "archive-thread", Offset: offset, Data: data, SHA256: bodyHash(data)})
	if err != nil {
		t.Fatal(err)
	}
	var receipt protocol.NativeRolloutArchiveReceipt
	if err = s.Write(t.Context(), func(tx *Tx) error { var err error; receipt, err = tx.ApplyNativeRollout(t.Context(), p); return err }); err != nil {
		t.Fatal(err)
	}
	return receipt
}
func retainNativeBody(t *testing.T, s *Store, raw string) string {
	t.Helper()
	ctx, err := s.StageOriginals(t.Context(), "fallback-stream", []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	var result string
	if err = s.Write(ctx, func(tx *Tx) error { var err error; result, err = tx.RetainJSON(ctx, raw); return err }); err != nil {
		t.Fatal(err)
	}
	if err = s.Read(ctx, func(tx *ReadTx) error {
		got, err := ResolveJSON(ctx, tx.Q(), result)
		if err == nil && got != raw {
			t.Fatal("exact reconstructed original differs")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return result
}
func TestOriginalNativeArchivePartialBackupResumeAndCrossRunBinding(t *testing.T) {
	s, path := openTest(t)
	nativeArchiveFixture(t, s)
	// Preserve a half Unicode codepoint as accepted raw bytes without claiming a
	// complete JSONL record. Neither a JSON parser nor a runner-local path is used.
	body := retainedSample(t)
	line := []byte(`{"type":"response_item","payload":` + body + "}\n")
	split := bytes.Index(line, []byte("😀")) + 2
	first := archiveNativeChunk(t, s, "source-generation", 0, line[:split])
	if first.IndexedOffset != 0 || first.NextOffset != int64(split) {
		t.Fatalf("partial receipt=%+v", first)
	}
	// Put identical bytes in another source first: hash dedup must not cause the
	// first source's unindexed tail to disappear from its own backup prefix.
	archiveNativeChunk(t, s, "source-identical", 0, line[:split])
	clock := testkit.NewClock()
	id := testkit.NewIDGen(clock, 75).New(protocol.PrefixRequest)
	receipt, err := s.BackupManagers(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Objects != 2 {
		t.Fatalf("backup originals=%d", receipt.Objects)
	}
	backup, err := Open(t.Context(), filepath.Join(path+".manager-backups", id, "ledger.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	// Remove live originals to prove the restored database uses the backup files.
	if err = os.RemoveAll(path + ".files"); err != nil {
		t.Fatal(err)
	}
	final := archiveNativeChunk(t, backup, "source-generation", int64(split), line[split:])
	if final.IndexedOffset != int64(len(line)) || final.NextOffset != int64(len(line)) {
		t.Fatalf("final=%+v", final)
	}
	again := archiveNativeChunk(t, backup, "source-generation", 0, line[:split])
	if again != first {
		t.Fatalf("historical receipt changed: %+v != %+v", again, first)
	}
	retained := retainNativeBody(t, backup, body)
	if len(retained) > 1600 {
		t.Fatalf("body retained inline (%d)", len(retained))
	}
	mustExec(t, backup, `INSERT INTO runs(run_id,task_id,action_id,generation,role,desired_state,observed_state,host_id,created_at,updated_at) VALUES('body-run-2','body-task','body-action',2,'issue_manager','stop','stopped','archive-host',0,0)`)
	p, err := backup.PrepareNativeRollout(t.Context(), "archive-host", protocol.NativeRolloutChunk{RunID: "body-run-2", SourceID: "source-generation", ThreadID: "archive-thread", Offset: final.NextOffset, SHA256: bodyHash(nil)})
	if err != nil {
		t.Fatal(err)
	}
	if err = backup.Write(t.Context(), func(tx *Tx) error { _, err := tx.ApplyNativeRollout(t.Context(), p); return err }); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = backup.read.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM retained_native_source_runs WHERE source_id='source-generation'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("run binding=%d err=%v", count, err)
	}
}
func TestOriginalNativeArchiveCanonicalAndNestedValuesReuseSource(t *testing.T) {
	s, path := openTest(t)
	nativeArchiveFixture(t, s)
	text := strings.Repeat("正文😀", 1200)
	// Provider whitespace and key ordering differ from the normalized event.
	source := `{ "z": 3, "text": ` + string(mustOriginalJSON(t, text)) + `, "a": 1 }`
	line := []byte(`{"payload":` + source + "}\n")
	archiveNativeChunk(t, s, "source-canonical", 0, line)
	canonical, err := canonicalOriginal([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	retained := retainNativeBody(t, s, string(canonical))
	if !strings.Contains(retained, retainedMarker) || strings.Contains(retained, "canonical") || len(retained) > 100 {
		t.Fatalf("expected only compact source reference: %s", retained)
	}
	event := `{"Kind":"item_completed","Raw":` + string(canonical) + `,"Text":` + string(mustOriginalJSON(t, text)) + `}`
	retained = retainNativeBody(t, s, event)
	if len(retained) > 2500 {
		t.Fatalf("nested values copied into DB: %d", len(retained))
	}
	var total int64
	entries, err := os.ReadDir(filepath.Join(path+".files", "archives"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		total += info.Size()
	}
	if total <= int64(len(line)) || total > int64(len(line)+2500) {
		t.Fatalf("only a small shared rule should be added: archive=%d source=%d", total, len(line))
	}
	var count int
	if err = s.read.QueryRowContext(t.Context(), `SELECT count(*) FROM retained_original_values WHERE recipe_window_id IS NOT NULL`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("shared rule count=%d err=%v", count, err)
	}
	// Recipe cache survives closing/reopening and still validates original hashes.
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(t.Context(), path, testkit.NewClock().Now)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	retainNativeBody(t, restored, event)
}
func mustOriginalJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestOriginalNativeArchiveRejectsForeignHostConflictAndCorruption(t *testing.T) {
	s, path := openTest(t)
	nativeArchiveFixture(t, s)
	data := []byte("{\"text\":\"original\"}\n")
	p := protocol.NativeRolloutChunk{RunID: "body-run", SourceID: "source", ThreadID: "archive-thread", Data: data, SHA256: bodyHash(data)}
	if _, err := s.PrepareNativeRollout(t.Context(), "foreign-host", p); err == nil {
		t.Fatal("foreign host archived another Run")
	}
	archiveNativeChunk(t, s, "source", 0, data)
	p.Data = []byte("{\"text\":\"changed!\"}\n")
	p.SHA256 = bodyHash(p.Data)
	if _, err := s.PrepareNativeRollout(t.Context(), "archive-host", p); err == nil {
		t.Fatal("conflicting offset overwritten")
	}
	p.Offset = int64(len(data)) + 1
	if _, err := s.PrepareNativeRollout(t.Context(), "archive-host", p); err == nil {
		t.Fatal("offset gap accepted")
	}
	p.SourceID = "unseen"
	p.Data = nil
	p.SHA256 = bodyHash(nil)
	p.Offset = 0
	if _, err := s.PrepareNativeRollout(t.Context(), "archive-host", p); err == nil {
		t.Fatal("empty unknown source accepted")
	}
	var archive string
	if err := s.read.QueryRowContext(t.Context(), `SELECT archive_id FROM retained_native_sources WHERE source_id='source'`).Scan(&archive); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path+".files", "archives", archive), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	p.SourceID = "source"
	p.Data = data
	p.SHA256 = bodyHash(data)
	if _, err := s.PrepareNativeRollout(context.Background(), "archive-host", p); err == nil {
		t.Fatal("corrupt historical chunk replay accepted")
	}
}

func TestOriginalNativeArchiveChunkBoundaryAndCommitInterruption(t *testing.T) {
	s, path := openTest(t)
	nativeArchiveFixture(t, s)
	body := `{"text":"` + strings.Repeat("多😀", 150000) + `"}`
	data := []byte(body + "\n")
	var next int64
	for next < int64(len(data)) {
		end := min(int64(len(data)), next+protocol.MaxNativeRolloutChunkBytes)
		piece := data[next:end]
		if next == 0 {
			p, err := s.PrepareNativeRollout(t.Context(), "archive-host", protocol.NativeRolloutChunk{RunID: "body-run", SourceID: "large-source", ThreadID: "archive-thread", Offset: next, Data: piece, SHA256: bodyHash(piece)})
			if err != nil {
				t.Fatal(err)
			}
			interrupted := fmt.Errorf("interrupted before commit")
			if err = s.Write(t.Context(), func(tx *Tx) error {
				if _, err := tx.ApplyNativeRollout(t.Context(), p); err != nil {
					return err
				}
				return interrupted
			}); !errors.Is(err, interrupted) {
				t.Fatal(err)
			}
		}
		receipt := archiveNativeChunk(t, s, "large-source", next, piece)
		if end < int64(len(data)) && receipt.IndexedOffset != 0 {
			t.Fatal("incomplete large line indexed", receipt)
		}
		next = receipt.NextOffset
	}
	retained := retainNativeBody(t, s, body)
	if len(retained) > 2000 {
		t.Fatalf("large body inline: %d", len(retained))
	}
	var bytesOnDisk int64
	entries, err := os.ReadDir(filepath.Join(path+".files", "archives"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		bytesOnDisk += info.Size()
	}
	if bytesOnDisk != int64(len(data)) {
		t.Fatalf("interrupted stage duplicated bytes: %d vs %d", bytesOnDisk, len(data))
	}
}
