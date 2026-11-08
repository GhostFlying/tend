package codexenv

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Execution references grow independently of control requests. Keeping them in
// an indexed local database avoids rewriting run history during every launch.
func adoptionDB(ctx context.Context, dir string, create bool) (*sql.DB, error) {
	path := filepath.Join(dir, "adoptions.db")
	if !create {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return nil, ErrNotFound
		} else if err != nil {
			return nil, err
		}
	}
	dsn := (&url.URL{Scheme: "file", Path: path}).String()
	if !create {
		dsn += "?mode=ro"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if create {
		_, err = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS adoptions(execution TEXT PRIMARY KEY,generation TEXT NOT NULL,state TEXT NOT NULL,body BLOB NOT NULL); CREATE INDEX IF NOT EXISTS adoptions_state ON adoptions(state);`)
		if err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return db, nil
}
func readAdoption(ctx context.Context, dir, key string) (Adoption, bool, error) {
	var out Adoption
	db, err := adoptionDB(ctx, dir, false)
	if errors.Is(err, ErrNotFound) {
		return out, false, nil
	}
	if err != nil {
		return out, false, err
	}
	defer db.Close()
	var raw []byte
	err = db.QueryRowContext(ctx, "SELECT body FROM adoptions WHERE execution=?", key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return out, false, nil
	}
	if err != nil {
		return out, false, err
	}
	err = json.Unmarshal(raw, &out)
	return out, err == nil, err
}
func writeAdoption(ctx context.Context, dir string, a Adoption) error {
	db, err := adoptionDB(ctx, dir, true)
	if err != nil {
		return err
	}
	defer db.Close()
	raw, err := json.Marshal(a)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO adoptions(execution,generation,state,body) VALUES(?,?,?,?) ON CONFLICT(execution) DO UPDATE SET generation=excluded.generation,state=excluded.state,body=excluded.body`, a.Execution, a.Generation, a.State, raw)
	return err
}
func adoptionPage(ctx context.Context, dir, after string, limit int) (map[string]Adoption, string, bool, error) {
	out := map[string]Adoption{}
	db, err := adoptionDB(ctx, dir, false)
	if errors.Is(err, ErrNotFound) {
		return out, "", false, nil
	}
	if err != nil {
		return nil, "", false, err
	}
	defer db.Close()
	if limit == 0 {
		limit = 100
	}
	rows, err := db.QueryContext(ctx, "SELECT execution,body FROM adoptions WHERE execution>? ORDER BY execution LIMIT ?", after, limit+1)
	if err != nil {
		return nil, "", false, err
	}
	defer rows.Close()
	next := ""
	for rows.Next() {
		var key string
		var raw []byte
		if err = rows.Scan(&key, &raw); err != nil {
			return nil, "", false, err
		}
		if len(out) == limit {
			return out, next, true, nil
		}
		var a Adoption
		if err = json.Unmarshal(raw, &a); err != nil {
			return nil, "", false, err
		}
		out[key] = a
		next = key
	}
	return out, "", false, rows.Err()
}
func activeAdoptions(ctx context.Context, dir string) (bool, error) {
	db, err := adoptionDB(ctx, dir, false)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer db.Close()
	var active bool
	err = db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM adoptions WHERE state IN ('running','initialized','ready','unknown','pinned','pending_restart'))`).Scan(&active)
	return active, err
}
