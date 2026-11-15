//go:build linux

package updater

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/blobstore"
	"golang.org/x/sys/unix"
)

func TestBackupReadDoesNotInitializeObjectStore(t *testing.T) {
	cfg := Config{StateDir: t.TempDir(), DataFiles: []string{filepath.Join(t.TempDir(), "ledger.db")}}
	f := BackupFile{Path: cfg.DataFiles[0] + ".files/ab/object", Existed: true, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ObjectSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	if _, err := backupParts(cfg, f); err == nil {
		t.Fatal("missing object accepted")
	}
	if _, err := os.Stat(filepath.Join(cfg.StateDir, "backup-objects")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("backup read initialized missing object store", err)
	}
}

func TestBackupReadsEachPriorManifestOnce(t *testing.T) {
	h := newHarness(t)
	originals, err := blobstore.New(h.data + ".files")
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"original-one", "original-two", "original-three"} {
		if _, err = originals.Put(t.Context(), strings.NewReader(body)); err != nil {
			t.Fatal(err)
		}
	}
	for _, stream := range []string{"one", "two", "three", "four"} {
		if _, err = originals.AppendOriginal(t.Context(), stream, []byte("prefix-"+stream)); err != nil {
			t.Fatal(err)
		}
	}
	one := createRetentionCheckpoint(t, h, "index-one", h.now.Add(-2*time.Hour), false)
	two := createRetentionCheckpoint(t, h, "index-two", h.now.Add(-time.Hour), false)
	fd, err := unix.InotifyInit1(unix.IN_NONBLOCK | unix.IN_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	watches := map[int]string{}
	for _, dir := range []string{one.Directory, two.Directory} {
		wd, err := unix.InotifyAddWatch(fd, filepath.Join(dir, "files.json"), unix.IN_OPEN|unix.IN_CLOSE_NOWRITE)
		if err != nil {
			t.Fatal(err)
		}
		watches[wd] = dir
	}
	wd, err := unix.InotifyAddWatch(fd, h.cfg.StateDir, unix.IN_OPEN|unix.IN_CLOSE_NOWRITE)
	if err != nil {
		t.Fatal(err)
	}
	watches[wd] = "object-store initialization"
	createRetentionCheckpoint(t, h, "index-three", h.now, false)
	readOpens := func() map[int]int {
		counts := map[int]int{}
		buffer := make([]byte, 64<<10)
		for {
			n, err := unix.Read(fd, buffer)
			if errors.Is(err, unix.EAGAIN) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if n == 0 {
				break
			}
			for at := 0; at < n; {
				wd := int(int32(binary.NativeEndian.Uint32(buffer[at : at+4])))
				mask := binary.NativeEndian.Uint32(buffer[at+4 : at+8])
				// A directory watch also reports child opens. Only unnamed events
				// denote opening the watched directory itself (the Sync entry).
				if mask&unix.IN_OPEN != 0 && binary.NativeEndian.Uint32(buffer[at+12:at+16]) == 0 {
					counts[wd]++
				}
				at += unix.SizeofInotifyEvent + int(binary.NativeEndian.Uint32(buffer[at+12:at+16]))
			}
		}
		return counts
	}
	counts := readOpens()
	for wd, dir := range watches {
		if counts[wd] != 1 {
			t.Errorf("prior manifest %s opened %d times, want once", filepath.Base(dir), counts[wd])
		}
	}
	// A new backup takes a new view, including the just-completed checkpoint.
	thirdManifest := filepath.Join(h.cfg.StateDir, "backups", "index-three", "files.json")
	thirdWD, err := unix.InotifyAddWatch(fd, thirdManifest, unix.IN_OPEN|unix.IN_CLOSE_NOWRITE)
	if err != nil {
		t.Fatal(err)
	}
	watches[thirdWD] = thirdManifest
	if _, err = originals.AppendOriginal(t.Context(), "one", []byte("next-operation")); err != nil {
		t.Fatal(err)
	}
	createRetentionCheckpoint(t, h, "index-four", h.now.Add(time.Hour), false)
	counts = readOpens()
	for wd, path := range watches {
		if counts[wd] != 1 {
			t.Errorf("next operation %s opened %d times, want once", filepath.Base(path), counts[wd])
		}
	}

}
