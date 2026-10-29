package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestAttentionActionTimeMigrationPreservesNonemptyFollowupLedger(t *testing.T) {
	for _, version := range []int{60, 61} {
		t.Run(fmt.Sprintf("schema-%d", version), func(t *testing.T) { testAttentionActionTimeMigration(t, version) })
	}
}
func testAttentionActionTimeMigration(t *testing.T, version int) {
	ctx := t.Context()
	clock := testkit.NewClock()
	path := filepath.Join(t.TempDir(), fmt.Sprintf("old%d.db", version))
	db, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	old := &Store{write: db, read: db, now: clock.Now}
	t.Cleanup(func() { old.Close() })
	if err := old.migrate(ctx, version); err != nil {
		t.Fatal(err)
	}
	// This is an actual old schema built by its migrations, not a database with
	// a lowered schema version. All five rebuilt tables contain live references.
	w := protocol.WorkItem{WorkItemID: "wi_old-clock", Revision: 1, Status: "open", IssuePlan: protocol.IssuePlan{Title: "Historical responsibility", Project: "default", Intent: "Retain historical followups"}}
	err = old.Write(ctx, func(tx *Tx) error {
		if err := tx.SaveIssue(ctx, w, 0, map[string]any{"reason": "original"}); err != nil {
			return err
		}
		if _, err := tx.InsertMessage(ctx, MessageRow{MessageID: "msg_old-clock", Author: "system", Body: "Original reminder", CreatedAt: clock.Now().UnixMilli()}); err != nil {
			return err
		}
		r := protocol.FollowupRule{RuleID: "followup_old-clock", WorkItemID: w.WorkItemID, Revision: 1, Status: "active", FollowupSpec: protocol.FollowupSpec{Kind: "at", Purpose: "Original reminder", Instruction: "Retain original", SourceID: "req_original", Timezone: "UTC", At: clock.Now().Add(time.Hour).UnixMilli()}, CreatedAt: clock.Now().UnixMilli(), UpdatedAt: clock.Now().UnixMilli()}
		if err := tx.SaveFollowup(ctx, r, 0, "req_original"); err != nil {
			return err
		}
		if err := tx.FollowupTarget(ctx, r.RuleID, "work_item", w.WorkItemID); err != nil {
			return err
		}
		if err := tx.InsertFollowupOccurrence(ctx, protocol.FollowupOccurrence{OccurrenceID: "occurrence_old-clock", RuleID: r.RuleID, Revision: 1, WorkItemID: w.WorkItemID, MessageID: "msg_old-clock", Status: "queued", Reason: "at", Evidence: "Original evidence", CreatedAt: clock.Now().UnixMilli()}, "revision:1"); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO followup_registrations(identity,rule_id) VALUES('original-registration',?)`, r.RuleID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	family := []string{"followup_rules", "followup_revisions", "followup_targets", "followup_occurrences", "followup_registrations"}
	snapshot := func() map[string][]string {
		t.Helper()
		out := map[string][]string{}
		for _, table := range family {
			rows, err := db.QueryContext(ctx, "SELECT * FROM "+table+" ORDER BY 1,2")
			if err != nil {
				t.Fatal(err)
			}
			cols, err := rows.Columns()
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				values := make([]any, len(cols))
				pointers := make([]any, len(cols))
				for i := range values {
					pointers[i] = &values[i]
				}
				if err := rows.Scan(pointers...); err != nil {
					t.Fatal(err)
				}
				raw, err := json.Marshal(values)
				if err != nil {
					t.Fatal(err)
				}
				out[table] = append(out[table], string(raw))
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			rows.Close()
		}
		return out
	}
	before := snapshot()
	// Inspect incoming references from every real old-schema table, not just
	// the original followup migration. Every affected child must be preserved.
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	incoming := map[string]bool{}
	for _, table := range tables {
		rows, err := db.QueryContext(ctx, fmt.Sprintf("PRAGMA foreign_key_list(%q)", table))
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id, seq int
			var target, from, to, update, del, match string
			if err := rows.Scan(&id, &seq, &target, &from, &to, &update, &del, &match); err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(target, "followup_") {
				incoming[table+"->"+target] = true
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	expected := map[string]bool{"followup_occurrences->followup_rules": true, "followup_revisions->followup_rules": true, "followup_targets->followup_rules": true, "followup_registrations->followup_rules": true}
	if !reflect.DeepEqual(incoming, expected) {
		t.Fatal("actual incoming FK set changed", incoming)
	}
	if err := old.migrate(ctx, 62); err != nil {
		t.Fatal(err)
	}
	if after := snapshot(); !reflect.DeepEqual(before, after) {
		t.Fatal("historical values changed", before, after)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_check`).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	for _, query := range []string{`INSERT INTO followup_targets VALUES('missing','attention','attn_missing')`, `UPDATE followup_rules SET work_item_id='missing'`, `UPDATE followup_occurrences SET message_id='missing'`} {
		if _, err := db.ExecContext(ctx, query); err == nil {
			t.Fatal("original FK no longer enforced", query)
		}
	}
	for _, index := range []string{"followup_rules_due", "followup_rules_item", "followup_targets_match", "followup_occurrences_rule", "followup_occurrences_message", "followup_occurrences_latest"} {
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE type='index' AND name=?`, index).Scan(&count); err != nil || count != 1 {
			t.Fatal(index, count, err)
		}
	}
	// Replay keys and immutable revision identities retain their original uniqueness.
	for _, query := range []string{
		`INSERT INTO followup_revisions SELECT * FROM followup_revisions`,
		`INSERT INTO followup_targets SELECT * FROM followup_targets`,
		`INSERT INTO followup_registrations SELECT * FROM followup_registrations`,
		`INSERT INTO followup_occurrences SELECT 'another-occurrence',rule_id,revision,work_item_id,occurrence_key,message_id,status,reason,evidence,created_at FROM followup_occurrences`,
	} {
		if _, err := db.ExecContext(ctx, query); err == nil {
			t.Fatal("original uniqueness no longer enforced", query)
		}
	}
	if after := snapshot(); !reflect.DeepEqual(before, after) {
		t.Fatal("rejected duplicate changed history", before, after)
	}
	// The newly permitted absence is NULL, and all other references remain real.
	if _, err := db.ExecContext(ctx, `UPDATE followup_rules SET work_item_id=NULL`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE followup_occurrences SET work_item_id=NULL`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_check`).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}
