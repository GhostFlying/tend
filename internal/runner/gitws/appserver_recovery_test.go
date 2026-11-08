package gitws

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAppServerRecoveryReattachReadsOriginalLocalWorkspace(t *testing.T) {
	for _, scenario := range []string{"original", "legacy missing run state", "changed resource"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFixture(t, nil)
			as := f.implement()
			as.Workspace.ID = "original-resource"
			as.Workspace.Profile = "prepare"
			f.ws.cfg.Profiles = map[string]Profile{"prepare": {Commands: [][]string{{"sh", "-c", "printf prepared >> profile-count"}}, RerunIfChanged: []string{"README"}}}
			prepared := f.prepare(as)
			if scenario != "legacy missing run state" {
				if err := os.MkdirAll(prepared.StateDir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			write(t, filepath.Join(prepared.Dir, "tend", "README"), "changed after original execution")
			state := statePath(f.ws.home(as))
			before, err := os.ReadFile(state)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "changed resource" {
				as.Workspace.ID = "unrelated-resource"
			}
			f.ws.git.bin = filepath.Join(t.TempDir(), "must-not-execute-git")
			got, err := f.ws.ReattachRecovery(t.Context(), as)
			if scenario == "changed resource" {
				if err == nil {
					t.Fatal("changed original resource was accepted")
				}
			} else if err != nil || got.Dir != prepared.Dir || got.StateDir != prepared.StateDir {
				t.Fatalf("original local workspace not located: %+v %v", got, err)
			}
			after, err := os.ReadFile(state)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("recovery mutated workspace state", err)
			}
			profile, err := os.ReadFile(filepath.Join(prepared.Dir, "profile-count"))
			if err != nil || string(profile) != "prepared" {
				t.Fatal("recovery repeated workspace preparation", string(profile), err)
			}
			if scenario == "legacy missing run state" {
				if _, err := os.Stat(prepared.StateDir); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("legacy recovery fabricated missing process evidence", err)
				}
			}
		})
	}
}
