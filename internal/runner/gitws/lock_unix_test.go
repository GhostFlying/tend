//go:build linux || darwin

package gitws

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestConcurrentRunnerResourceLocks(t *testing.T) {
	cfg := Config{Home: t.TempDir()}
	current, next := New(cfg), New(cfg)
	release, err := current.lock(t.Context(), "repo:shared")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if release != nil {
			release()
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if unlock, err := next.lock(ctx, "repo:shared"); !errors.Is(err, context.DeadlineExceeded) {
		if unlock != nil {
			unlock()
		}
		t.Fatalf("acquired busy repository: %v", err)
	}
	unlock, err := next.lock(t.Context(), "repo:other")
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	release()
	release = nil
	unlock, err = next.lock(t.Context(), "repo:shared")
	if err != nil {
		t.Fatal(err)
	}
	unlock()
}
