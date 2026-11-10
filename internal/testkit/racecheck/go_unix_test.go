//go:build !windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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

func TestIndependentGoSubprocessesReceiveDefaultsOrPreserveParentBudget(t *testing.T) {
	for _, tc := range []struct{ name, flags, procs, wantFlags, wantProcs string }{
		{"standalone", "", "", "-p=1", "4"},
		{"parent check", "-trimpath -p=3", "2", "-trimpath -p=3", "2"},
		{"explicit package", "-p=2", "", "-p=2", "2"},
		{"explicit threads", "-trimpath", "3", "-trimpath -p=1", "3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			selected, record := filepath.Join(directory, "selected go"), filepath.Join(directory, "commands")
			script := `#!/bin/sh
printf '%s|%s|%s\n' "$GOFLAGS" "$GOMAXPROCS" "$*" >> "$RECORD"
case "$1" in
 list) printf '%s\n' 'example/store' 'example/sandbox' ;;
esac
`
			if err := os.WriteFile(selected, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("RECORD", record)
			t.Setenv("GOFLAGS", tc.flags)
			t.Setenv("GOMAXPROCS", tc.procs)
			if err := checkWithCPUs(t.Context(), 4, selected, 4, 4, 0, "1m", "other", "", []string{"./..."}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(record)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(lines) != 2 {
				t.Fatal("package discovery or complete package run lost", lines)
			}
			for _, line := range lines {
				parts := strings.SplitN(line, "|", 3)
				if len(parts) != 3 {
					t.Fatal("invalid fake Go observation", line)
				}
				// Discovery uses a single-process phase; the actual test phase
				// must budget both selected packages for an explicit -p=2.
				wantProcs := tc.wantProcs
				if strings.HasPrefix(parts[2], "list ") && tc.name == "explicit package" {
					wantProcs = "4"
				}
				if parts[0] != tc.wantFlags || parts[1] != wantProcs || strings.Contains(parts[2], "-count") {
					t.Fatal("inherited budget or native cache changed", line)
				}
			}
			if !strings.Contains(string(data), "example/sandbox example/store") {
				t.Fatal("new packages omitted", string(data))
			}
		})
	}
}

func TestBudgetedAllEntryPointDiscoversEveryRootAndKeepsExplicitCount(t *testing.T) {
	directory := t.TempDir()
	selected, record := filepath.Join(directory, "selected go"), filepath.Join(directory, "commands")
	script := `#!/bin/sh
printf '%s|%s|%s\n' "$GOFLAGS" "$GOMAXPROCS" "$*" >> "$RECORD"
case "$1" in
 list) printf '%s\n' "$PACKAGES" ;;
 test) case "$*" in *-list*) printf '%s\n' TestOne TestTwo ExampleNew FuzzNew ;; esac ;;
esac
`
	if err := os.WriteFile(selected, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	sharded := []string{appPackage, e2ePackage, "github.com/GhostFlying/tend/internal/api", "github.com/GhostFlying/tend/internal/mainthread"}
	t.Setenv("PACKAGES", strings.Join(append(slices.Clone(sharded), "example/new", "example/store"), "\n"))
	t.Setenv("RECORD", record)
	t.Setenv("GOFLAGS", "-trimpath")
	t.Setenv("GOMAXPROCS", "")
	if err := checkWithCPUs(t.Context(), 16, selected, 2, 2, 1, "1m", "all", "", []string{"./..."}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]int)
	unsharded := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		parts := strings.SplitN(line, "|", 3)
		if len(parts) != 3 || parts[0] != "-trimpath -p=1" {
			t.Fatal("Go subprocess omitted package budget", line)
		}
		args := strings.Fields(parts[2])
		if args[0] == "list" {
			continue
		}
		if parts[1] != "2" {
			t.Fatal("concurrent shard phase omitted thread budget", line)
		}
		if slices.Contains(args, "-list") {
			continue
		}
		if !strings.Contains(parts[2], "-race -timeout 1m -count 1") {
			t.Fatal("race, original deadline or explicit count lost", line)
		}
		i := slices.Index(args, "-run")
		if i < 0 {
			unsharded++
			if !slices.Contains(args, "example/new") || !slices.Contains(args, "example/store") {
				t.Fatal("unrecognized packages dropped", args)
			}
			continue
		}
		pattern := regexp.MustCompile(args[i+1])
		pkg := args[len(args)-1]
		for _, root := range []string{"TestOne", "TestTwo", "ExampleNew", "FuzzNew"} {
			if pattern.MatchString(root) {
				seen[pkg+"/"+root]++
			}
		}
	}
	if unsharded != 1 {
		t.Fatal("unsharded phase omitted or repeated", unsharded)
	}
	for _, pkg := range sharded {
		for _, root := range []string{"TestOne", "TestTwo", "ExampleNew", "FuzzNew"} {
			if seen[pkg+"/"+root] != 1 {
				t.Fatal("discovered root omitted or repeated", seen)
			}
		}
	}
}
