package gitws

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRetainedWorkspaceReattachNeverPreparesOrRecreates(t *testing.T) {
	for _, kind := range []string{"existing", "missing-state", "missing-checkout", "changed-resource", "changed-provider", "missing-scratch"} {
		t.Run(kind, func(t *testing.T) {
			f, _ := newOrcaFixture(t)
			as := f.implement()
			as.Workspace.ID = "original-resource"
			as.Workspace.Profile = "prepare"
			f.ws.cfg.Profiles = map[string]Profile{"prepare": {Commands: [][]string{{"sh", "-c", "printf prepared >> profile-count"}}, RerunIfChanged: []string{"README"}}}
			p := f.prepare(as)
			if err := os.MkdirAll(p.StateDir, 0o700); err != nil {
				t.Fatal(err)
			}
			// A changed profile input would cause ordinary Prepare to run commands.
			write(t, filepath.Join(p.Dir, "README"), "changed by original worker")
			st, err := loadState(f.ws.home(as))
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "missing-state":
				err = os.Remove(statePath(f.ws.home(as)))
			case "missing-checkout":
				err = os.Rename(p.Dir, p.Dir+"-retained")
			case "changed-resource":
				st.ResourceID = "another-resource"
				err = saveState(f.ws.home(as), st)
			case "changed-provider":
				as.Workspace.ProviderID = "another-provider"
			case "missing-scratch":
				err = os.Remove(p.ScratchDir)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, beforeErr := os.ReadFile(statePath(f.ws.home(as)))
			f.ws.git.bin = filepath.Join(t.TempDir(), "forbidden-git")
			layout := f.ws.layout.(orcaLayout)
			layout.orca.Exec = func(context.Context, string, []string) ([]byte, error) {
				t.Error("strict workspace attachment called the provider")
				return nil, errors.New("provider call is forbidden")
			}
			got, err := f.ws.Reattach(t.Context(), as)
			if kind == "existing" {
				if err != nil || got.Dir != p.Dir || got.ProviderID != p.ProviderID || got.StateDir != p.StateDir || got.ScratchDir != p.ScratchDir {
					t.Fatal("existing original workspace did not reattach", got, err)
				}
			} else if err == nil {
				t.Fatal("missing or changed original workspace was repaired", kind, got)
			}
			after, afterErr := os.ReadFile(statePath(f.ws.home(as)))
			if !bytes.Equal(before, after) || errors.Is(beforeErr, os.ErrNotExist) != errors.Is(afterErr, os.ErrNotExist) {
				t.Fatal("strict attachment rewrote original state")
			}
			root := p.Dir
			if kind == "missing-checkout" {
				root += "-retained"
				if _, err := os.Stat(p.Dir); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("strict attachment recreated missing checkout", err)
				}
			}
			data, err := os.ReadFile(filepath.Join(root, "profile-count"))
			if err != nil || string(data) != "prepared" {
				t.Fatal("strict attachment reexecuted preparation profile", string(data), err)
			}
		})
	}
}
