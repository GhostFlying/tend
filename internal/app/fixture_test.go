package app

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

// Each test process migrates once, including when the suite runs in shards.
// Only immutable bytes are shared; every fixture has its own files and pools.
var appFixtureTemplate struct {
	once sync.Once
	data []byte
	err  error
}

func openAppFixture(ctx context.Context, path string, now func() time.Time) (*store.Store, error) {
	appFixtureTemplate.once.Do(func() {
		appFixtureTemplate.data, appFixtureTemplate.err = buildAppFixtureTemplate(ctx, now)
	})
	if appFixtureTemplate.err != nil {
		return nil, appFixtureTemplate.err
	}
	if err := os.WriteFile(path, appFixtureTemplate.data, 0o600); err != nil {
		return nil, fmt.Errorf("copy app fixture: %w", err)
	}
	return store.Open(ctx, path, now)
}

func buildAppFixtureTemplate(ctx context.Context, now func() time.Time) (data []byte, err error) {
	dir, err := os.MkdirTemp("", "tend-app-fixture-")
	if err != nil {
		return nil, fmt.Errorf("create app fixture template directory: %w", err)
	}
	defer func() {
		if cleanupErr := os.RemoveAll(dir); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("remove app fixture template directory: %w", cleanupErr))
		}
	}()
	path := filepath.Join(dir, "template.db")
	db, err := store.Open(ctx, path, now)
	if err != nil {
		return nil, fmt.Errorf("migrate app fixture template: %w", err)
	}
	if err := db.Close(); err != nil {
		return nil, fmt.Errorf("close app fixture template: %w", err)
	}
	// Closing the last connection checkpoints WAL. Refuse to copy a main file
	// if any committed content could still reside in its WAL sidecar.
	info, err := os.Stat(path + "-wal")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect app fixture template WAL: %w", err)
	}
	if err == nil && info.Size() != 0 {
		return nil, fmt.Errorf("app fixture template has %d remaining WAL bytes", info.Size())
	}
	data, err = os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read app fixture template: %w", err)
	}
	return data, nil
}
