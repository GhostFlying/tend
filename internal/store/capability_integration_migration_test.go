package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/testkit"
)

func TestCapabilityMigrationAppendsAfterMainHistory(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "ledger.db")
	db, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	old := &Store{write: db, read: db, now: testkit.NewClock().Now}
	if err := old.migrate(ctx, 97); err != nil {
		t.Fatal(err)
	}
	const payload = `{ "opaque": "original-unknown" }`
	if _, err := db.ExecContext(ctx, `INSERT INTO request_records(domain,request_id,operation,payload_hash,principal_json,trace_id,status,result_json,created_at,updated_at) VALUES('owner','original','operation','hash','{}','trace','unknown',?,1,1)`, payload); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, path, old.now)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var status, result string
	if err := db.QueryRowContext(ctx, `SELECT status,result_json FROM request_records WHERE request_id='original'`).Scan(&status, &result); err != nil || status != "unknown" || result != payload {
		t.Fatal("upgrade rewrote original unknown request", status, result, err)
	}
	for _, table := range []string{"retained_run_continuations", "decision_continuations", "agent_compaction_runtimes", "run_acceptance_evidence_contracts", "capability_definition_versions", "run_capability_bindings"} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil || count != 1 {
			t.Fatal("missing integrated table", table, count, err)
		}
	}
	var revision int64
	if err := db.QueryRowContext(ctx, `SELECT revision FROM capability_definition_versions WHERE capability_id='image_gen'`).Scan(&revision); err != nil || revision != 1 {
		t.Fatal(revision, err)
	}
	if err := old.migrate(ctx, 97); err == nil || !strings.Contains(err.Error(), "newer than this binary") {
		t.Fatal("old main reader accepted the new storage version", err)
	}
}

func TestCapabilityMigrationRejectsOldCandidateNumberCollision(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "candidate.db")
	db, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	old := &Store{write: db, read: db, now: testkit.NewClock().Now}
	if err := old.migrate(ctx, 87); err != nil {
		t.Fatal(err)
	}
	body, err := migrationFS.ReadFile("migrations/0098_declarative_capabilities.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, string(body)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations VALUES(88,'executor_capabilities',1)`); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, path, old.now)
	if err == nil {
		s.Close()
		t.Fatal("accepted conflicting candidate migration history")
	}
	if !strings.Contains(err.Error(), "migration history conflict") {
		t.Fatal("missing precise collision diagnostic", err)
	}
	var version, definitions int
	if err := db.QueryRowContext(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil || version != 88 {
		t.Fatal("rejected open changed version", version, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM capability_definition_versions`).Scan(&definitions); err != nil || definitions != 1 {
		t.Fatal("rejected open changed candidate history", definitions, err)
	}
}
