package gitws

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/GhostFlying/tend/internal/harness/orca"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner"
)

func workspaceTitle(as protocol.Assignment) string {
	title := strings.TrimSpace(as.Workspace.TaskTitle)
	if title == "" {
		title = as.Workspace.Name
	}
	id := as.Workspace.ShortTaskID
	if id == "" {
		id = as.TaskID
		if len(id) > 8 {
			id = id[len(id)-8:]
		}
	}

	return title + " · " + id
}

func (w *Workspaces) workspaceComment(as protocol.Assignment) string {
	comment := fmt.Sprintf("%s\ntend task %s\nworkspace %s\nkind %s\nrole %s", resourceMarker(as), as.TaskID, as.Workspace.ID, as.Workspace.Kind, as.Role)
	if u, err := url.Parse(w.cfg.ServerURL); err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https") {
		u.User = nil
		u.Fragment = ""
		q := u.Query()
		q.Set("task", as.TaskID)
		u.RawQuery = q.Encode()
		comment += "\n" + u.String()
	}
	return comment
}

func (w *Workspaces) prepareDirectory(ctx context.Context, as protocol.Assignment, home string) (runner.Prepared, error) {
	if as.Workspace.ID == "" || filepath.Base(as.Workspace.ID) != as.Workspace.ID || as.Workspace.ID == "." {
		return runner.Prepared{}, errors.New("gitws: invalid workspace ID")
	}
	unlock, err := w.lock(ctx, "ws:"+home)
	if err != nil {
		return runner.Prepared{}, err
	}
	defer unlock()
	if as.Workspace.Path != "" && filepath.Clean(as.Workspace.Path) != home {
		return runner.Prepared{}, errors.New("gitws: directory resource path changed")
	}
	if as.Workspace.Reused || as.Role == protocol.RoleAccept {
		if _, err := os.Stat(statePath(home)); err != nil {
			return runner.Prepared{}, fmt.Errorf("gitws: retained workspace missing: %w", err)
		}
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return runner.Prepared{}, err
	}
	st, err := loadState(home)
	if err != nil {
		return runner.Prepared{}, err
	}
	if st.ResourceID != "" && st.ResourceID != as.Workspace.ID {
		return runner.Prepared{}, errors.New("gitws: directory resource identity changed")
	}
	if st.BackendID != "" && st.BackendID != as.Workspace.BackendID {
		return runner.Prepared{}, errors.New("directory workspace backend differs from recorded origin")
	}
	if as.Workspace.BackendID != "" {
		st.BackendID = as.Workspace.BackendID
		if err := saveState(home, st); err != nil {
			return runner.Prepared{}, err
		}
	}
	st.ResourceID = as.Workspace.ID
	if l, ok := w.layout.(orcaLayout); ok {
		if err := w.ensureFolder(ctx, as, home, &st, l.orca); err != nil {
			return runner.Prepared{}, err
		}
	} else if as.Workspace.ProviderID != "" {
		return runner.Prepared{}, errors.New("gitws: provider-backed directory requires its original harness")
	}
	if err := w.runProfiles(ctx, as, home, home, &st); err != nil {
		return runner.Prepared{}, err
	}
	if err := saveState(home, st); err != nil {
		return runner.Prepared{}, err
	}
	baseline, err := directoryBaseline(ctx, home, as.TaskID)
	if err != nil {
		return runner.Prepared{}, err
	}
	stateDir := filepath.Join(w.cfg.Home, "runs", as.RunID)
	scratch := filepath.Join(w.cfg.Home, "scratch", as.Workspace.ID)
	if as.Role == protocol.RoleAccept {
		scratch = filepath.Join(w.cfg.Home, "scratch", ".accept", as.RunID)
	}
	scratch, err = prepareScratch(scratch)
	if err != nil {
		return runner.Prepared{}, err
	}
	return runner.Prepared{Dir: home, StateDir: stateDir, ScratchDir: scratch, ProviderID: st.ProviderID, Guide: "This is a retained ordinary directory. It need not contain a Git repository. Preserve inherited files and perform only the task's authorized work.", Report: protocol.WorkspaceReport{BackendID: as.Workspace.BackendID, Path: home, ProviderID: st.ProviderID, ProviderInstanceID: st.ProviderInstanceID, Baseline: baselineReport(baseline, "directory", as.TaskID)}}, nil
}

func (w *Workspaces) ensureFolder(ctx context.Context, as protocol.Assignment, home string, st *state, c *orca.Client) error {
	folders, err := c.FolderWorkspaces(ctx)
	if err != nil {
		return err
	}
	var found *orca.FolderWorkspace
	for i := range folders {
		f := &folders[i]
		if st.ProviderID != "" && "folder:"+f.ID == st.ProviderID {
			found = f
			break
		}
		if st.ProviderID == "" && st.FolderAsked && filepath.Clean(f.FolderPath) == home {
			if found != nil {
				return errors.New("gitws: multiple folder registrations for pending workspace")
			}
			found = f
		}
	}
	if found != nil {
		if filepath.Clean(found.FolderPath) != home {
			return errors.New("gitws: folder resource path changed")
		}
		st.ProviderID = "folder:" + found.ID
	} else if st.ProviderID != "" || as.Workspace.ProviderID != "" || as.Workspace.Reused || as.Role == protocol.RoleAccept {
		return errors.New("gitws: retained Orca folder registration is missing")
	} else {
		if st.FolderAsked {
			return errors.New("gitws: folder creation outcome unknown; reconcile before retry")
		}
		for _, f := range folders {
			if filepath.Clean(f.FolderPath) == home {
				return errors.New("gitws: folder path already registered outside this creation")
			}
		}
		group, err := w.temporaryGroup(ctx, c, filepath.Dir(home))
		if err != nil {
			return err
		}
		st.FolderAsked = true
		if err := saveState(home, *st); err != nil {
			return err
		}
		f, err := c.CreateFolder(ctx, group, home, workspaceTitle(as))
		if err != nil {
			if !orca.Unconfirmed(err) {
				st.FolderAsked = false
				_ = saveState(home, *st)
			}
			return err
		}
		if f.ID == "" || filepath.Clean(f.FolderPath) != home {
			return errors.New("gitws: created folder identity or path is invalid")
		}
		st.ProviderID = "folder:" + f.ID
	}
	if as.Workspace.ProviderID != "" && as.Workspace.ProviderID != st.ProviderID {
		return errors.New("gitws: folder provider identity changed")
	}
	comment := w.workspaceComment(as)
	if st.DisplayTask != as.TaskID || st.DisplayComment != comment {
		if err := c.RenameWorkspace(ctx, st.ProviderID, workspaceTitle(as), comment); err != nil {
			return err
		}
		st.DisplayTask = as.TaskID
		st.DisplayComment = comment
	}
	return nil
}
