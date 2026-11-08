package nativeusage

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A large metadata line reproduces the measured idle-history parsing cost.
func BenchmarkCaughtUpHistory(b *testing.B) {
	path := filepath.Join(b.TempDir(), "history.jsonl")
	raw, _ := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"id": "thread", "session_id": "session", "instructions": strings.Repeat("x", 64<<10)}})
	if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
		b.Fatal(err)
	}
	src := Source{Path: path, Provider: "traex", ThreadID: "thread", SessionID: "session"}
	page, err := Read(context.Background(), src, Cursor{}, 256)
	if err != nil {
		b.Fatal(err)
	}
	for _, cached := range []bool{false, true} {
		name := "full-header"
		if cached {
			name = "cached-header"
		}
		b.Run(name, func(b *testing.B) {
			index := NewDiscovery()
			b.ReportAllocs()
			for b.Loop() {
				var err error
				if cached {
					_, err = index.Read(context.Background(), src, page.Cursor, 256, false)
				} else {
					_, err = Read(context.Background(), src, page.Cursor, 256)
				}
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
