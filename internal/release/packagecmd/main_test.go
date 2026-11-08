package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestArchiveReproducibilityAndOutputConflict(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input")
	if err := os.Mkdir(input, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(input, "tend"), []byte("fixed executable bytes"), 0o700); err != nil {
		t.Fatal(err)
	}
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	stamp := time.Unix(12345, 0)
	if err := pack(input, a, stamp); err != nil {
		t.Fatal(err)
	}
	if err := pack(input, b, stamp); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(a)
	second, _ := os.ReadFile(b)
	if string(first) != string(second) {
		t.Fatal("same source produced different archive bytes")
	}
	if err := publishFile(b, a); err != nil {
		t.Fatal("identical retry rejected", err)
	}
	if err := os.WriteFile(b, []byte("different"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := publishFile(b, a); err == nil {
		t.Fatal("conflicting output replaced")
	}
}
