//go:build linux || darwin

package gitws

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const hostFileLocks = true

// Keep lock files in place. Unlinking after unlock could give a waiter and a
// new arrival different inodes for the same physical resource.
func lockHostResource(ctx context.Context, home, key string) (func(), error) {
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	dir := filepath.Join(home, "locks", "workspaces")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, fmt.Sprintf("%x", sha256.Sum256([]byte(key)))), os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = f.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			_ = f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, context.Cause(ctx)
		case <-time.After(20 * time.Millisecond):
		}
	}
}
