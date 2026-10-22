package gitws

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestDirectoryWorkspaceProfilesUseRootAndPersistentReuseCache(t *testing.T) {
	for _, withInputs := range []bool{false, true} {
		name := "run once"
		if withInputs {
			name = "rerun changed inputs"
		}
		t.Run(name, func(t *testing.T) {
			profile := Profile{Commands: [][]string{{"sh", "-c", `pwd > profile-root; echo run >> profile-count`}}}
			if withInputs {
				profile.RerunIfChanged = []string{"deps.txt"}
			}
			cfg := Config{Home: t.TempDir(), Profiles: map[string]Profile{"setup": profile}}
			w := New(cfg)
			as := protocol.Assignment{TaskID: "original", RunID: "impl", Role: protocol.RoleImplement, Workspace: protocol.AssignmentWorkspace{ID: "ws_profile", Kind: "directory", Profile: "setup"}}
			p, err := w.Prepare(t.Context(), as)
			if err != nil {
				t.Fatal(err)
			}
			root, err := os.ReadFile(filepath.Join(p.Dir, "profile-root"))
			if err != nil || strings.TrimSpace(string(root)) != p.Dir {
				t.Fatalf("profile did not run in physical directory root: %q %v", root, err)
			}
			// Profile outputs belong to the initial prepared baseline, before worker work.
			baseline, err := directoryBaseline(t.Context(), p.Dir, as.TaskID)
			if err != nil || !strings.Contains(string(baseline), "profile-count") {
				t.Fatalf("baseline captured before profile: %s %v", baseline, err)
			}
			count := func() int {
				b, err := os.ReadFile(filepath.Join(p.Dir, "profile-count"))
				if err != nil {
					t.Fatal(err)
				}
				return strings.Count(string(b), "run")
			}
			// A new adapter instance must use the on-disk profile cache.
			w = New(cfg)
			if _, err := w.Prepare(t.Context(), as); err != nil {
				t.Fatal(err)
			}
			as.Role = protocol.RoleAccept
			as.RunID = "accept"
			if _, err := w.Prepare(t.Context(), as); err != nil {
				t.Fatal(err)
			}
			as.Role = protocol.RoleImplement
			as.TaskID = "followup"
			as.RunID = "reuse"
			as.Workspace.Reused = true
			as.Workspace.Path = p.Dir
			if _, err := w.Prepare(t.Context(), as); err != nil {
				t.Fatal(err)
			}
			if count() != 1 {
				t.Fatalf("unchanged inherited profile reran: %d", count())
			}
			if err := os.WriteFile(filepath.Join(p.Dir, "deps.txt"), []byte("changed"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := w.Prepare(t.Context(), as); err != nil {
				t.Fatal(err)
			}
			want := 1
			if withInputs {
				want = 2
			}
			if count() != want {
				t.Fatalf("profile runs=%d want=%d", count(), want)
			}
		})
	}
}

func TestDirectoryWorkspaceRejectsUnknownInheritedProfile(t *testing.T) {
	for _, reused := range []bool{false, true} {
		name := "new"
		if reused {
			name = "reuse"
		}
		t.Run(name, func(t *testing.T) {
			cfg := Config{Home: t.TempDir()}
			w := New(cfg)
			as := protocol.Assignment{TaskID: "original", RunID: "impl", Role: protocol.RoleImplement, Workspace: protocol.AssignmentWorkspace{ID: "ws_unknown_profile", Kind: "directory"}}
			as.Workspace.Profile = "unknown"
			if reused {
				// The source selected this profile successfully. A later runner
				// configuration lost it; the inherited selection must still fail.
				cfg.Profiles = map[string]Profile{"unknown": {}}
				if _, err := New(cfg).Prepare(t.Context(), as); err != nil {
					t.Fatal(err)
				}
				cfg.Profiles = nil
				w = New(cfg)
				as.TaskID = "followup"
				as.RunID = "followup"
				as.Workspace.Reused = true
			}
			if _, err := w.Prepare(t.Context(), as); err == nil || !strings.Contains(err.Error(), "no workspace profile") {
				t.Fatalf("unknown profile accepted: %v", err)
			}
			if _, err := os.Stat(filepath.Join(w.home(as), ".tend", "baselines", as.TaskID+".json")); !os.IsNotExist(err) {
				t.Fatalf("failed prepare recorded a baseline: %v", err)
			}
		})
	}
}

func TestDirectoryWorkspaceFailedProfileRemainsRetryable(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	cfg := Config{Home: t.TempDir(), Env: []string{"PROFILE_READY=" + ready}, Profiles: map[string]Profile{"setup": {Commands: [][]string{{"sh", "-c", `test -f "$PROFILE_READY" && echo ready > generated`}}}}}
	w := New(cfg)
	as := protocol.Assignment{TaskID: "task", RunID: "run", Role: protocol.RoleImplement, Spec: json.RawMessage(`{"deliverables":["report"]}`), Workspace: protocol.AssignmentWorkspace{ID: "ws_retry", Kind: "directory", Profile: "setup"}}
	if _, err := w.Prepare(t.Context(), as); err == nil {
		t.Fatal("failed profile was ignored")
	}
	if err := os.WriteFile(ready, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := New(cfg).Prepare(t.Context(), as)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(p.Dir, "generated")); err != nil || string(b) != "ready\n" {
		t.Fatalf("profile retry did not prepare workspace: %q %v", b, err)
	}
}
