//go:build !windows

package impact

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestBaselineCLIUsesActualFullEventsAndPreservesPreviousIndexOnFailure(t *testing.T) {
	dir := fixtureRepo(t)
	fixtureWrite(t, dir, "process_test.go", "package fixture\nimport \"os/exec\"\nvar _ = exec.Command\n")
	fixtureCommit(t, dir)
	runner := filepath.Join(t.TempDir(), "full")
	if err := os.WriteFile(runner, []byte("#!/bin/sh\nset -eu\nmkdir -p \"$TEST_REPORT_DIR/race-all-1\"\ngo test -race -count=1 -json ./... > \"$TEST_REPORT_DIR/race-all-1/all.jsonl\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(dir, ".local/index.json")
	tools, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"run", "./cmd", "baseline", "-dir", dir, "-index", indexPath, "-check-runner", runner}
	if _, err := output(t.Context(), tools, "go", args...); err != nil {
		t.Fatal(err)
	}
	index, err := ReadIndex(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range index.Executions {
		if entry.Mode != "static-process" {
			t.Fatal(entry)
		}
	}
	before, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runner, []byte("#!/bin/sh\nset -eu\nmkdir -p \"$TEST_REPORT_DIR/race-all-1\"\necho '{\"Action\":\"pass\",\"Package\":\"example.test/fixture\"}' > \"$TEST_REPORT_DIR/race-all-1/all.jsonl\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := output(t.Context(), tools, "go", args...); err == nil {
		t.Fatal("empty full run published a baseline")
	}
	after, err := os.ReadFile(indexPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed refresh overwrote successful index", err)
	}
}

func TestCompiledChecksPreserveMakefileControls(t *testing.T) {
	t.Setenv("CHECK_JOBS", "4")
	t.Setenv("APP_TEST_SHARDS", "3")
	t.Setenv("TEST_SHARDS", "2")
	t.Setenv("PNPM", "selected-pnpm")
	args := CheckBudgetArgs()
	for _, value := range []string{"-jobs", "4", "-shards", "3", "-suite-shards", "2", "-pnpm", "selected-pnpm"} {
		if !slices.Contains(args, value) {
			t.Fatal(args)
		}
	}
	if PNPMTool() != "selected-pnpm" {
		t.Fatal("lost pnpm override")
	}
}

func TestBaselineCLITracksUnboundCacheLinksWithoutFunctionAttribution(t *testing.T) {
	dir := fixtureRepo(t)
	cache := filepath.Join(dir, ".pnpm-store/v10/projects")
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"directory": t.TempDir(), "dangling": filepath.Join(t.TempDir(), "absent")} {
		if err := os.Symlink(target, filepath.Join(cache, name)); err != nil {
			t.Fatal(err)
		}
	}
	fixtureCommit(t, dir)
	runner := filepath.Join(t.TempDir(), "full")
	if err := os.WriteFile(runner, []byte("#!/bin/sh\nset -eu\nmkdir -p \"$TEST_REPORT_DIR/race-all-1\"\ngo test -race -count=1 -json ./... > \"$TEST_REPORT_DIR/race-all-1/all.jsonl\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	tools, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".local/index.json")
	if _, err := output(t.Context(), tools, "go", "run", "./cmd", "baseline", "-dir", dir, "-index", path, "-check-runner", runner); err != nil {
		t.Fatal(err)
	}
	index, err := ReadIndex(path)
	if err != nil || !index.Source.UnboundResources {
		t.Fatal(index, err)
	}
	for _, row := range index.Executions {
		if row.Mode != "static-resource" || len(row.Functions) != 0 {
			t.Fatal("unbound input claimed function coverage", row)
		}
	}
	current := index.Source
	for _, unbound := range []bool{true, false} {
		current.UnboundResources = unbound
		if !Select(current, index, "").Full {
			t.Fatal("resource fallback baseline narrowed after link removal")
		}
	}
	index.Executions[0].Mode = "coverage"
	if err := WriteJSON(path, index); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadIndex(path); err == nil {
		t.Fatal("resource fallback accepted function attribution")
	}
}
