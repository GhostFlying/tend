package journal

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/testkit"
)

func historicalIntegrationJournal(t *testing.T, version int) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "journal.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries[:version] {
		body, err := migrationFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(t.Context(), string(body)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(t.Context(), fmt.Sprintf("PRAGMA user_version=%d", version)); err != nil {
		t.Fatal(err)
	}
	return path, db
}

func TestCapabilityMigrationPreservesMainJournalFacts(t *testing.T) {
	path, db := historicalIntegrationJournal(t, 16)
	ctx := t.Context()
	const assignment = `{ "run_id": "original", "opaque": "frozen" }`
	const authority = `{ "opaque": "original-permission" }`
	const payload = `{ "opaque": "original-request" }`
	for _, row := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO runs(run_id,lease_id,run_token,assignment_json,phase,fenced,attempts,updated_at,stopped_confirmed,thread_id) VALUES('original','lease','credential',?,'started',1,3,1,1,'original-thread')`, []any{assignment}},
		{`INSERT INTO retained_run_resumes VALUES('original',?)`, []any{authority}},
		{`INSERT INTO requests(request_id,operation,run_id,target_json,payload_json,created_at) VALUES('original-request','operation','original','{}',?,1)`, []any{payload}},
	} {
		if _, err := db.ExecContext(ctx, row.sql, row.args...); err != nil {
			t.Fatal(err)
		}
	}
	j, err := Open(ctx, path, testkit.NewClock().Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	var savedAssignment, savedAuthority, savedPayload, thread string
	var stopped, fenced, attempts int
	if err := db.QueryRowContext(ctx, `SELECT assignment_json,authority_json,payload_json,thread_id,stopped_confirmed,fenced,attempts FROM runs JOIN retained_run_resumes USING(run_id) JOIN requests USING(run_id)`).Scan(&savedAssignment, &savedAuthority, &savedPayload, &thread, &stopped, &fenced, &attempts); err != nil || savedAssignment != assignment || savedAuthority != authority || savedPayload != payload || thread != "original-thread" || stopped != 1 || fenced != 1 || attempts != 3 {
		t.Fatal("upgrade changed original frozen/request/stop facts", savedAssignment, savedAuthority, savedPayload, thread, stopped, fenced, attempts, err)
	}
	for _, table := range []string{"capability_verifications", "run_capability_receipts"} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil || count != 1 {
			t.Fatal("missing appended capability table", table, count, err)
		}
	}
}

func TestCapabilityMigrationRejectsOldCandidateJournalCollision(t *testing.T) {
	for _, version := range []int{16, 17} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			path, db := historicalIntegrationJournal(t, 15)
			for _, file := range []string{"0017_capability_verification.sql", "0018_capability_revalidation_receipts.sql"}[:version-15] {
				body, err := migrationFS.ReadFile("migrations/" + file)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.ExecContext(t.Context(), string(body)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.ExecContext(t.Context(), fmt.Sprintf("PRAGMA user_version=%d", version)); err != nil {
				t.Fatal(err)
			}
			const original = `{ "request_id": "unknown-original" }`
			if _, err := db.ExecContext(t.Context(), `INSERT INTO capability_verifications(request_id,request_json,status_json) VALUES('original',?,'{"state":"unknown"}')`, original); err != nil {
				t.Fatal(err)
			}
			j, err := Open(t.Context(), path, testkit.NewClock().Now)
			if err == nil {
				j.Close()
				t.Fatal("accepted conflicting candidate journal")
			}
			if !strings.Contains(err.Error(), "migration history conflict") {
				t.Fatal("missing precise collision diagnostic", err)
			}
			var after int
			if err := db.QueryRowContext(t.Context(), `PRAGMA user_version`).Scan(&after); err != nil || after != version {
				t.Fatal("rejected open changed candidate version", after, err)
			}
			var request, status string
			if err := db.QueryRowContext(t.Context(), `SELECT request_json,status_json FROM capability_verifications WHERE request_id='original'`).Scan(&request, &status); err != nil || request != original || status != `{"state":"unknown"}` {
				t.Fatal("rejected open changed original unknown request", request, status, err)
			}
		})
	}
}
