package store

import (
	"database/sql"
	"github.com/GhostFlying/tend/internal/testkit"
	"path/filepath"
	"testing"
)

func TestUsageAndControlIndexesMigratePopulatedSchema82(t *testing.T) {
	ctx := t.Context()
	clock := testkit.NewClock()
	db, err := sql.Open("sqlite", dsn(filepath.Join(t.TempDir(), "historical.db"), true))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	s := &Store{write: db, read: db, now: clock.Now}
	if err = s.migrate(ctx, 82); err != nil {
		t.Fatal(err)
	}
	const payload = `{"original":"immutable request and usage evidence"}`
	if err = s.Write(ctx, func(tx *Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO request_records(domain,request_id,operation,payload_hash,principal_json,trace_id,status,result_json,created_at,updated_at) VALUES('owner','original-request','original.operation','original-hash','{}','original-trace','unknown',?,1,1)`, payload); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO token_usage_facts(source,request_id,revision,payload_hash,payload_json,subject,role,received_at) VALUES('legacy','original-usage',1,'original-hash',?,'owner','owner',1)`, payload)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var history string
	if err = db.QueryRowContext(ctx, `SELECT group_concat(name,'|') FROM schema_migrations WHERE version<=82`).Scan(&history); err != nil {
		t.Fatal(err)
	}
	if err = s.migrate(ctx, 84); err != nil {
		t.Fatal(err)
	}
	// Reopening this historical index migration must not replay its indexes.
	// Later compact storage migrations have their own semantic preservation test.
	if err = s.migrate(ctx, 84); err != nil {
		t.Fatal(err)
	}
	var after, request, usage string
	if err = db.QueryRowContext(ctx, `SELECT group_concat(name,'|') FROM schema_migrations WHERE version<=82`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRowContext(ctx, `SELECT result_json FROM request_records WHERE domain='owner' AND request_id='original-request'`).Scan(&request); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRowContext(ctx, `SELECT payload_json FROM token_usage_facts WHERE request_id='original-usage'`).Scan(&usage); err != nil {
		t.Fatal(err)
	}
	if after != history || request != payload || usage != payload {
		t.Fatal("migration changed original facts")
	}
	for _, version := range []int{83, 84} {
		var n int
		if err = db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE version=?`, version).Scan(&n); err != nil || n != 1 {
			t.Fatal(version, n, err)
		}
	}
	for _, name := range []string{"token_usage_legacy_candidates_native", "token_usage_current_fact", "request_records_identity_status"} {
		var n int
		if err = db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE type='index' AND name=?`, name).Scan(&n); err != nil || n != 1 {
			t.Fatal(name, n, err)
		}
	}
}
