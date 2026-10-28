package gitws

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScratchResolvesAliasesAndReportsPreparationFailure(t *testing.T) {
	root := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	got, err := prepareScratch(filepath.Join(alias, "scratch", "run"))
	want, _ := filepath.EvalSymlinks(filepath.Join(root, "scratch", "run"))
	if err != nil || got != want {
		t.Fatalf("scratch %q, %v; want %q", got, err, want)
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareScratch(filepath.Join(file, "scratch")); err == nil {
		t.Fatal("invalid scratch must fail preparation")
	}
}
