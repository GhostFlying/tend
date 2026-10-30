package app

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestAppFixtureMatchesFreshStore(t *testing.T) {
	clock := testkit.NewClock()
	fresh, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "fresh.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fresh.Close() })
	copy, err := openAppFixture(t.Context(), filepath.Join(t.TempDir(), "copy.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { copy.Close() })
	snapshot := func(db *store.Store) []string {
		t.Helper()
		var result []string
		err := db.Read(t.Context(), func(tx *store.ReadTx) error {
			rows, err := tx.Q().QueryContext(t.Context(), `SELECT type,name,tbl_name,COALESCE(sql,'') FROM sqlite_schema ORDER BY type,name`)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var kind, name, table, sql string
				if err := rows.Scan(&kind, &name, &table, &sql); err != nil {
					return err
				}
				result = append(result, strings.Join([]string{kind, name, table, sql}, "\n"))
			}
			if err := rows.Err(); err != nil {
				return err
			}
			rows.Close()
			rows, err = tx.Q().QueryContext(t.Context(), `SELECT version,name,applied_at FROM schema_migrations ORDER BY version`)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var version int
				var name string
				var appliedAt int64
				if err := rows.Scan(&version, &name, &appliedAt); err != nil {
					return err
				}
				result = append(result, fmt.Sprintf("%d:%s:%d", version, name, appliedAt))
			}
			return rows.Err()
		})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	if !reflect.DeepEqual(snapshot(fresh), snapshot(copy)) {
		t.Fatal("copied fixture differs from the freshly migrated schema or migration history")
	}
	if err := copy.Read(t.Context(), func(tx *store.ReadTx) error {
		var integrity string
		if err := tx.Q().QueryRowContext(t.Context(), `PRAGMA integrity_check`).Scan(&integrity); err != nil {
			return err
		}
		if integrity != "ok" {
			return fmt.Errorf("fixture integrity: %s", integrity)
		}
		rows, err := tx.Q().QueryContext(t.Context(), `PRAGMA foreign_key_check`)
		if err != nil {
			return err
		}
		defer rows.Close()
		if rows.Next() {
			return fmt.Errorf("copied fixture has a foreign key violation")
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAppFixturesKeepWritesAndFilesIsolated(t *testing.T) {
	first := newHarness(t)
	if err := first.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO inbox(source,source_event_id,received_at) VALUES('fixture-isolation','first',0)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	object, err := first.store.FileStore().Put(t.Context(), strings.NewReader("only the first fixture owns this original"))
	if err != nil {
		t.Fatal(err)
	}
	for i := range 8 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				var count int
				if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM inbox WHERE source='fixture-isolation'`).Scan(&count); err != nil {
					return err
				}
				if count != 0 {
					return fmt.Errorf("fixture inherited %d writes from another copy", count)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			// Every copy can insert the same unique key independently.
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				_, err := tx.Exec(t.Context(), `INSERT INTO inbox(source,source_event_id,received_at) VALUES('fixture-isolation','parallel-copy',0)`)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			f, err := h.store.FileStore().Open(object.SHA256)
			if err == nil {
				f.Close()
				t.Fatal("fixture shares another copy's original-file storage")
			}
			if !os.IsNotExist(err) {
				t.Fatal(err)
			}
		})
	}
}
