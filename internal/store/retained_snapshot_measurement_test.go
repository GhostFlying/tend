package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

// TestOriginalMigrationHistoricalSnapshot is explicitly opt-in. It reads a
// frozen source, requires its supplied digest, and only opens a private copy for
// writing. It does not have credentials or contact any service.
func TestOriginalMigrationHistoricalSnapshot(t *testing.T) {
	source := os.Getenv("TEND_STORAGE_SNAPSHOT_SOURCE")
	expected := os.Getenv("TEND_STORAGE_SNAPSHOT_SHA256")
	if source == "" || expected == "" {
		t.Skip("set frozen source and SHA256 to measure a disposable local copy")
	}
	input, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	path := filepath.Join(t.TempDir(), "ledger.db")
	copy, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(copy, hash), input)
	if err != nil {
		copy.Close()
		t.Fatal(err)
	}
	if err = copy.Close(); err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(hash.Sum(nil)) != expected {
		t.Fatal("frozen source digest differs; disposable copy not opened")
	}
	var maxWrite time.Duration
	s, err := OpenWithOptions(context.Background(), path, testkit.NewClock().Now, WithWriteObserver(time.Now, 0, func(observation WriteObservation) {
		if observation.Callback > maxWrite {
			maxWrite = observation.Callback
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	initial, err := s.OriginalStorageState(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	batches := 0
	revision := initial.Revision
	for batches < 10000 {
		ctx, batch, err := s.PrepareOriginalMigration(t.Context(), 100)
		if err != nil {
			t.Fatal(err)
		}
		var result protocol.OriginalMigrationResult
		if err = s.Write(ctx, func(tx *Tx) error {
			var err error
			result, err = tx.ApplyOriginalMigration(ctx, batch, revision, 1)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		revision = result.Status.Revision
		batches++
		if !result.More {
			break
		}
	}
	elapsed := time.Since(started)
	if batches == 10000 {
		t.Fatal("migration did not converge")
	}
	final, err := s.OriginalStorageState(t.Context())
	if err != nil || final.PendingRows != 0 {
		t.Fatalf("final=%+v err=%v", final, err)
	}
	if _, err = s.write.ExecContext(t.Context(), `VACUUM`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.write.ExecContext(t.Context(), `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	var originals int64
	if err = filepath.WalkDir(path+".files", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			originals += info.Size()
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for cursor := ""; ; {
		verified, err := s.VerifyOriginals(t.Context(), cursor, 1000)
		if err != nil {
			t.Fatal(err)
		}
		cursor = verified.Next
		if !verified.More {
			break
		}
	}
	t.Logf("frozen_snapshot_sha256=%s before_db_bytes=%d after_db_bytes=%d new_original_bytes=%d existing_attachment_bytes=unavailable backup_bytes=not_measured combined_known_after_bytes=%d initial_pending=%d migrated_rows=%d unmatched_rows=%d batches=%d elapsed=%s max_writer_callback=%s; existing attachment originals absent from this DB-only snapshot, so total fleet bytes and full backup are not claimed", expected, n, after.Size(), originals, after.Size()+originals, initial.PendingRows, final.MigratedRows, final.UnmatchedRows, batches, elapsed, maxWrite)
}
