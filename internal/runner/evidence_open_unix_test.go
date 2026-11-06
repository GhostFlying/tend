//go:build !windows

package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestAcceptanceEvidenceRejectsSpecialFileWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "evidence.pipe")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, _, err := hashEvidence(ctx, path)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, errEvidenceNonRegular) {
			t.Fatal("special file was not rejected", err)
		}
	case <-time.After(time.Second):
		cancel()
		// Nonblocking cleanup also releases an implementation regressing to
		// a blocking FIFO open, without waiting on a nonexistent reader.
		f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NONBLOCK, 0)
		if err == nil {
			defer f.Close()
		}
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("special-file selection leaked an open goroutine")
		}
		t.Fatal("special-file selection blocked before returning its rejection")
	}
	cancel()
	if _, _, err := hashEvidence(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled selection ignored the original context", err)
	}
	// Exercise the actual open primitive as well: it must not block if a
	// previously regular selected path was replaced before the final fstat.
	f, err := openEvidenceFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Mode().IsRegular() {
		t.Fatal("FIFO replacement did not retain its actual special-file type", err)
	}
}
