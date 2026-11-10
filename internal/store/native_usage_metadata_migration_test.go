package store

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/testkit"
)

func TestNativeUsageMigrationRepairsOnlyOptionalPurposeConflicts(t *testing.T) {
	ctx := t.Context()
	db, err := sql.Open("sqlite", dsn(filepath.Join(t.TempDir(), "historical.db"), true))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	s := &Store{write: db, read: db, now: testkit.NewClock().Now}
	if err := s.migrate(ctx, 115); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"optional", "counter", "nonempty_purpose", "content", "legacy", "ownership"} {
		for variant := range 2 {
			payload := map[string]any{"native_content_hash": "immutable", "total": 10, "purpose": "issue_owner_native", "revision": 1}
			subject := "session:original"
			if variant == 1 {
				delete(payload, "purpose")
				switch kind {
				case "counter":
					payload["total"] = 11
				case "nonempty_purpose":
					payload["purpose"] = "internal_summary"
				case "content":
					payload["native_content_hash"] = "different"
				case "ownership":
					subject = "session:foreign"
				}
			}
			if kind == "legacy" {
				delete(payload, "native_content_hash")
			}
			raw, _ := json.Marshal(payload)
			r, err := db.ExecContext(ctx, `INSERT INTO token_usage_facts(source,request_id,revision,payload_hash,payload_json,subject,role,received_at) VALUES(?,'original',1,?,?,?,'native',1)`, kind, string(rune('a'+variant)), string(raw), subject)
			if err != nil {
				t.Fatal(err)
			}
			if variant == 0 {
				seq, _ := r.LastInsertId()
				if _, err := db.ExecContext(ctx, `INSERT INTO token_usage_current VALUES(?,'original',?,1)`, kind, seq); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if err := s.migrate(ctx, 116); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"optional", "counter", "nonempty_purpose", "content", "legacy", "ownership"} {
		var conflict bool
		var facts int
		var purpose string
		if err := db.QueryRowContext(ctx, `SELECT c.conflict,json_extract(f.payload_json,'$.purpose'),(SELECT count(*) FROM token_usage_facts WHERE source=c.source) FROM token_usage_current c JOIN token_usage_facts f ON f.seq=c.fact_seq WHERE c.source=?`, kind).Scan(&conflict, &purpose, &facts); err != nil {
			t.Fatal(err)
		}
		if conflict != (kind != "optional") || facts != 2 || purpose != "issue_owner_native" {
			t.Fatal(kind, conflict, facts, purpose)
		}
	}
	if err := s.migrate(ctx, 116); err != nil {
		t.Fatal("migration replay", err)
	}
}
