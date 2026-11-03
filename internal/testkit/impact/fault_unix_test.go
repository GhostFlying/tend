//go:build !windows

package impact

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestWebFaultCannotAcceptStaleReportsAfterStartupFailure(t *testing.T) {
	tmp := t.TempDir()
	if err := WriteJSON(filepath.Join(tmp, "full-web.json"), map[string]any{
		"testResults": []any{map[string]any{"name": filepath.Join(tmp, "source/web/src/old.test.ts"), "assertionResults": []any{map[string]string{"status": "failed"}}}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := WriteJSON(filepath.Join(tmp, "web-result.json"), WebExecution{Files: []string{"src/old.test.ts"}, ExecutedFiles: []string{"src/old.test.ts"}, FailedFiles: []string{"src/old.test.ts"}, Completed: true}); err != nil {
		t.Fatal(err)
	}
	pnpm := filepath.Join(tmp, "pnpm")
	if err := os.WriteFile(pnpm, []byte("#!/bin/sh\nif [ \"$1\" = install ]; then exit 0; fi\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PNPM", pnpm)
	if err := os.MkdirAll(filepath.Join(tmp, "source/web"), 0o700); err != nil {
		t.Fatal(err)
	}
	e := FaultEvidence{Name: "startup", Plan: Plan{Web: WebPlan{Run: true, Full: true}}}
	if err := webFault(t.Context(), FaultOptions{Log: io.Discard}, filepath.Join(tmp, "source"), tmp, &e); err == nil || e.Passed {
		t.Fatal("stale fault reports were accepted", e, err)
	}
}
