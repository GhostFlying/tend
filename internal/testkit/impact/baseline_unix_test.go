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
