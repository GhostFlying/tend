// Package store owns the SQLite ledger: connection setup, embedded
// migrations and transaction helpers. Only the app layer opens write
// transactions.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/blobstore"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Retain a small read burst without repeatedly initializing SQLite's schema
// state. This bounds idle resources without limiting concurrent readers.
const readIdleConnections = 8

// Store holds a single-connection pool for writes and a pool for reads.
// SQLite allows one writer at a time; funnelling writes through one
// connection with BEGIN IMMEDIATE avoids lock upgrade deadlocks and makes
// concurrent writers queue in order.
type Store struct {
	path             string
	files            *blobstore.Store
	write            *sql.DB
	read             *sql.DB
	now              func() time.Time
	observer         *writeObserver
	writeDiagnostics writeDiagnostics
}

// Open opens or creates the database at path and applies pending
// migrations. now supplies the time recorded for applied migrations.
func Open(ctx context.Context, dbPath string, now func() time.Time) (*Store, error) {
	return OpenWithOptions(ctx, dbPath, now)
}

// OpenWithOptions adds internal diagnostic options while Open retains its
// original function type for callers that inject the store constructor.
func OpenWithOptions(ctx context.Context, dbPath string, now func() time.Time, opts ...OpenOption) (*Store, error) {
	write, err := sql.Open("sqlite", dsn(dbPath, true))
	if err != nil {
		return nil, fmt.Errorf("store: open writer: %w", err)
	}
	write.SetMaxOpenConns(1)
	read, err := sql.Open("sqlite", dsn(dbPath, false))
	if err != nil {
		write.Close()
		return nil, fmt.Errorf("store: open reader: %w", err)
	}
	read.SetMaxIdleConns(readIdleConnections)
	files, err := blobstore.New(dbPath + ".files")
	if err != nil {
		write.Close()
		read.Close()
		return nil, err
	}
	s := &Store{path: dbPath, write: write, read: read, now: now, files: files}
	for _, opt := range opts {
		opt(s)
	}
	if err := s.migrate(ctx, 0); err != nil {
		s.Close()
		return nil, err
	}
	if err := s.exportLegacyBlobs(ctx); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// FileStore returns the ledger's immutable original-file store.
func (s *Store) FileStore() *blobstore.Store { return s.files }

func dsn(dbPath string, writer bool) string {
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "synchronous(NORMAL)")
	if writer {
		q.Set("_txlock", "immediate")
	} else {
		q.Add("_pragma", "query_only(1)")
	}
	u := url.URL{Scheme: "file", OmitHost: true, Path: filepath.ToSlash(dbPath), RawQuery: q.Encode()}
	return u.String()
}

// Close closes both pools.
func (s *Store) Close() error {
	return errors.Join(s.write.Close(), s.read.Close())
}

// Tx is a write transaction.
type Tx struct {
	changed     map[string]bool
	files       *blobstore.Store
	tx          *sql.Tx
	observation *writeCall
}

// ReadTx is a read-only transaction.
type ReadTx struct {
	tx    *sql.Tx
	files *blobstore.Store
}

// Querier is the subset of query methods shared by Tx and ReadTx.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Q returns the transaction's query interface.
func (t *Tx) Q() Querier { return fileQuerier{Querier: t.tx, files: t.files} }

// Exec runs a statement inside the transaction.
func (t *Tx) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return t.tx.ExecContext(ctx, query, args...)
}

// Q returns the transaction's query interface.
func (t *ReadTx) Q() Querier { return fileQuerier{Querier: t.tx, files: t.files} }

// Write runs fn in a write transaction that begins immediately. The
// transaction commits when fn returns nil and rolls back otherwise.
func (s *Store) Write(ctx context.Context, fn func(*Tx) error) (err error) {
	observation := s.startWriteObservation(ctx)
	defer func() { s.finishWriteObservation(observation, err) }()
	conn, err := s.write.Conn(ctx)
	if err != nil {
		observation.endStage("")
		s.failedWriteWait(observation)
		return fmt.Errorf("store: begin write: %w", err)
	}
	defer func() {
		conn.Close()
		s.releasedWrite(observation)
	}()
	observation.endStage("begin_immediate")
	s.acquiredWrite(observation)
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		observation.endStage("")
		return fmt.Errorf("store: begin write: %w", err)
	}
	// Always finish the transaction before closing the explicit connection,
	// including when the callback panics. After commit Rollback is a no-op.
	defer func() {
		// This is a cleanup attempt, possibly a no-op after Commit or an
		// automatic rollback. It does not assert that a SQLite lock is held.
		observation.setLiveStage("rollback_cleanup")
		var started time.Time
		if observation != nil && err != nil {
			started = observation.observer.now()
		}
		rbErr := tx.Rollback()
		if err != nil {
			if observation != nil {
				observation.observation.Rollback = observation.observer.now().Sub(started)
			}
			if rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
				err = errors.Join(err, fmt.Errorf("store: rollback: %w", rbErr))
			}
		}
	}()
	observation.endStage("callback")
	if err := fn(&Tx{tx: tx, files: s.files, observation: observation}); err != nil {
		observation.failedContext(ctx, err)
		observation.endStage("")
		return err
	}
	observation.endStage("commit")
	if err := tx.Commit(); err != nil {
		observation.failedContext(ctx, err)
		observation.endStage("")
		return fmt.Errorf("store: commit: %w", err)
	}
	observation.endStage("")
	return nil
}

