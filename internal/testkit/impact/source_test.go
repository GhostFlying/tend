package impact

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitFixture(t *testing.T) (string, func(...string) string) {
	t.Helper()
	root := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Fixture User", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture User", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatal("fixture git", args, err, string(out))
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("baseline\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-qm", "fixture baseline")
	git("update-ref", "refs/remotes/origin/main", "HEAD")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("candidate\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-qm", "fixture candidate")
	return root, git
}

func TestSavedPlanRejectsChangedWorktreeBaselineAndImmutableDirtySource(t *testing.T) {
	root, git := gitFixture(t)
	// The saved plan is outside the source tree and is not another business ledger.
	path := filepath.Join(t.TempDir(), "plan.json")
	p, err := Load(t.Context(), root, Options{Worktree: true})
	if err != nil || p.Full {
		t.Fatal(p, err)
	}
	if err := Write(path, p); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(t.Context(), root, path); err != nil {
		t.Fatal("unchanged plan rejected", err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("different candidate\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(t.Context(), root, path); err == nil {
		t.Fatal("stale source fingerprint accepted")
	}
	if _, err := Load(t.Context(), root, Options{}); err == nil {
		t.Fatal("immutable plan ignored dirty source")
	}
	git("checkout", "--", "README.md")
	git("update-ref", "refs/remotes/origin/main", "HEAD")
	if _, err := Read(t.Context(), root, path); err == nil {
		t.Fatal("new baseline reused old plan")
	}
}

func TestPlanPropagatesCancellationAndDiscoveryFailureSelectsFull(t *testing.T) {
	root, _ := gitFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Load(ctx, root, Options{Worktree: true}); err == nil {
		t.Fatal("cancelled source read succeeded")
	}
	if err := os.MkdirAll(filepath.Join(root, "internal", "new"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal", "new", "new.go"), []byte("package new\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := Load(t.Context(), root, Options{Worktree: true, Go: "definitely-unavailable-go-tool"})
	if err != nil || !p.Full {
		t.Fatal("missing graph silently omitted coverage", p, err)
	}
}

func TestPlanKeepsBudgetOverridesButRejectsChangedBuildTags(t *testing.T) {
	root, _ := gitFixture(t)
	path := filepath.Join(t.TempDir(), "plan.json")
	t.Setenv("GOFLAGS", "-tags=fixture")
	p, err := Load(t.Context(), root, Options{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := Write(path, p); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOFLAGS", "-tags=fixture -p=2")
	if _, err := Read(t.Context(), root, path); err != nil {
		t.Fatal("parent CPU budget invalidated unchanged source", err)
	}
	t.Setenv("GOFLAGS", "-tags=different -p=2")
	if _, err := Read(t.Context(), root, path); err == nil {
		t.Fatal("different build graph reused a stale plan")
	}
}
