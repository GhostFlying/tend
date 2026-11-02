//go:build !windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReportsPreserveNativeEventsWithoutOverwritingEarlierRuns(t *testing.T) {
	directory := t.TempDir()
	selected := filepath.Join(directory, "selected go")
	script := `#!/bin/sh
case "$1" in
  list) printf '%s\n' 'example/package' ;;
  test) printf '%s\n' '{"Action":"pass","Package":"example/package","Test":"TestOne","Elapsed":1.25}' ;;
esac
`
	if err := os.WriteFile(selected, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	reports := filepath.Join(directory, "reports")
	for range 2 {
		if err := check(t.Context(), selected, 1, 1, 1, "1m", "other", reports, []string{"example/package"}); err != nil {
			t.Fatal(err)
		}
	}
	files, err := filepath.Glob(filepath.Join(reports, "*", "*.jsonl"))
	if err != nil || len(files) != 2 {
		t.Fatal(files, err)
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil || !strings.Contains(string(data), `"Elapsed":1.25`) {
			t.Fatal("native timing event lost", string(data), err)
		}
	}
}

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
	for _, group := range []string{"app", "other", "e2e", "api", "mainthread"} {
		pkg := "example/package"
		if group == "app" {
			pkg = appPackage
		} else if group == "e2e" {
			pkg = e2ePackage
		} else if group != "other" {
			pkg = "github.com/GhostFlying/tend/internal/" + group
		}
		t.Setenv("PACKAGE", pkg)
		if err := check(context.Background(), selected, 2, 2, 1, "1m", group, "", []string{pkg}); err != nil {
			t.Fatal(group, err)
		}
	}
	commands, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(commands), "-run ") != 8 || !strings.Contains(string(commands), "-count 1") || strings.Count("\n"+string(commands), "\nlist ") != 5 {
		t.Fatalf("discovery, isolated tests or shards missing: %s", commands)
	}
}

func TestFailedShardDoesNotSkipItsPeers(t *testing.T) {
	directory := t.TempDir()
	selected, record := filepath.Join(directory, "go"), filepath.Join(directory, "runs")
	script := `#!/bin/sh
case "$1" in
 list) printf '%s\n' "$PACKAGE" ;;
 test) case "$*" in
  *-list*) printf 'TestFailed\nTestPeer\n' ;;
  *) printf '%s\n' "$*" >> "$RECORD"
     case "$*" in *TestFailed*) exit 7 ;; esac ;;
 esac ;;
esac
`
	if err := os.WriteFile(selected, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PACKAGE", e2ePackage)
	t.Setenv("RECORD", record)
	if err := check(t.Context(), selected, 1, 2, 1, "1m", "e2e", "", []string{e2ePackage}); err == nil {
		t.Fatal("failed shard was ignored")
	}
	data, err := os.ReadFile(record)
	if err != nil || strings.Count(string(data), "-run ") != 2 || !strings.Contains(string(data), "TestPeer") {
		t.Fatal("peer shard did not finish", string(data), err)
	}
}
