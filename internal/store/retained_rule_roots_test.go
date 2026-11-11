package store

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/blobstore"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestLegacyRuleSweepDefersBeforeRetirementWithoutArchivingCache(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "ledger.db")
	db, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	files, err := blobstore.New(path + ".files")
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	s := &Store{path: path, write: db, read: db, files: files, now: testkit.NewClock().Now}
	defer s.Close()
	if err = s.migrate(ctx, 119); err != nil {
		t.Fatal(err)
	}
	// This is an obsolete response-cache wrapper. It has no retained business
	// root or original. Retirement must not try to archive/reconstruct it.
	hash := bodyHash([]byte("obsolete request cache"))
	mustExec(t, s, `INSERT INTO retained_body_recipes(sha256,recipe_json) VALUES(?,?)`, hash, `{"unused":"cache wrapper"}`)
	for range 3 {
		if !migrateOriginalBatch(t, s, 1).More {
			break
		}
	}
	status, err := s.OriginalStorageState(ctx)
	if err != nil || status.PendingRows != 1 || status.Bodies != 0 {
		t.Fatalf("119 must honestly defer cache-only rule: %+v, %v", status, err)
	}
	if err = s.migrate(ctx, 122); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if !migrateOriginalBatch(t, s, 1).More {
			break
		}
	}
	status, err = s.OriginalStorageState(ctx)
	if err != nil || status.PendingRows != 0 || status.Bodies != 0 || status.OriginalFiles != 0 {
		t.Fatalf("cache retirement created retained copies: %+v, %v", status, err)
	}
	var n int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM retained_original_values`).Scan(&n); err != nil || n != 0 {
		t.Fatal("cache wrapper became a retained body", n, err)
	}
}

func TestLegacyRuleSweepRechecksBusinessRootsAndPreservesMissingOriginal(t *testing.T) {
	s, _ := openTest(t)
	ctx := t.Context()
	ref := originalReference{SHA256: bodyHash([]byte("required original")), Window: &blobstore.Window{Archive: "missing-original", Length: 17, SHA256: bodyHash([]byte("required original"))}}
	raw, _ := json.Marshal(ref)
	mustExec(t, s, `INSERT INTO retained_body_recipes(sha256,recipe_json) VALUES(?,?)`, ref.SHA256, string(raw))
	state, err := s.OriginalStorageState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	staged, batch, err := s.PrepareOriginalMigration(ctx, 1)
	if err != nil || len(batch.rules) != 1 {
		t.Fatal("unreferenced cache rule was not selected", err)
	}
	marker, _ := json.Marshal(map[string]any{legacyRetainedMarker: ref})
	mustExec(t, s, `INSERT INTO agent_rollout_events(conversation_id,thread_id,kind,data_json,created_at) VALUES('main','thread','sample',?,1)`, string(marker))
	err = s.Write(staged, func(tx *Tx) error {
		_, err := tx.ApplyOriginalMigration(staged, batch, state.Revision, 2)
		return err
	})
	if protocol.CodeOf(err) != protocol.CodeStaleRevision {
		t.Fatal("new business reference was ignored", err)
	}
	if _, _, err = s.PrepareOriginalMigration(ctx, 1, true); err == nil {
		t.Fatal("missing business original was silently discarded")
	}
	var n int
	if err = s.read.QueryRowContext(ctx, `SELECT count(*) FROM retained_body_recipes WHERE sha256=?`, ref.SHA256).Scan(&n); err != nil || n != 1 {
		t.Fatal("required legacy rule was removed", n, err)
	}
}
