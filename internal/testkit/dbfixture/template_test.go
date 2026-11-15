package dbfixture

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCopyRequiresCheckpointedWAL(t *testing.T) {
	for _, wal := range []string{"", "committed pages"} {
		t.Run(wal, func(t *testing.T) {
			var template Template
			var templatePath string
			destination := filepath.Join(t.TempDir(), "copy.db")
			err := template.Copy(t.Context(), destination, func(_ context.Context, path string) error {
				templatePath = path
				if err := os.WriteFile(path, []byte("closed database"), 0o600); err != nil {
					return err
				}
				return os.WriteFile(path+"-wal", []byte(wal), 0o600)
			})
			if _, statErr := os.Stat(filepath.Dir(templatePath)); !os.IsNotExist(statErr) {
				t.Fatal("template directory was not removed", statErr)
			}
			if wal != "" {
				if err == nil || !strings.Contains(err.Error(), "remaining WAL bytes") {
					t.Fatal("uncheckpointed WAL was not rejected", err)
				}
				if _, statErr := os.Stat(destination); !os.IsNotExist(statErr) {
					t.Fatal("incomplete database was copied", statErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(destination)
			if err != nil || string(data) != "closed database" {
				t.Fatal("closed database was not copied", string(data), err)
			}
		})
	}
}

func TestConcurrentCopiesPrepareOnceAndKeepIndependentFiles(t *testing.T) {
	var template Template
	var calls atomic.Int64
	for i := range 12 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			destination := filepath.Join(t.TempDir(), "copy.db")
			if err := template.Copy(t.Context(), destination, func(_ context.Context, path string) error {
				calls.Add(1)
				return os.WriteFile(path, []byte("closed immutable database"), 0600)
			}); err != nil {
				t.Fatal(err)
			}
			if data, err := os.ReadFile(destination); err != nil || string(data) != "closed immutable database" {
				t.Fatal("template bytes changed", string(data), err)
			}
			if err := os.WriteFile(destination, []byte(fmt.Sprint(i)), 0600); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(destination)
			if err != nil || string(data) != fmt.Sprint(i) {
				t.Fatal("copies shared mutable storage", string(data), err)
			}
		})
	}
	t.Cleanup(func() {
		if calls.Load() != 1 {
			t.Errorf("repeated preparation: %d", calls.Load())
		}
	})
}
