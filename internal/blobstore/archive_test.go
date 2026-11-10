package blobstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOriginalWindowsRemainImmutableAcrossAppendsAndCopies(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("中😀ab", 5000)
	first, err := store.AppendOriginal(t.Context(), "session", []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.AppendOriginal(t.Context(), "session", []byte("later"))
	if err != nil {
		t.Fatal(err)
	}
	if second.Offset != first.Length || second.Archive != first.Archive {
		t.Fatal(first, second)
	}
	selected, length, err := store.ReadTextWindow(t.Context(), first, 15999, 16000)
	if err != nil || selected != string([]rune(text)[15999:]) || length != 20000 {
		t.Fatalf("text window length=%d: %v", length, err)
	}
	copied, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = store.CopyOriginalPrefix(t.Context(), copied, first.Archive, first.Length); err != nil {
		t.Fatal(err)
	}
	raw, err := copied.ReadWindow(t.Context(), first)
	if err != nil || string(raw) != text {
		t.Fatal("prefix copy differs", err)
	}
	if _, err = copied.ReadWindow(t.Context(), second); err == nil {
		t.Fatal("snapshot copied future append")
	}
	p, err := copied.archivePath(first.Archive)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p)
	if err != nil || info.Size() != first.Length {
		t.Fatal(info, err)
	}
	first.Length = 1 << 60
	if _, err = copied.ReadWindow(t.Context(), first); err == nil {
		t.Fatal("out-of-bounds archive allocation accepted")
	}
	if _, _, err = copied.ReadTextWindow(t.Context(), first, 0, 1); err == nil {
		t.Fatal("out-of-bounds original text accepted")
	}
	if _, err = os.Stat(filepath.Join(copied.root, "archives")); err != nil {
		t.Fatal(err)
	}
}
