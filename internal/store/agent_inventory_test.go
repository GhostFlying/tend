package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/testkit"
)

func TestAgentInventoryMigrationPreservesExistingLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.db")
	clock := testkit.NewClock()
	write, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		t.Fatal(err)
	}
	old := &Store{write: write, read: write, now: clock.Now}
	if err := old.migrate(t.Context(), 54); err != nil {
		t.Fatal(err)
	}
	if err := old.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO conversation_messages(message_id,author,body,created_at) VALUES('existing','system','retained history',0)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := write.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		upgraded, err := Open(t.Context(), path, clock.Now)
		if err != nil {
			t.Fatal(err)
		}
		err = upgraded.Read(t.Context(), func(tx *ReadTx) error {
			item, err := GetConversationItem(t.Context(), tx.Q(), "existing")
			if err != nil {
				return err
			}
			if item.Body != "retained history" {
				t.Fatal("migration altered prior ledger")
			}
			for _, table := range []string{"agent_category_versions", "agent_inventory_versions"} {
				var count int
				if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil {
					return err
				}
				if count != 0 {
					t.Fatal("migration invented category or runtime metadata")
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := upgraded.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
