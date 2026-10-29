package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"modernc.org/sqlite"
)

func databaseURI(path, mode string, immutable bool) string {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	q := url.Values{}
	q.Set("mode", mode)
	if immutable {
		q.Set("immutable", "1")
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func openSnapshot(ctx context.Context, path string) (*sql.DB, error) {
	if _, err := regularFile(path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", databaseURI(path, "ro", true))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err = db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// snapshotFromCopy allows recovery only in a disposable copy, never the source.
func snapshotFromCopy(ctx context.Context, source, destination string) (result error) {
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", databaseURI(source, "rw", false))
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	defer func() { result = errors.Join(result, db.Close()) }()
	connection, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	err = connection.Raw(func(raw any) error {
		backupper, ok := raw.(interface {
			NewBackup(string) (*sqlite.Backup, error)
		})
		if !ok {
			return errors.New("archive: SQLite backup unavailable")
		}
		backup, err := backupper.NewBackup(databaseURI(destination, "rwc", false))
		if err != nil {
			return err
		}
		for {
			if err = ctx.Err(); err != nil {
				break
			}
			var more bool
			more, err = backup.Step(128)
			if err != nil || !more {
				break
			}
		}
		return errors.Join(err, backup.Finish())
	})
	err = errors.Join(err, connection.Close())
	if err != nil {
		return fmt.Errorf("archive: snapshot backup: %w", err)
	}
	// Finalize the destination independently. Only the working/destination copies
	// are writable; the immutable reader is opened after both have been closed.
	target, err := sql.Open("sqlite", databaseURI(destination, "rw", false))
	if err != nil {
		return err
	}
	target.SetMaxOpenConns(1)
	var mode string
	err = target.QueryRowContext(ctx, "PRAGMA journal_mode=DELETE").Scan(&mode)
	if err == nil && mode != "delete" {
		err = fmt.Errorf("archive: snapshot retained journal mode %s", mode)
	}
	if err == nil {
		err = checkIntegrity(ctx, target)
	}
	err = errors.Join(err, target.Close())
	if err != nil {
		return err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(destination + suffix); !os.IsNotExist(err) {
			return fmt.Errorf("archive: snapshot sidecar remains: %s", suffix)
		}
	}
	file, err := os.Open(destination)
	if err != nil {
		return err
	}
	return errors.Join(file.Sync(), file.Close())
}

func checkIntegrity(ctx context.Context, db *sql.DB) error {
	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("archive: SQLite integrity: %s", result)
	}
	return nil
}

func inspectLedger(ctx context.Context, db *sql.DB) (string, []Migration, error) {
	rows, err := db.QueryContext(ctx, "SELECT version,name FROM schema_migrations ORDER BY version")
	if err != nil {
		return "", nil, err
	}
	var migrations []Migration
	for rows.Next() {
		var m Migration
		if err = rows.Scan(&m.Version, &m.Name); err != nil {
			break
		}
		if m.Version != len(migrations)+1 {
			err = errors.New("archive: incomplete migration sequence")
			break
		}
		migrations = append(migrations, m)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return "", nil, err
	}
	if len(migrations) < MinimumSchema || len(migrations) > MaximumSchema {
		return "", nil, fmt.Errorf("%w: %d (supports %d..%d)", ErrUnsupportedSchema, len(migrations), MinimumSchema, MaximumSchema)
	}
	var identity string
	if err = db.QueryRowContext(ctx, "SELECT deployment_id FROM deployment_identity WHERE singleton=1").Scan(&identity); err != nil {
		return "", nil, err
	}
	if identity == "" {
		return "", nil, errors.New("archive: missing deployment identity")
	}
	return identity, migrations, nil
}
