//go:build linux

package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestHandoffRejectsReplaceableInstallerStream(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "installer")
	next := filepath.Join(root, "replacement")
	reviewed := []byte("#!/bin/sh\nprintf 'REVIEWED\\n'\n#" + strings.Repeat("x", 64<<20) + "\n")
	replaced := []byte("#!/bin/sh\nprintf 'UNREVIEWED\\n'\n")
	sum := sha256.Sum256(reviewed)
	if err := os.WriteFile(path, reviewed, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(next, replaced, 0700); err != nil {
		t.Fatal(err)
	}
	fd, err := syscall.InotifyInit1(syscall.IN_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	if _, err := syscall.InotifyAddWatch(fd, path, syscall.IN_OPEN); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		var events [4096]byte
		_, err := syscall.Read(fd, events[:])
		if err != nil {
			done <- err
			return
		}
		done <- os.Rename(next, path)
	}()
	e := newTestEnv(t, "")
	code := e.main("updater", "handoff", "status", "--installer", path, "--installer-sha256", hex.EncodeToString(sum[:]), "--config", filepath.Join(root, "config.json"), "--json")
	<-done
	if code == exitOK && strings.Contains(e.out.String(), "UNREVIEWED") {
		t.Fatalf("checksum accepted reviewed regular-file bytes but executed replacement: %s", e.out.String())
	}
}
