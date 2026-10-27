package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type testAgentDrain bool

func (d testAgentDrain) UpgradeDrainStatus(context.Context) (bool, error) { return bool(d), nil }

func TestUpgradeWaitsForEveryAgentAndUncertainCompaction(t *testing.T) {
	s, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "test.db"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a := app.New(s, protocol.NewULIDGen(time.Now, nil), time.Now)
	if drained, err := allAgentsDrained(t.Context(), a, testAgentDrain(true), testAgentDrain(false)); err != nil || drained {
		t.Fatalf("background activity ignored: %t %v", drained, err)
	}
	if err := s.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO agent_conversations(conversation_id,created_at) VALUES('detached-background',0)`); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_compactions(operation_id,conversation_id,status,source,created_at,updated_at) VALUES('compact','detached-background','unknown','manual',0,0)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"starting", "running", "unknown", "completed", "failed", "queued"} {
		if err := s.Write(t.Context(), func(tx *store.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE agent_compactions SET status=?`, status)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		drained, err := allAgentsDrained(t.Context(), a, testAgentDrain(true), testAgentDrain(true))
		want := status == "completed" || status == "failed" || status == "queued"
		if err != nil || drained != want {
			t.Fatalf("%s: drained=%t want=%t err=%v", status, drained, want, err)
		}
	}
}
