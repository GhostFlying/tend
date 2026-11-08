package release

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	if runtime.GOOS == "linux" {
		verifyComponentFixture(t, executable, commit)
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

func verifyComponentFixture(t *testing.T, executable, commit string) {
	t.Helper()
	dir := t.TempDir()
	body, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	c := Component{Format: 1, ReleaseID: "fixture", Commit: commit, State: Range{Min: 1, Max: 1}, Artifact: Artifact{Role: "maintainer", OS: "linux", Arch: runtime.GOARCH, Path: "maintainer", Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:])}}
	if err := os.WriteFile(filepath.Join(dir, "maintainer"), body, 0700); err != nil {
		t.Fatal(err)
	}
	write := func() {
		t.Helper()
		raw, _ := json.Marshal(c)
		if err := os.WriteFile(filepath.Join(dir, "component.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	if b, err := LoadService(dir, "maintainer"); err != nil || b.Manifest.Storage.ServerSchema.Max != 1 {
		t.Fatal("valid component rejected", b, err)
	}
	raw, _ := json.Marshal(c)
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("standalone component changed the legacy service format")
	}
	c.Commit = strings.Repeat("a", 40)
	write()
	if _, err := LoadComponent(dir); err == nil {
		t.Fatal("different executable source accepted")
	}
	c.Commit = commit
	write()
	body[0] ^= 1
	if err := os.WriteFile(filepath.Join(dir, "maintainer"), body, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadComponent(dir); err == nil {
		t.Fatal("corrupt artifact accepted")
	}
}
