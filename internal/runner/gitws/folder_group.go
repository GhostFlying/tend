package gitws

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/GhostFlying/tend/internal/harness/orca"
)

type folderGroupState struct {
	Parent string `json:"parent"`
	ID     string `json:"id,omitempty"`
	Asked  bool   `json:"asked,omitempty"`
}

func (w *Workspaces) temporaryGroup(ctx context.Context, c *orca.Client, parent string) (string, error) {
	unlock, err := w.lock(ctx, "orca:folder-groups")
	if err != nil {
		return "", err
	}
	defer unlock()
	path := filepath.Join(w.cfg.Home, "provider-state", "orca-folder-group.json")
	st := folderGroupState{Parent: parent}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &st); err != nil {
			return "", err
		}
		if st.Parent != parent {
			return "", errors.New("gitws: temporary workspace group parent changed")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	groups, err := c.ProjectGroups(ctx)
	if err != nil {
		return "", err
	}
	var found *orca.ProjectGroup
	for i := range groups {
		g := &groups[i]
		if st.ID != "" && g.ID == st.ID {
			if filepath.Clean(g.ParentPath) != parent {
				return "", errors.New("gitws: temporary group identity path changed")
			}
			found = g
			break
		}
		if st.ID == "" && filepath.Clean(g.ParentPath) == parent {
			if found != nil {
				return "", errors.New("gitws: multiple temporary workspace groups")
			}
			found = g
		}
	}
	if found != nil {
		st.ID = found.ID
		st.Asked = false
		if err := saveFolderGroup(path, st); err != nil {
			return "", err
		}
		return st.ID, nil
	}
	if st.ID != "" {
		return "", errors.New("gitws: retained temporary workspace group is missing")
	}
	if st.Asked {
		return "", errors.New("gitws: group creation outcome unknown; reconcile before retry")
	}
	st.Asked = true
	if err := saveFolderGroup(path, st); err != nil {
		return "", err
	}
	group, err := c.CreateGroup(ctx, "tend", parent)
	if err != nil {
		if !orca.Unconfirmed(err) {
			st.Asked = false
			if saveErr := saveFolderGroup(path, st); saveErr != nil {
				return "", errors.Join(err, saveErr)
			}
		}
		return "", err
	}
	if group.ID == "" || filepath.Clean(group.ParentPath) != parent {
		return "", errors.New("gitws: created temporary group identity is invalid")
	}
	st.ID = group.ID
	st.Asked = false
	if err := saveFolderGroup(path, st); err != nil {
		return "", err
	}
	return st.ID, nil
}

func saveFolderGroup(path string, st folderGroupState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path+".tmp", b, 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}
