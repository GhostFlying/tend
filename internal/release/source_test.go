package release

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecutableSourceReadsPinnedBytesAndDetectsModifiedSource(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	dir := t.TempDir()
	run := func(name string, args ...string) string {
		t.Helper()
		c := exec.CommandContext(t.Context(), name, args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid", "GOWORK=off", "GOPROXY=off")
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v: %s", name, args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	for name, body := range map[string]string{"go.mod": "module example.invalid/sourcefixture\n\ngo 1.24\n", "main.go": "package main\nfunc main() {}\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	run("git", "init", "-q")
	run("git", "add", "go.mod", "main.go")
	run("git", "commit", "-qm", "fixture source")
	commit := run("git", "rev-parse", "HEAD")
	// The executable is outside the source tree so its own bytes cannot dirty it.
	executable := filepath.Join(t.TempDir(), "fixture")
	run("go", "build", "-buildvcs=true", "-o", executable, ".")
	got, err := ExecutableSource(executable)
	if err != nil || got.Commit != commit || got.Modified {
		t.Fatal(got, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main() { println(1) }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run("go", "build", "-buildvcs=true", "-o", executable, ".")
	got, err = ExecutableSource(executable)
	if err != nil || got.Commit != commit || !got.Modified {
		t.Fatal(got, err)
	}
	if _, err := ExecutableSource(filepath.Join(dir, "main.go")); err == nil {
		t.Fatal("source text accepted as executable identity")
	}
}
