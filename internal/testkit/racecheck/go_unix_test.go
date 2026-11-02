//go:build !windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelectedGoIsUsedWithoutGoOnPath(t *testing.T) {
	directory := t.TempDir()
	selected := filepath.Join(directory, "selected go")
	record := filepath.Join(directory, "commands")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$RECORD"
case "$1" in
  list) printf '%s\n' "$PACKAGE" ;;
  test) case "$*" in *-list*) printf 'TestOne\nTestTwo\n' ;; esac ;;
esac
`
	if err := os.WriteFile(selected, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	t.Setenv("RECORD", record)
	for _, group := range []string{"app", "other", "e2e"} {
		pkg := "example/package"
		if group == "app" {
			pkg = appPackage
		} else if group == "e2e" {
			pkg = e2ePackage
		}
		t.Setenv("PACKAGE", pkg)
		if err := check(context.Background(), selected, 2, 1, "1m", group, []string{pkg}); err != nil {
			t.Fatal(group, err)
		}
	}
	commands, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(commands), "-run ") != 2 || !strings.Contains(string(commands), "-count 1") || strings.Count("\n"+string(commands), "\nlist ") != 3 {
		t.Fatalf("discovery, isolated tests or shards missing: %s", commands)
	}
}
