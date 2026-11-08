//go:build linux

package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/release"
)

func TestMaintainerComponentUsesExistingControlledInstallation(t *testing.T) {
	h := newHarness(t)
	if err := h.s.Close(); err != nil {
		t.Fatal(err)
	}
	h.cfg.Role = "maintainer"
	h.cfg.InstanceID = "maintenance-test"
	h.cfg.StateDir = filepath.Join(t.TempDir(), "supervisor")
	h.cfg.StorageVersion = 1
	var err error
	h.s, err = Open(h.cfg, h.opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.s.Bootstrap(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	h.bundle = maintainerComponentFixture(t)
	if err := h.s.Upgrade(context.Background(), "maintenance-install", h.bundle); err != nil {
		t.Fatal(err)
	}
	for range 12 {
		if err := h.s.Step(context.Background()); err != nil {
			t.Fatal(err)
		}
		if h.s.Status().Operation.Phase == "committed" {
			break
		}
	}
	s := h.s.Status()
	if s.Operation.Phase != "committed" || s.Current.Artifact.Role != "maintainer" || s.Current.StorageVersion != 1 || h.p.starts != 1 || h.p.stops != 1 {
		t.Fatalf("maintenance installation did not complete: %+v", s)
	}
	if err := h.s.Upgrade(context.Background(), "maintenance-install", h.bundle); err != nil {
		t.Fatal("terminal retry changed installation identity", err)
	}
}

func maintainerComponentFixture(t *testing.T) string {
	t.Helper()
	source := t.TempDir()
	bundle := t.TempDir()
	for name, body := range map[string]string{"go.mod": "module example.invalid/maintainerfixture\n\ngo 1.24\n", "main.go": "package main\nfunc main() {}\n"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	run := func(name string, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), name, args...)
		cmd.Dir = source
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid", "GOWORK=off", "GOPROXY=off")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("fixture %s: %v: %s", name, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("git", "init", "-q")
	run("git", "add", "go.mod", "main.go")
	run("git", "commit", "-qm", "fixture source")
	commit := run("git", "rev-parse", "HEAD")
	path := filepath.Join(bundle, "tend-maintainer")
	run("go", "build", "-buildvcs=true", "-o", path, ".")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	c := release.Component{Format: 1, ReleaseID: "fixture-maintainer", Commit: commit, State: release.Range{Min: 1, Max: 1}, Artifact: release.Artifact{Role: "maintainer", OS: "linux", Arch: runtime.GOARCH, Path: "tend-maintainer", Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:])}}
	raw, _ := json.Marshal(c)
	if err := os.WriteFile(filepath.Join(bundle, "component.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	return bundle
}
