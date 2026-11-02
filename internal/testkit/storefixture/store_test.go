package storefixture

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/store"
)

func TestConcurrentFirstCopiesHaveIndependentWrites(t *testing.T) {
	var template Template
	for i := range 8 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			now := time.Date(2026, 1, i+1, 0, 0, 0, 0, time.UTC)
			db, err := template.Open(t.Context(), filepath.Join(t.TempDir(), "copy.db"), func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			// This unique key must be available independently in every copy.
			if err := db.Write(t.Context(), func(tx *store.Tx) error {
				_, err := tx.Exec(t.Context(), `INSERT INTO inbox(source,source_event_id,received_at) VALUES('fixture','same-key',?)`, now.UnixMilli())
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOpenPreservesExistingFile(t *testing.T) {
	var template Template
	path := filepath.Join(t.TempDir(), "existing.db")
	if err := os.WriteFile(path, []byte("existing content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := template.Open(t.Context(), path, time.Now); !errors.Is(err, os.ErrExist) {
		t.Fatal("existing path not rejected", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "existing content" {
		t.Fatal("existing file changed", string(data), err)
	}
}

func TestInitializationFailureIsRetained(t *testing.T) {
	var template Template
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, ctx := range []context.Context{ctx, t.Context()} {
		path := filepath.Join(t.TempDir(), "copy.db")
		if _, err := template.Open(ctx, path, time.Now); !errors.Is(err, context.Canceled) {
			t.Fatal("template initialization error was lost", err)
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("failed initialization created a fixture", err)
		}
	}
}
