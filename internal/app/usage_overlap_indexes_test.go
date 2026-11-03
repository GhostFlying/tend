package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
	"github.com/GhostFlying/tend/internal/views"
)

// Rebuild the real index SQL around populated facts and complete views.
// Store migration tests separately cover the actual schema82 upgrade; this
// fixture never rewrites newer migration history or drops unrelated tables.
func TestUsageOverlapIndexesPreserveHistoryAndCompleteViews(t *testing.T) {
	for _, state := range []string{"unresolved", "matched", "native_conflict", "unknown_counters"} {
		t.Run(state, func(t *testing.T) {
			ctx := t.Context()
			clock := testkit.NewClock()
			path := filepath.Join(t.TempDir(), "usage.db")
			db, err := store.Open(ctx, path, clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if db != nil {
					_ = db.Close()
				}
			})
			at, total := clock.Now().Add(-time.Hour).UnixMilli(), int64(10)
			sample := protocol.TokenUsageSample{Source: "legacy", RequestID: "response", ThreadID: "shared-thread", TurnID: "shared-turn", Revision: 1, Settlement: "final", Model: "fixture-model", OccurredAt: &at, Total: &total}
			if err := db.Write(ctx, func(tx *store.Tx) error {
				// Remove only the indexes being compared; preserve every migration record.
				for _, q := range []string{`DROP INDEX token_usage_legacy_candidates_native`, `DROP INDEX token_usage_current_fact`} {
					if _, err := tx.Exec(ctx, q); err != nil {
						return err
					}
				}
				var legacy int64
				for _, kind := range []string{"legacy", "native"} {
					id := kind + "-request"
					sample.Source = kind
					if kind == "native" {
						sample.RecordID = id
						total = 11
						if state == "unknown_counters" {
							sample.Total = nil
						}
					}
					raw, err := json.Marshal(sample)
					if err != nil {
						return err
					}
					hash := sha256.Sum256(raw)
					result, err := tx.Exec(ctx, `INSERT INTO token_usage_facts(source,request_id,revision,payload_hash,payload_json,subject,role,received_at) VALUES(?,?,1,?,?,'run:shared','implement',?)`, kind, id, hex.EncodeToString(hash[:]), string(raw), clock.Now().UnixMilli())
					if err != nil {
						return err
					}
					seq, err := result.LastInsertId()
					if err != nil {
						return err
					}
					conflict := 0
					if kind == "native" && state == "native_conflict" || kind == "legacy" && state == "unknown_counters" {
						conflict = 1
					}
					if _, err := tx.Exec(ctx, `INSERT INTO token_usage_current(source,request_id,fact_seq,conflict) VALUES(?,?,?,?)`, kind, id, seq, conflict); err != nil {
						return err
					}
					if kind == "legacy" {
						legacy = seq
					} else {
						if _, err := tx.Exec(ctx, `INSERT INTO token_usage_bindings(source,request_id,run_id) VALUES(?,?,'shared')`, kind, id); err != nil {
							return err
						}
						if state != "unknown_counters" {
							matched := 0
							if state != "unresolved" {
								matched = 1
							}
							if _, err := tx.Exec(ctx, `INSERT INTO token_usage_legacy_candidates(legacy_seq,native_source,native_request_id,matched) VALUES(?,'native','native-request',?)`, legacy, matched); err != nil {
								return err
							}
							if matched == 1 {
								if _, err := tx.Exec(ctx, `INSERT INTO token_usage_legacy_links(legacy_seq,native_source,native_request_id,created_at) VALUES(?,'native','native-request',0)`, legacy); err != nil {
									return err
								}
							}
						}
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			before := usageIndexView(t, db, clock.Now())
			beforeHistory := usageIndexHistory(t, db)
			migration, err := os.ReadFile(filepath.Join("..", "store", "migrations", "0083_usage_overlap_query_indexes.sql"))
			if err != nil {
				t.Fatal(err)
			}
			if err = db.Write(ctx, func(tx *store.Tx) error { _, err := tx.Exec(ctx, string(migration)); return err }); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = store.Open(ctx, path, clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			after := usageIndexView(t, db, clock.Now())
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("index upgrade changed complete view: before=%+v after=%+v", before, after)
			}
			if got := usageIndexHistory(t, db); got != beforeHistory {
				t.Fatal("index upgrade changed original facts, links or migration history")
			}
			switch state {
			case "unresolved", "native_conflict":
				if after.Totals.Requests != 1 || after.Totals.Total != 10 || after.Conflicts != 1 {
					t.Fatal("unresolved/conflicted native hid legacy evidence", after)
				}
			case "matched":
				if after.Totals.Requests != 1 || after.Totals.Total != 11 || after.Conflicts != 0 {
					t.Fatal("matched native was double counted", after)
				}
			case "unknown_counters":
				if after.Totals.Requests != 1 || after.Totals.TotalKnown != 0 || after.Totals.InputKnown != 0 || len(after.Records) != 1 || after.Records[0].Total != nil {
					t.Fatal("unknown counters became measured zero", after)
				}
			}
			if err := db.Read(ctx, func(tx *store.ReadTx) error {
				var applied int
				if err := tx.Q().QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version=83`).Scan(&applied); err != nil {
					return err
				}
				if applied != 1 {
					t.Fatal("migration83 not applied exactly once", applied)
				}
				query := `EXPLAIN QUERY PLAN SELECT c.conflict OR ` + store.UsagePendingOverlapSQLFor("f") + ` FROM token_usage_current c JOIN token_usage_facts f ON f.seq=c.fact_seq WHERE NOT ` + store.UsageLegacyLinkedSQL("f.seq")
				rows, err := tx.Q().QueryContext(ctx, query)
				if err != nil {
					return err
				}
				defer rows.Close()
				var details []string
				for rows.Next() {
					var id, parent, unused int
					var detail string
					if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
						return err
					}
					details = append(details, detail)
				}
				plan := strings.Join(details, "\n")
				for _, index := range []string{"token_usage_legacy_candidates_native", "token_usage_current_fact"} {
					if !strings.Contains(plan, index) {
						t.Fatalf("overlap plan did not use %s: %s", index, plan)
					}
				}
				if strings.Contains(plan, "SCAN old_current") {
					t.Fatalf("correlated overlap still scans the current ledger: %s", plan)
				}
				return rows.Err()
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func usageIndexView(t *testing.T, db *store.Store, now time.Time) protocol.TokenUsageView {
	t.Helper()
	var v protocol.TokenUsageView
	if err := db.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		v, err = views.TokenUsageFiltered(t.Context(), tx.Q(), "total", 0, "", "", 0, now, views.UsageFilter{})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return v
}
func usageIndexHistory(t *testing.T, db *store.Store) string {
	t.Helper()
	ctx := context.Background()
	hash := sha256.New()
	if err := db.Read(ctx, func(tx *store.ReadTx) error {
		queries := []string{`SELECT version,name,applied_at FROM schema_migrations ORDER BY version`, `SELECT seq,source,request_id,revision,payload_hash,payload_json,subject,role,received_at FROM token_usage_facts ORDER BY seq`, `SELECT source,request_id,fact_seq,conflict FROM token_usage_current ORDER BY source,request_id`, `SELECT legacy_seq,native_source,native_request_id,created_at FROM token_usage_legacy_links ORDER BY legacy_seq`, `SELECT legacy_seq,native_source,native_request_id,matched FROM token_usage_legacy_candidates ORDER BY legacy_seq,native_source,native_request_id`, `SELECT source,request_id,run_id,conversation_id,host_id,proof,attribution_conflict FROM token_usage_bindings ORDER BY source,request_id`}
		for _, query := range queries {
			rows, err := tx.Q().QueryContext(ctx, query)
			if err != nil {
				return err
			}
			cols, err := rows.Columns()
			if err != nil {
				rows.Close()
				return err
			}
			for rows.Next() {
				values := make([]any, len(cols))
				args := make([]any, len(cols))
				for i := range values {
					args[i] = &values[i]
				}
				if err := rows.Scan(args...); err != nil {
					rows.Close()
					return err
				}
				raw, err := json.Marshal(values)
				if err != nil {
					rows.Close()
					return err
				}
				_, _ = hash.Write(raw)
				_, _ = hash.Write([]byte{'\n'})
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(hash.Sum(nil))
}
