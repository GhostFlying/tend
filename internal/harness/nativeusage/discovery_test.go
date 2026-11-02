package nativeusage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverySharesStoreScanAndRefreshesOncePerFinalSweep(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(name, id, session string) string {
		t.Helper()
		path := filepath.Join(dir, name+".jsonl")
		if err := os.WriteFile(path, []byte(line("session_meta", 0, map[string]string{"id": id, "session_id": session})), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	a := Source{Path: write("a", "a", "a"), ThreadID: "a", Provider: "codex"}
	b := Source{Path: write("b", "b", "b"), ThreadID: "b", Provider: "codex"}
	index := NewDiscovery()
	cycle := index.Cycle(1000)
	if _, err := cycle.Discover(t.Context(), a, false); err != nil {
		t.Fatal(err)
	}
	write("b-continuation", "b2", "b")
	if files, err := cycle.Discover(t.Context(), b, false); err != nil || len(files) != 1 {
		t.Fatal("second source rescanned the shared store", files, err)
	}
	cycle = index.Cycle(2000)
	if files, err := cycle.Discover(t.Context(), b, true); err != nil || len(files) != 2 {
		t.Fatal("final writer scan missed its continuation", files, err)
	}
	write("b-later", "b3", "b")
	if files, err := cycle.Discover(t.Context(), b, true); err != nil || len(files) != 2 {
		t.Fatal("same sweep repeated the forced scan", files, err)
	}
	if files, err := index.Cycle(32000).Discover(t.Context(), b, false); err != nil || len(files) != 3 {
		t.Fatal("expired index missed new history", files, err)
	}
}

func TestNewRegisteredFileIsReadBeforeSharedIndexExpires(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "root.jsonl")
	if err := os.WriteFile(path, []byte(line("session_meta", 0, map[string]string{"id": "root", "session_id": "root"})), 0600); err != nil {
		t.Fatal(err)
	}
	index := NewDiscovery()
	source := Source{Path: path, Provider: "traex", ThreadID: "root", SessionID: "root"}
	if _, err := index.Cycle(1000).Discover(t.Context(), source, false); err != nil {
		t.Fatal(err)
	}
	continued := filepath.Join(dir, "continuation.jsonl")
	if err := os.WriteFile(continued, []byte(line("session_meta", 0, map[string]string{"id": "continued", "session_id": "root"})), 0600); err != nil {
		t.Fatal(err)
	}
	source.Path = continued
	files, err := index.Cycle(2000).Discover(t.Context(), source, false)
	if err != nil || len(files) != 2 {
		t.Fatal("new explicit source waited for the metadata cache", files, err)
	}
}
