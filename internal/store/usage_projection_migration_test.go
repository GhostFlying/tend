package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/testkit"
)

func TestUsageProjectionMigrationPreservesFactsAndQueuesBackfill(t *testing.T) {
	ctx := t.Context()
	clock := testkit.NewClock()
	db, err := sql.Open("sqlite", dsn(filepath.Join(t.TempDir(), "historical.db"), true))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	s := &Store{write: db, read: db, now: clock.Now}
	if err = s.migrate(ctx, 90); err != nil {
		t.Fatal(err)
	}
	const raw = `{"source":"original","request_id":"request","thread_id":"thread","revision":1,"settlement":"final","total":7}`
	if err = s.Write(ctx, func(tx *Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO token_usage_facts(source,request_id,revision,payload_hash,payload_json,subject,role,received_at) VALUES('original','request',1,'hash',?,'conversation:main','main',1)`, raw); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO token_usage_current(source,request_id,fact_seq) VALUES('original','request',1)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err = s.migrate(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if err = s.migrate(ctx, 0); err != nil {
		t.Fatal(err)
	}
	var got, source, id string
	if err = db.QueryRowContext(ctx, `SELECT payload_json FROM token_usage_facts WHERE seq=1`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRowContext(ctx, `SELECT source,request_id FROM token_usage_dirty`).Scan(&source, &id); err != nil {
		t.Fatal(err)
	}
	if got != raw || source != "original" || id != "request" {
		t.Fatal("migration rewrote facts or lost backfill", got, source, id)
	}
}
