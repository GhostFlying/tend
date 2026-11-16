package views

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/testkit"
)

// This compares actual statement lifetimes, not cached scheduling conclusions.
// The batched hot query is used once in each read transaction after fact reuse.
// pool_prepare owns a database/sql DB statement and binds it through Tx.Stmt;
// the empty fixture emphasizes parsing cost, not whole-scenario performance.
func BenchmarkManagerHandoffPreparation(b *testing.B) {
	ctx := b.Context()
	path := filepath.Join(b.TempDir(), "ledger.db")
	fixture, err := viewFixture.Open(ctx, path, testkit.NewClock().Now)
	if err != nil {
		b.Fatal(err)
	}
	if err := fixture.Close(); err != nil {
		b.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, mode := range []string{"direct", "transaction_prepare", "pool_prepare"} {
		b.Run(mode, func(b *testing.B) {
			var shared *sql.Stmt
			if mode == "pool_prepare" {
				shared, err = db.PrepareContext(ctx, managerHandoffBatchSQL)
				if err != nil {
					b.Fatal(err)
				}
				defer shared.Close()
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
				if err != nil {
					b.Fatal(err)
				}
				var stmt *sql.Stmt
				var rows *sql.Rows
				if shared != nil {
					stmt = tx.StmtContext(ctx, shared)
				} else if mode == "transaction_prepare" {
					stmt, err = tx.PrepareContext(ctx, managerHandoffBatchSQL)
					if err != nil {
						b.Fatal(err)
					}
				}
				if stmt == nil {
					rows, err = tx.QueryContext(ctx, managerHandoffBatchSQL, `{"fixture":2}`)
				} else {
					rows, err = stmt.QueryContext(ctx, `{"fixture":2}`)
				}
				if err != nil {
					b.Fatal(err)
				}
				for rows.Next() {
					b.Fatal("empty fixture unexpectedly acquired a blocker")
				}
				if err := rows.Err(); err != nil {
					b.Fatal(err)
				}
				if err := rows.Close(); err != nil {
					b.Fatal(err)
				}
				if stmt != nil {
					if err := stmt.Close(); err != nil {
						b.Fatal(err)
					}
				}
				if err := tx.Rollback(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
