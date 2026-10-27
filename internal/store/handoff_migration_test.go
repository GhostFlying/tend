package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/testkit"
)

func TestHandoffMigrationSeedsExistingConversationChanges(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ledger.db")
	clock := testkit.NewClock()
	write, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		t.Fatal(err)
	}
	old := &Store{write: write, read: write, now: clock.Now}
	if err := old.migrate(ctx, 25); err != nil {
		write.Close()
		t.Fatal(err)
	}
	if err := old.Write(ctx, func(tx *Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO conversation_messages(message_id,author,body,created_at) VALUES('existing','system','historic completion',0)`)
		return err
	}); err != nil {
		write.Close()
		t.Fatal(err)
	}
	if err := write.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(ctx, path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	if err := upgraded.Read(ctx, func(tx *ReadTx) error {
		var version int64
		if err := tx.Q().QueryRowContext(ctx, `SELECT last_version FROM conversation_changes WHERE message_id='existing'`).Scan(&version); err != nil {
			return err
		}
		row, err := GetConversationItem(ctx, tx.Q(), "existing")
		if err != nil {
			return err
		}
		if row.Body != "historic completion" {
			t.Fatalf("historic body changed: %+v", row)
		}
		ids, err := SummaryObservationIDs(ctx, tx.Q(), nil, 10)
		if err != nil {
			return err
		}
		if len(ids) != 1 || ids[0] != "existing" {
			t.Fatalf("historic row omitted: %v", ids)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := upgraded.Write(ctx, func(tx *Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM conversation_messages WHERE message_id='existing'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := upgraded.Read(ctx, func(tx *ReadTx) error {
		since := int64(0)
		ids, err := SummaryObservationIDs(ctx, tx.Q(), &since, 10)
		if err != nil {
			return err
		}
		if len(ids) != 1 || ids[0] != "existing" {
			t.Fatalf("historic tombstone omitted: %v", ids)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
