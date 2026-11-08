//go:build linux

package updater

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestSelfFlockProofKeepsExactOwnerDuringUnrelatedLockChurn(t *testing.T) {
	file, err := os.OpenFile(filepath.Join(t.TempDir(), "held"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	st := info.Sys().(*syscall.Stat_t)
	id, err := (LinuxProcess{}).Inspect(t.Context(), os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if selfOwnsFlock(t.Context(), id, uint64(st.Dev), st.Ino) {
		t.Fatal("open descriptor was mistaken for lock ownership")
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	defer func() { cancel(); wg.Wait() }()
	for range 8 {
		files := make([]*os.File, 64)
		dir := t.TempDir()
		for i := range files {
			f, err := os.CreateTemp(dir, "churn-")
			if err != nil {
				t.Fatal(err)
			}
			files[i] = f
			t.Cleanup(func() { _ = f.Close() })
		}
		wg.Go(func() {
			for ctx.Err() == nil {
				for _, f := range files {
					_ = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
				}
				for _, f := range files {
					_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				}
			}
		})
	}
	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) {
		if !selfOwnsFlock(t.Context(), id, uint64(st.Dev), st.Ino) {
			t.Fatal("per-descriptor proof lost a continuously-held supervisor lock")
		}
	}
	wrong := id
	wrong.StartTicks++
	if selfOwnsFlock(t.Context(), wrong, uint64(st.Dev), st.Ino) {
		t.Fatal("proof accepted a reused process identity")
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if selfOwnsFlock(t.Context(), id, uint64(st.Dev), st.Ino) {
		t.Fatal("proof remained valid after unlock")
	}
}
