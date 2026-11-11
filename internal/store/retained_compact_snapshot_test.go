package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/blobstore"
	"github.com/GhostFlying/tend/internal/testkit"
)

// This opt-in measurement opens only its newly copied fixture and deliberately
// stops at S1's schema. Other work packages are measured by their own callers.
func TestCompactOriginalHistoricalSnapshot(t *testing.T) {
	source := os.Getenv("TEND_COMPACT_ORIGINAL_SNAPSHOT")
	if source == "" {
		t.Skip("explicit frozen DB and adjacent files required")
	}
	expected := os.Getenv("TEND_COMPACT_ORIGINAL_SNAPSHOT_SHA256")
	directory := os.Getenv("TEND_COMPACT_ORIGINAL_WORKDIR")
	if len(expected) != 64 || !filepath.IsAbs(source) || !filepath.IsAbs(directory) {
		t.Fatal("explicit SHA and absolute source/exclusive workdir required")
	}
	resume := os.Getenv("TEND_COMPACT_ORIGINAL_RESUME") == "1"
	if !resume {
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
	} else {
		owner, err := os.ReadFile(filepath.Join(directory, "FIXTURE-SOURCE-SHA256"))
		if err != nil || string(owner) != expected+"\n" {
			t.Fatal("existing fixture source identity not confirmed", err)
		}
	}
	path := filepath.Join(directory, "ledger.db")
	copyFile := func(from, to string) error {
		input, err := os.Open(from)
		if err != nil {
			return err
		}
		defer input.Close()
		output, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = io.Copy(output, input)
		if err != nil {
			output.Close()
			return err
		}
		return output.Close()
	}
	if !resume {
		if err := copyFile(source, path); err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.New()
		_, err = io.Copy(hash, f)
		f.Close()
		if err != nil || hex.EncodeToString(hash.Sum(nil)) != expected {
			t.Fatal("frozen source differs", err)
		}
		if err = filepath.WalkDir(source+".files", func(name string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(source+".files", name)
			if err != nil {
				return err
			}
			to := filepath.Join(path+".files", rel)
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("fixture contains symlink")
			}
			if entry.IsDir() {
				return os.MkdirAll(to, 0700)
			}
			if !entry.Type().IsRegular() {
				return fmt.Errorf("fixture contains special file")
			}
			return copyFile(name, to)
		}); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(directory, "FIXTURE-SOURCE-SHA256"), []byte(expected+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	writer, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		t.Fatal(err)
	}
	writer.SetMaxOpenConns(1)
	reader, err := sql.Open("sqlite", dsn(path, false))
	if err != nil {
		writer.Close()
		t.Fatal(err)
	}
	files, err := blobstore.New(path + ".files")
	if err != nil {
		writer.Close()
		reader.Close()
		t.Fatal(err)
	}
	s := &Store{path: path, write: writer, read: reader, files: files, now: testkit.NewClock().Now}
	defer s.Close()
	allocated := func() map[string]int64 {
		result := map[string]int64{}
		for _, key := range []string{"page_count", "page_size", "freelist_count"} {
			var n int64
			if err := reader.QueryRowContext(ctx, "PRAGMA "+key).Scan(&n); err != nil {
				t.Fatal(err)
			}
			result[key] = n
		}
		return result
	}
	beforePages := allocated()
	if err = s.migrate(ctx, 119); err != nil {
		t.Fatal(err)
	}
	digest := func(ledger *Store) map[string]any {
		result := map[string]any{}
		if err := ledger.Read(ctx, func(tx *ReadTx) error {
			for _, column := range originalColumns {
				rows, err := tx.Q().QueryContext(ctx, `SELECT rowid,`+column.column+` FROM `+column.table+` WHERE 1=1`+column.where+` ORDER BY rowid`)
				if err != nil {
					return err
				}
				sum := sha256.New()
				count := 0
				for rows.Next() {
					var id int64
					var value sql.NullString
					if err = rows.Scan(&id, &value); err != nil {
						rows.Close()
						return err
					}
					raw := value.String
					if column.table == "background_replies" && column.column == "tool_json" && raw != "" {
						raw, err = ResolveToolRecord(ctx, tx.Q(), raw)
					} else {
						raw, err = ResolveJSON(ctx, tx.Q(), raw)
					}
					if err != nil {
						rows.Close()
						return err
					}
					fmt.Fprintf(sum, "%d:%t:%d:", id, value.Valid, len(raw))
					sum.Write([]byte(raw))
					count++
				}
				if err = rows.Err(); err != nil {
					rows.Close()
					return err
				}
				if err = rows.Close(); err != nil {
					return err
				}
				result[column.table+"."+column.column] = map[string]any{"rows": count, "sha256": hex.EncodeToString(sum.Sum(nil))}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return result
	}
	if info, e := os.Stat(source + "-wal"); e == nil && info.Size() > 0 {
		t.Fatal("frozen source must be a standalone consistent DB")
	}
	u := url.URL{Scheme: "file", Path: source}
	v := u.Query()
	v.Set("mode", "ro")
	v.Set("immutable", "1")
	u.RawQuery = v.Encode()
	originalDB, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer originalDB.Close()
	before := digest(&Store{read: originalDB, files: s.files})
	baseline, _ := json.MarshalIndent(before, "", "  ")
	if err = os.WriteFile(filepath.Join(directory, "BODY-BASELINE.json"), append(baseline, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	initial, err := s.OriginalStorageState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	revision := initial.Revision
	batches := 0
	for batches < 10000 {
		staged, batch, err := s.PrepareOriginalMigration(ctx, 100)
		if err != nil {
			t.Fatal(err)
		}
		more := false
		if err = s.Write(staged, func(tx *Tx) error {
			result, err := tx.ApplyOriginalMigration(staged, batch, revision, 1)
			revision = result.Status.Revision
			more = result.More
			return err
		}); err != nil {
			t.Fatal(err)
		}
		batches++
		if batches%50 == 0 {
			t.Logf("migrated batches=%d revision=%d", batches, revision)
		}
		if !more {
			break
		}
	}
	if batches == 10000 {
		t.Fatal("migration did not converge")
	}
	after := digest(s)
	b, _ := json.Marshal(before)
	a, _ := json.Marshal(after)
	if string(a) != string(b) {
		t.Fatal("historical body/page identity digest changed")
	}
	status, err := s.OriginalStorageState(ctx)
	if err != nil {
		t.Fatal(status, err)
	}
	// Schema 119 cannot yet retire cache-only rules. Its status deliberately
	// reports those deferred rows; every actual business field must be done.
	for _, field := range status.Fields {
		if field.Table != "retained_body_recipes" && field.PendingRows != 0 {
			t.Fatal("business body migration is incomplete", field)
		}
	}
	verified := 0
	cursor := ""
	for {
		v, err := s.VerifyOriginals(ctx, cursor, 1000)
		if err != nil {
			t.Fatal(err)
		}
		verified += v.Checked
		cursor = v.Next
		if !v.More {
			break
		}
	}
	// The copy alone is compacted after body readback and cursor validation.
	if _, err = writer.ExecContext(ctx, `VACUUM`); err != nil {
		t.Fatal(err)
	}
	if _, err = writer.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	var filesBytes int64
	if err = filepath.WalkDir(path+".files", func(_ string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.Type().IsRegular() {
			info, err := e.Info()
			if err != nil {
				return err
			}
			filesBytes += info.Size()
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	originalHash := sha256.New()
	_, err = io.Copy(originalHash, original)
	original.Close()
	if err != nil || hex.EncodeToString(originalHash.Sum(nil)) != expected {
		t.Fatal("frozen source changed", err)
	}
	report := map[string]any{"source_sha256": expected, "source_unchanged": true, "migration_target": 119, "before_pages": beforePages, "after_pages": allocated(), "after_db_bytes": info.Size(), "after_file_bytes": filesBytes, "after_total_bytes": info.Size() + filesBytes, "batches": batches, "elapsed_ms": time.Since(start).Milliseconds(), "verified_windows_and_values": verified, "status": status, "body_digests": after, "production_operations": 0, "exclusive_physical_bytes": "unknown; ordinary copies used; no COW claim"}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, "REPORT.json"), append(encoded, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("report=%s batches=%d database_bytes=%d original_files_bytes=%d", filepath.Join(directory, "REPORT.json"), batches, info.Size(), filesBytes)
}
