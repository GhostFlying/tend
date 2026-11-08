package codexenv

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestMaterializeRejectsAncestorLinkBeforeRecursingIntoStaging(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory symlink fixture")
	}
	root := t.TempDir()
	source := filepath.Join(root, "package")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, filepath.Join(source, "ancestor")); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "store", "staging")
	if err := materialize(t.Context(), source, target); err == nil {
		t.Fatal("ancestor symlink recursively copied its own staging directory")
	}
	if _, err := os.Stat(filepath.Join(target, "ancestor", "store")); !os.IsNotExist(err) {
		t.Fatal("copy entered the environment store", err)
	}
}

func TestPackageDigestIncludesEmptyDirectoryStructure(t *testing.T) {
	root := t.TempDir()
	before, err := treeDigest(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(root, "output"), 0700); err != nil {
		t.Fatal(err)
	}
	after, err := treeDigest(t.Context(), root)
	if err != nil || after == before {
		t.Fatal("empty resource directory did not change package identity", err)
	}
}

func TestPublicationSyncDoesNotReadUnchangedAncestors(t *testing.T) {
	if runtime.GOOS != "linux" || os.Geteuid() == 0 {
		t.Skip("Linux directory permission fixture requires an unprivileged user")
	}
	ancestor := filepath.Join(t.TempDir(), "execute-only")
	store := filepath.Join(ancestor, "store")
	dest := filepath.Join(store, "environment", "generations", "snapshot")
	if err := os.MkdirAll(dest, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ancestor, 0111); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(ancestor, 0700)
	if err := syncParents(dest, store); err != nil {
		t.Fatal("publication required read permission above the store", err)
	}
}