// Read runs fn in a read-only transaction with a consistent snapshot.
func (s *Store) Read(ctx context.Context, fn func(*ReadTx) error) error {
	tx, err := s.read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("store: begin read: %w", err)
	}
	// A read-only transaction has nothing to keep; its rollback error is
	// irrelevant.
	defer func() { _ = tx.Rollback() }()
	return fn(&ReadTx{tx: tx, files: s.files})
}

type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("store: list migrations: %w", err)
	}
	var out []migration
	for _, e := range entries {
		base := strings.TrimSuffix(e.Name(), ".sql")
		num, name, ok := strings.Cut(base, "_")
		version, convErr := strconv.Atoi(num)
		if !ok || convErr != nil || version <= 0 {
			return nil, fmt.Errorf("store: migration %q must be named NNNN_name.sql", e.Name())
		}
		body, err := migrationFS.ReadFile(path.Join("migrations", e.Name()))
		if err != nil {
			return nil, fmt.Errorf("store: read migration %q: %w", e.Name(), err)
		}
		out = append(out, migration{version: version, name: name, sql: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	for i, m := range out {
		if m.version != i+1 {
			return nil, fmt.Errorf("store: migration versions must be contiguous from 1, found %d at position %d", m.version, i+1)
		}
	}
	return out, nil
}

// migrate applies the pending migrations up to version upTo, or all of
// them when upTo is 0. Only tests stop early.
func (s *Store) migrate(ctx context.Context, upTo int) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	if upTo > 0 {
		migrations = migrations[:upTo]
	}
	return s.Write(ctx, func(tx *Tx) error {
		if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER NOT NULL PRIMARY KEY,
			name       TEXT    NOT NULL,
			applied_at INTEGER NOT NULL
		) STRICT`); err != nil {
			return fmt.Errorf("store: create schema_migrations: %w", err)
		}
		var current int
		if err := tx.Q().QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
			return fmt.Errorf("store: read schema version: %w", err)
		}
		if current > len(migrations) {
			return fmt.Errorf("store: database schema version %d is newer than this binary (%d)", current, len(migrations))
		}
		// A version number alone cannot identify an unreleased branch's history.
		// Refuse collisions before applying SQL; never rewrite the recorded source.
		rows, err := tx.Q().QueryContext(ctx, `SELECT version,name FROM schema_migrations ORDER BY version`)
		if err != nil {
			return fmt.Errorf("store: read migration identities: %w", err)
		}
		seen := 0
		for rows.Next() {
			var version int
			var name string
			if err := rows.Scan(&version, &name); err != nil {
				rows.Close()
				return fmt.Errorf("store: scan migration identity: %w", err)
			}
			seen++
			if version != seen || name != migrations[version-1].name {
				rows.Close()
				return fmt.Errorf("store: migration history conflict at version %d", version)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("store: read migration identities: %w", err)
		}
		rows.Close()
		for _, m := range migrations[current:] {
			if _, err := tx.Exec(ctx, m.sql); err != nil {
				return fmt.Errorf("store: apply migration %04d_%s: %w", m.version, m.name, err)
			}
			if m.version == 122 {
				if err := migrateRetiredRequests(ctx, tx); err != nil {
					return fmt.Errorf("store: retire request ledgers: %w", err)
				}
			}
			if m.version == 124 {
				if err := migratePublishedBackgroundTools(ctx, tx); err != nil {
					return fmt.Errorf("store: retire published background tools: %w", err)
				}
			}
			if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
				m.version, m.name, s.now().UnixMilli()); err != nil {
				return fmt.Errorf("store: record migration %04d: %w", m.version, err)
			}
		}
		return nil
	})
}

// IsUniqueViolation reports whether err is a SQLite unique or primary key
// constraint failure.
func IsUniqueViolation(err error) bool {
	se, ok := errors.AsType[*sqlite.Error](err)
	if !ok {
		return false
	}
	code := se.Code()
	return code == sqlite3.SQLITE_CONSTRAINT_UNIQUE || code == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY
}

// MarkChanged records a transaction-local view invalidation. It is discarded on rollback.
func (t *Tx) MarkChanged(view string) {
	if t.changed == nil {
		t.changed = map[string]bool{}
	}
	t.changed[view] = true
}
func (t *Tx) Changed(view string) bool { return t.changed[view] }
