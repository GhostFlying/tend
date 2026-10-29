package gitws

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

// Cleanup removes only a reserved, locally owned directory resource.
func (w *Workspaces) Cleanup(ctx context.Context, p protocol.WorkspaceCleanup) error {
	if p.BackendID != "" && p.BackendID != w.backendID {
		view, err := w.Backend(p.BackendID)
		if err != nil {
			return err
		}
		return view.Cleanup(ctx, p)
	}
	if p.Kind != "directory" {
		return fmt.Errorf("gitws: cleanup of %s is not supported", p.Kind)
	}
	if p.WorkspaceID == "" || filepath.Base(p.WorkspaceID) != p.WorkspaceID || p.WorkspaceID == "." {
		return errors.New("gitws: invalid cleanup resource ID")
	}
	home := filepath.Join(w.cfg.Home, "temporary-workspaces", p.WorkspaceID)
	if p.Path != home {
		return errors.New("gitws: cleanup path does not match owned resource")
	}
	unlock, err := w.lock(ctx, "ws:"+home)
	if err != nil {
		return err
	}
	defer unlock()
	if info, err := os.Lstat(home); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("gitws: cleanup root is a symbolic link")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	st, err := loadState(home)
	if err != nil {
		return err
	}
	_, statErr := os.Stat(statePath(home))
	if statErr == nil {
		if st.BackendID != "" && st.BackendID != p.BackendID {
			return errors.New("gitws: cleanup backend differs from recorded origin")
		}
		if st.ResourceID != p.WorkspaceID || st.ProviderID != p.ProviderID || st.ProviderInstanceID != p.ProviderInstanceID {
			return errors.New("gitws: cleanup identity mismatch")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	provider, hasProvider := w.layout.(orcaLayout)
	if p.ProviderID != "" && !hasProvider {
		return errors.New("gitws: cleanup requires the resource original provider")
	}
	if l := provider; hasProvider {
		if p.ProviderID != "" && !strings.HasPrefix(p.ProviderID, "folder:") {
			return errors.New("gitws: directory has non-folder provider identity")
		}
		folders, err := l.orca.FolderWorkspaces(ctx)
		if err != nil {
			return err
		}
		found := false
		for _, f := range folders {
			if filepath.Clean(f.FolderPath) == home && "folder:"+f.ID != p.ProviderID {
				return errors.New("gitws: cleanup path belongs to another provider identity")
			}
			if "folder:"+f.ID == p.ProviderID {
				if filepath.Clean(f.FolderPath) != home {
					return errors.New("gitws: cleanup provider path changed")
				}
				found = true
			}
		}
		if found {
			if statErr != nil {
				return errors.New("gitws: provider remains but local ownership proof is missing")
			}
			terminals, err := l.orca.Terminals(ctx, "id:"+p.ProviderID)
			if err != nil {
				return err
			}
			if len(terminals) > 0 {
				return errors.New("gitws: workspace still has live terminals")
			}
			if err := l.orca.RemoveFolder(ctx, p.ProviderID); err != nil {
				return err
			}
			remaining, err := l.orca.FolderWorkspaces(ctx)
			if err != nil {
				return err
			}
			for _, f := range remaining {
				if "folder:"+f.ID == p.ProviderID || filepath.Clean(f.FolderPath) == home {
					return errors.New("gitws: folder registration remains after cleanup")
				}
			}
		}
	}
	if statErr == nil {
		if err := os.RemoveAll(home); err != nil {
			return err
		}
	} else if _, err := os.Stat(home); err == nil {
		return errors.New("gitws: refusing to remove unowned directory")
	}
	if _, err := os.Stat(home); !errors.Is(err, os.ErrNotExist) {
		return errors.New("gitws: workspace remains after cleanup")
	}
	return nil
}
