package store

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func compactUsageHistoricalStore(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", dsn(filepath.Join(t.TempDir(), "historical.db"), true))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	s := &Store{write: db, read: db, now: testkit.NewClock().Now}
	if err := s.migrate(t.Context(), 119); err != nil {
		t.Fatal(err)
	}
	return s, db
}

func TestCompactUsageMigrationPreservesTypedFactsConflictsAndBindings(t *testing.T) {
	s, db := compactUsageHistoricalStore(t)
	zero, n, at := int64(0), int64(47), int64(1000)
	sample := protocol.TokenUsageSample{Source: "native-ledger/provider/session", RecordID: "original-record", RequestID: "response", ThreadID: "thread", TurnID: "turn", RootTurnID: "root", Revision: 1, Settlement: "final", Profile: "profile", Model: "model", Provider: "provider", NativeContentHash: "original-content", NativeVersion: "version", SessionID: "session", Ordinal: &zero, ModelSource: "native-turn-context", TimeSource: "native", CounterSource: "native", Purpose: "owner", ParentSessionID: "parent", ParentThreadID: "parent-thread", OccurredAt: &at, Input: &n, CacheRead: &zero, CacheWrite: nil, Output: &zero, Total: &n}
	for variant := range 2 {
		v := sample
		if variant == 1 {
			v.Output = &n // A real contradictory counter variant must survive.
		}
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(t.Context(), `INSERT INTO token_usage_facts(source,request_id,revision,payload_hash,payload_json,subject,role,received_at) VALUES(?,?,1,?,?, 'session:session','native',2000)`, v.Source, v.RecordID, string(rune('a'+variant)), string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{
		`INSERT INTO token_usage_current VALUES('native-ledger/provider/session','original-record',1,1)`,
		`INSERT INTO token_usage_bindings(source,request_id,run_id,conversation_id,host_id,proof,attribution_conflict) VALUES('native-ledger/provider/session','original-record','run','conversation','host','recorded-native-turn',1)`,
		`INSERT INTO token_usage_binding_revisions(source,request_id,run_id,conversation_id,host_id,proof,created_at) VALUES('native-ledger/provider/session','original-record','run','conversation','host','recorded-native-turn',1)`,
		`INSERT INTO token_usage_price_catalogs VALUES(10,'{"data":[{"id":"old"}]}'),(20,'{"data":[{"id":"current"}]}')`,
	} {
		if _, err := db.ExecContext(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.migrate(t.Context(), 120); err != nil {
		t.Fatal(err)
	}
	for variant := range 2 {
		var r protocol.TokenUsageRecord
		if err := db.QueryRowContext(t.Context(), `SELECT `+UsageFactColumns("f")+` FROM token_usage_fact_details f WHERE seq=?`, variant+1).Scan(UsageFactDestinations(&r)...); err != nil {
			t.Fatal(err)
		}
		want := sample
		if variant == 1 {
			want.Output = &n
		}
		if !reflect.DeepEqual(r.TokenUsageSample, want) || r.Subject != "session:session" || r.Role != "native" || r.ReceivedAt != 2000 || r.Seq != int64(variant+1) {
			t.Fatalf("migration changed real measurement: %+v, want %+v", r, want)
		}
	}
	var facts, contexts, conflict, bindingConflict, dirty int
	if err := db.QueryRowContext(t.Context(), `SELECT (SELECT COUNT(*) FROM token_usage_facts),(SELECT COUNT(*) FROM token_usage_contexts),(SELECT conflict FROM token_usage_current),(SELECT attribution_conflict FROM token_usage_bindings),(SELECT COUNT(*) FROM token_usage_dirty)`).Scan(&facts, &contexts, &conflict, &bindingConflict, &dirty); err != nil {
		t.Fatal(err)
	}
	if facts != 2 || contexts != 1 || conflict != 1 || bindingConflict != 1 || dirty != 1 {
		t.Fatal("lost variants, shared metadata, conflict or rebuild watermark", facts, contexts, conflict, bindingConflict, dirty)
	}
	var quoted int64
	var catalog string
	if err := db.QueryRowContext(t.Context(), `SELECT collected_at,catalog_json FROM token_usage_latest_prices`).Scan(&quoted, &catalog); err != nil || quoted != 20 || catalog != `{"data":[{"id":"current"}]}` {
		t.Fatal("latest valid quote was not retained", quoted, catalog, err)
	}
	var redundant int
	if err := db.QueryRowContext(t.Context(), `SELECT (SELECT COUNT(*) FROM sqlite_schema WHERE name='token_usage_price_catalogs')+(SELECT COUNT(*) FROM pragma_table_info('token_usage_facts') WHERE name IN ('payload_json','payload_hash'))+(SELECT COUNT(*) FROM pragma_table_info('token_usage_contributions') WHERE name IN ('record_json','dimension_json','totals_json'))`).Scan(&redundant); err != nil || redundant != 0 {
		t.Fatal("redundant persistent ledger remains", redundant, err)
	}
	rows, err := db.QueryContext(t.Context(), `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() {
		t.Fatal("migration broke a historical fact reference")
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(t.Context(), 120); err != nil {
		t.Fatal("reopen repeated a completed migration", err)
	}
}

func TestCompactUsageMigrationRollsBackInvalidHistoricalCounter(t *testing.T) {
	s, db := compactUsageHistoricalStore(t)
	const raw = `{"source":"original","request_id":"request","total":"invalid"}`
	if _, err := db.ExecContext(t.Context(), `INSERT INTO token_usage_facts(source,request_id,revision,payload_hash,payload_json,subject,role,received_at) VALUES('original','request',1,'hash',?,'conversation:main','main',1)`, raw); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(t.Context(), 120); err == nil {
		t.Fatal("invalid counter was discarded or coerced into a measurement")
	}
	var got string
	var version int
	if err := db.QueryRowContext(t.Context(), `SELECT payload_json,(SELECT MAX(version) FROM schema_migrations) FROM token_usage_facts`).Scan(&got, &version); err != nil || got != raw || version != 119 {
		t.Fatal("failed migration modified the original or advanced its watermark", got, version, err)
	}
}
