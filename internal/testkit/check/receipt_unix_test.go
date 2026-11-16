//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestTaskReceiptsPreserveNativeExitAndNeverStartCancelledWork(t *testing.T) {
	tools, reports := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(tools, "make"), []byte("#!/bin/sh\nexit 7\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := runTaskReport(t.Context(), task{name: "failed", env: os.Environ()}, reports); err == nil {
		t.Fatal("native failure was hidden")
	}
	failed := readReceipt(t, reports, "failed")
	if failed.ExitCode == nil || *failed.ExitCode != 7 || failed.PID == 0 || failed.Outcome != "failed" || !failed.CleanupCompleted || failed.Finished.Before(failed.Started) {
		t.Fatal("native terminal receipt lost facts", failed)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := runTaskReport(ctx, task{name: "not-started", env: os.Environ()}, reports); err == nil {
		t.Fatal("cancelled work succeeded")
	}
	notStarted := readReceipt(t, reports, "not-started")
	if notStarted.ExitCode != nil || notStarted.PID != 0 || notStarted.Outcome != "not_started" || notStarted.Cause == "" {
		t.Fatal("unstarted work fabricated a process result", notStarted)
	}
	files, err := filepath.Glob(filepath.Join(reports, ".receipt-*"))
	if err != nil || len(files) != 0 {
		t.Fatal("atomic receipt temporary files leaked", files, err)
	}
}

func readReceipt(t *testing.T, root, name string) receipt {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var value receipt
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}
