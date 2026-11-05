// Package dbfixture copies immutable database templates without depending on a
// database driver or the application's store package.
package dbfixture

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Template retains a closed database's immutable bytes. The zero value is ready
// for use. A Template must not be copied after use.
type Template struct {
	once sync.Once
	data []byte
	err  error
}

// Copy prepares a template once, then writes an independent, exclusive file.
// prepare must create and close the database at its supplied path before
// returning. Only the first caller's prepare function is used; initialization
// failures are retained rather than retried by another test.
func (f *Template) Copy(ctx context.Context, destination string, prepare func(context.Context, string) error) error {
	f.once.Do(func() { f.data, f.err = build(ctx, prepare) })
	if f.err != nil {
		return f.err
	}
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create database fixture: %w", err)
	}
	_, writeErr := file.Write(f.data)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return fmt.Errorf("copy database fixture: %w", err)
	}
	return nil
}

func build(ctx context.Context, prepare func(context.Context, string) error) (data []byte, err error) {
	dir, err := os.MkdirTemp("", "tend-db-fixture-")
	if err != nil {
		return nil, fmt.Errorf("create database template directory: %w", err)
	}
	defer func() {
		if cleanupErr := os.RemoveAll(dir); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("remove database template directory: %w", cleanupErr))
		}
	}()
	path := filepath.Join(dir, "template.db")
	if err := prepare(ctx, path); err != nil {
		return nil, fmt.Errorf("prepare database template: %w", err)
	}
	// A committed WAL must be checkpointed before copying the main file.
	info, err := os.Stat(path + "-wal")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect database template WAL: %w", err)
	}
	if err == nil && info.Size() != 0 {
		return nil, fmt.Errorf("database template has %d remaining WAL bytes", info.Size())
	}
	data, err = os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read database template: %w", err)
	}
	return data, nil
}
