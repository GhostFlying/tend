package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/testkit"
)

func TestConversationBlockMigrationPreservesInterleavedTurns(t *testing.T) {
	ctx := t.Context()
	write, err := sql.Open("sqlite", dsn(filepath.Join(t.TempDir(), "tend.db"), true))
	if err != nil {
		t.Fatal(err)
	}
	defer write.Close()
	old := &Store{write: write, read: write, now: testkit.NewClock().Now}
	if err := old.migrate(ctx, 38); err != nil {
		t.Fatal(err)
	}
	for i, author := range []string{"owner", "system", "owner"} {
		id := []string{"a", "event", "b"}[i]
		mustExec(t, old, `INSERT INTO conversation_messages(message_id,author,body,created_at) VALUES(?,?,?,?)`, id, author, id, (i+1)*10)
		mustExec(t, old, `INSERT INTO main_turns(request_id,message_id,thread_id,status,created_at,updated_at) VALUES(?,?,'thread','completed',?,?)`, id, id, (i+1)*10, (i+1)*10+1)
	}
	if err := old.migrate(ctx, 39); err != nil {
		t.Fatal(err)
	}
	rows, err := write.QueryContext(ctx, `SELECT message_id FROM conversation_blocks WHERE notice_only=0 ORDER BY position`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 || ids[0] != "a" || ids[1] != "event" || ids[2] != "b" {
		t.Fatalf("processing order: %v", ids)
	}
}
