//go:build linux && integration

package codexenv

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/procx"
)

func TestIntegrationNativeProbePreservesCancellationIdentity(t *testing.T) {
	if err := platformCheck(t.Context()); err != nil {
		t.Skip(err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\nexec /bin/sleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	s, p := fixture(t)
	p.UserHome, _ = os.UserHomeDir()
	s.Validate = nil
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	_, err := s.Execute(ctx, Request{Action: "create", ID: p.ID, RequestID: "cancelled-probe", Profile: &p})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("native EOF hid probe cancellation", err)
	}
	status, err := s.Read(p.ID)
	if err != nil || len(status.PendingRequestIDs) != 1 {
		t.Fatal("cancelled probe was not recoverable by original request", err)
	}
}

func TestIntegrationNativeCleanAndProjection(t *testing.T) {
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("native Codex unavailable")
	}
	if err := platformCheck(t.Context()); err != nil {
		t.Skip(err)
	}
	s, p := fixture(t)
	p.UserHome, _ = os.UserHomeDir()
	s.Validate = nil
	p.Skills = Selection{Mode: "projection", Allow: []Item{{ID: "selected", Path: skill(t, t.TempDir(), "selected", "Native probe")}}}
	status := create(t, s, p)
	if status.Latest == "" {
		t.Fatal("native probe did not publish")
	}
	ref, err := (&Binding{Directory: s.Root, ID: p.ID}).Pin(t.Context(), "integration:process")
	if err != nil {
		t.Fatal(err)
	}
	// The actual user home is untouched; its discovery directory is substituted
	// only in the child mount namespace, also through its canonical alias.
	cmd, cleanup, err := ref.Command(t.Context(), "/bin/sh", []string{"-c", `test "$HOME" = "$1" && test "$CODEX_HOME" = "$2" && test -f "$HOME/.agents/skills/$3/SKILL.md" && test "$(ls -A "$HOME/.agents/skills" | wc -l)" -eq 1`, "probe", os.Getenv("HOME"), p.Home, digest([]byte("selected"))[:16]}, t.TempDir(), os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	procx.Contain(cmd)
	out, err := cmd.CombinedOutput()
	procx.KillGroup(cmd)
	if err != nil {
		t.Fatalf("mount view: %v %s", err, out)
	}
	// Codex's own catalog validation above creates no model turn. Exercise one
	// explicit model turn only when the integration caller selects a model.
	if model := os.Getenv("TEND_CODEX_ENV_MODEL"); model != "" {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		defer cancel()
		p.AuthSource = filepath.Join(p.UserHome, ".codex", "auth.json")
		ref.Profile = p
		cmd, cleanup, err = ref.Command(ctx, "codex", []string{"exec", "--skip-git-repo-check", "--model", model, "--json", "Reply exactly ENVIRONMENT_OK. Do not run tools."}, t.TempDir(), os.Environ())
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		procx.Contain(cmd)
		out, err = cmd.CombinedOutput()
		procx.KillGroup(cmd)
		if err != nil {
			t.Fatalf("model probe failed: %v", err)
		}
		if !strings.Contains(string(out), "ENVIRONMENT_OK") {
			t.Fatal("model probe did not return expected output")
		}
		t.Log("minimal model turn confirmed; persistent sessions remain under the isolated home")
	}
	raw, _ := json.Marshal(map[string]any{"generation": status.Latest, "packages": len(status.Snapshots[0].Packages)})
	t.Log(string(raw))
}
