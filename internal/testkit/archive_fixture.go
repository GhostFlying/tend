package testkit

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// ArchiveLedger57 constructs a historical fixture by applying exactly the real
// first 57 migrations to a new database. It never opens a current store and
// rewrites its version; callers seed the old schema explicitly with SQL.
func ArchiveLedger57(ctx context.Context, path, migrations string) (*sql.DB, error) {
	u := url.URL{Scheme: "file", Path: path}
	q := url.Values{}
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "journal_mode(WAL)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	failed := func(err error) (*sql.DB, error) { _ = db.Close(); return nil, err }
	if _, err = db.ExecContext(ctx, `CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,name TEXT NOT NULL,applied_at INTEGER NOT NULL) STRICT`); err != nil {
		return failed(err)
	}
	for version := 1; version <= 57; version++ {
		names, err := filepath.Glob(filepath.Join(migrations, fmt.Sprintf("%04d_*.sql", version)))
		if err != nil {
			return failed(err)
		}
		if len(names) != 1 {
			return failed(fmt.Errorf("historical fixture: migration %d is missing or ambiguous", version))
		}
		body, err := os.ReadFile(names[0])
		if err != nil {
			return failed(err)
		}
		if _, err = db.ExecContext(ctx, string(body)); err != nil {
			return failed(fmt.Errorf("historical fixture migration %d: %w", version, err))
		}
		name := strings.TrimSuffix(filepath.Base(names[0])[5:], ".sql")
		if _, err = db.ExecContext(ctx, `INSERT INTO schema_migrations VALUES(?,?,0)`, version, name); err != nil {
			return failed(err)
		}
	}
	return db, nil
}
