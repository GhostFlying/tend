package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/testkit"
)

func TestTaskRelationsMigrationPreservesExistingTask(t *testing.T) {
	ctx := t.Context()
	p := filepath.Join(t.TempDir(), "tend.db")
	write, err := sql.Open("sqlite", dsn(p, true))
	if err != nil {
		t.Fatal(err)
	}
	old := &Store{write: write, read: write, now: testkit.NewClock().Now}
	if err := old.migrate(ctx, 22); err != nil {
		t.Fatal(err)
	}
	insertTask(t, old, "task_existing")
	if _, err := write.ExecContext(ctx, `UPDATE tasks SET lifecycle='completed' WHERE task_id='task_existing'`); err != nil {
		t.Fatal(err)
	}
	write.Close()
	upgraded, err := Open(context.Background(), p, testkit.NewClock().Now)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	if err := upgraded.Read(ctx, func(tx *ReadTx) error {
		original, err := GetTask(ctx, tx.Q(), "task_existing")
		if err != nil {
			return err
		}
		if original.Lifecycle != "completed" || original.SpecVersion != 1 {
			t.Fatalf("migration changed task: %+v", original)
		}
		refs, err := ListTaskRelations(ctx, tx.Q(), original.TaskID)
		if err != nil {
			return err
		}
		if len(refs) != 0 {
			t.Fatal("migration invented historical references")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
