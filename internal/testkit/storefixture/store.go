// Package storefixture creates independent, current-schema test databases.
// Migration and reopen tests should continue to use store.Open directly.
package storefixture

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/GhostFlying/tend/internal/store"
)

// Template lazily migrates one empty database and shares only immutable bytes.
// The zero value is ready for use. A Template must not be copied after use.
// Migration timestamps come from the first caller; each opened store uses its
// own clock, connections, database files and original-file directory.
type Template struct {
	once sync.Once
	data []byte
	err  error
}

// Open copies the template to a new path, then opens it through store.Open.
// Existing databases are never overwritten. Template initialization errors are
// retained so a broken migration cannot be hidden by a subsequent fixture.
func (f *Template) Open(ctx context.Context, path string, now func() time.Time) (*store.Store, error) {
	f.once.Do(func() { f.data, f.err = build(ctx, now) })
	if f.err != nil {
		return nil, f.err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create store fixture: %w", err)
	}
	_, writeErr := file.Write(f.data)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return nil, fmt.Errorf("copy store fixture: %w", err)
	}
	return store.Open(ctx, path, now)
}

func build(ctx context.Context, now func() time.Time) (data []byte, err error) {
	dir, err := os.MkdirTemp("", "tend-store-fixture-")
	if err != nil {
		return nil, fmt.Errorf("create store fixture template directory: %w", err)
	}
	defer func() {
		if cleanupErr := os.RemoveAll(dir); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("remove store fixture template directory: %w", cleanupErr))
		}
	}()
	path := filepath.Join(dir, "template.db")
	db, err := store.Open(ctx, path, now)
	if err != nil {
		return nil, fmt.Errorf("migrate store fixture template: %w", err)
	}
	if err := db.Close(); err != nil {
		return nil, fmt.Errorf("close store fixture template: %w", err)
	}
	// Closing the last connection checkpoints WAL. Refuse to copy a main file
	// if any committed content could still reside in its WAL sidecar.
	info, err := os.Stat(path + "-wal")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect store fixture template WAL: %w", err)
	}
	if err == nil && info.Size() != 0 {
		return nil, fmt.Errorf("store fixture template has %d remaining WAL bytes", info.Size())
	}
	data, err = os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read store fixture template: %w", err)
	}
	return data, nil
}
