package gitws

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestGitBaselineRecoversAfterPartialWrite(t *testing.T) {
	// Isolate the real file-size limit and SIGXFSZ handler from all other tests.
	if os.Getenv("TEND_TEST_BASELINE_PARTIAL_WRITE") != "1" {
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestGitBaselineRecoversAfterPartialWrite$", "-test.count=1")
		cmd.Env = append(os.Environ(), "TEND_TEST_BASELINE_PARTIAL_WRITE=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("partial-write subprocess: %s %v", out, err)
		}
		return
	}
	f := newFixture(t, nil)
	as := f.implement()
	as.Workspace.ID = "ws_write_failure"
	as.Spec = json.RawMessage(`{"deliverables":["report"]}`)
	p := f.prepare(as)
	write(t, filepath.Join(p.Dir, "tend", "README"), strings.Repeat("changed evidence\n", 30000))
	as.TaskID = "task_followup_partial_write"
	as.RunID = "run_followup_partial_write"
	as.Workspace.Reused = true
	var original syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &original); err != nil {
		t.Fatal(err)
	}
	limited := original
	limited.Cur = 65536
	signal.Ignore(syscall.SIGXFSZ)
	defer signal.Reset(syscall.SIGXFSZ)
	defer syscall.Setrlimit(syscall.RLIMIT_FSIZE, &original)
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limited); err != nil {
		t.Fatal(err)
	}
	_, writeErr := f.ws.Prepare(t.Context(), as)
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &original); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.ws.home(as), ".tend", "baselines", as.TaskID+".json")
	var pe *os.PathError
	if !errors.As(writeErr, &pe) || pe.Path != path+".tmp" || !errors.Is(writeErr, syscall.EFBIG) {
		t.Fatalf("fault missed partial baseline write: %v", writeErr)
	}
	for _, p := range []string{path, path + ".tmp"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("failed write left poisoned path %s: %v", p, err)
		}
	}
	q, err := f.ws.Prepare(t.Context(), as)
	if err != nil {
		t.Fatalf("retry after storage recovery: %v", err)
	}
	full, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var mounts []baselineMount
	if err := json.Unmarshal(full, &mounts); err != nil || len(mounts) != 1 || mounts[0].Unstaged == "" {
		t.Fatalf("retry baseline: %s %v", q.Report.Baseline, err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temporary baseline not removed: %v", err)
	}
}
