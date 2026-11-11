package contextbuilder

import (
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/store"
)

func TestExpiredRefreshCursorRebuildsContextFromBusinessFacts(t *testing.T) {
	e := newEnv(t)
	e.task("Persistent original business goal")
	var before int64
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		var err error
		before, err = store.LatestEventSeq(t.Context(), tx.Q())
		if err != nil {
			return err
		}
		for range 3 {
			if _, err := tx.InsertEvent(t.Context(), store.EventRow{ObjectType: "storage", ObjectID: "originals", Kind: "storage.originals_changed", CreatedAt: 1}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.store.Read(t.Context(), func(tx *store.ReadTx) error {
		result, err := e.b.Compose(t.Context(), tx.Q(), ComposeRequest{Watermark: before})
		if err != nil || !strings.Contains(result.Text, "Persistent original business goal") || result.ToSeq <= before {
			t.Fatal("expired delta omitted authoritative active context", result, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestExpiredRefreshCursorKeepsUnseenBusinessCompletion(t *testing.T) {
	e := newEnv(t)
	id, _ := e.task("Completed while refresh notices coalesced")
	if err := e.b.Confirm(t.Context(), e.prepare(0).ID); err != nil {
		t.Fatal(err)
	}
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE tasks SET lifecycle='completed' WHERE task_id=?`, id); err != nil {
			return err
		}
		if _, err := tx.InsertEvent(t.Context(), store.EventRow{EventID: "completed-original", Source: "app", ObjectType: "task", ObjectID: id, Kind: "task.completed", BodyJSON: `{"spec_version":1}`}); err != nil {
			return err
		}
		for range 3 {
			if _, err := tx.InsertEvent(t.Context(), store.EventRow{ObjectType: "token_usage", ObjectID: "all", Kind: "token_usage.changed"}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	full := e.prepare(0)
	if full == nil || !full.Full || !strings.Contains(full.Text, "task.completed") || !strings.Contains(full.Text, id) {
		t.Fatal("notification reset discarded a real unobserved completion", full)
	}
}
