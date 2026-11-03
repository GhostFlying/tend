//go:build linux

package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestCopyAtomicPinnedRejectsChangedCandidateBeforePublication(t *testing.T) {
	root := t.TempDir()
	src, dst := filepath.Join(root, "candidate"), filepath.Join(root, "installed")
	original := []byte("reviewed updater artifact")
	sum := sha256.Sum256(original)
	if err := os.WriteFile(src, []byte("replaced after preflight"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyPinnedAtomic(src, dst, 0o755, hex.EncodeToString(sum[:])); err == nil {
		t.Fatal("changed candidate was published")
	}
	actual, err := os.ReadFile(dst)
	if err != nil || string(actual) != string(original) {
		t.Fatalf("installed artifact changed on failed pin: %q %v", actual, err)
	}
	entries, err := filepath.Glob(filepath.Join(root, ".updater-copy-*"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("unpublished temporary artifact retained: %v %v", entries, err)
	}
	if err := os.WriteFile(src, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyPinnedAtomic(src, dst, 0o755, hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
	actual, err = os.ReadFile(dst)
	if err != nil || string(actual) != string(original) {
		t.Fatalf("pinned artifact was not published: %q %v", actual, err)
	}
}
