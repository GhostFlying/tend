package orca

import (
	"context"
	"fmt"
	"strings"
)

// CheckWorkspaceProtocol probes read-only operations before claiming directory work.
func (c *Client) CheckWorkspaceProtocol(ctx context.Context) error {
	if _, err := c.FolderWorkspaces(ctx); err != nil {
		return fmt.Errorf("folder workspace CLI unavailable: %w", err)
	}
	if _, err := c.ProjectGroups(ctx); err != nil {
		return fmt.Errorf("project group CLI unavailable: %w", err)
	}
	return nil
}

func workspaceSelector(id string) string {
	return "id:" + id
}

type FolderWorkspace struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	FolderPath     string `json:"folderPath"`
	ProjectGroupID string `json:"projectGroupId"`
}

type ProjectGroup struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	ParentPath string `json:"parentPath"`
}

func (c *Client) FolderWorkspaces(ctx context.Context) ([]FolderWorkspace, error) {
	var out struct {
		Folders []FolderWorkspace `json:"folderWorkspaces"`
	}
	err := c.call(ctx, "", &out, "folder-workspace", "list")
	return out.Folders, err
}

func (c *Client) CreateFolder(ctx context.Context, group, path, name string) (FolderWorkspace, error) {
	var out struct {
		Folder FolderWorkspace `json:"folderWorkspace"`
	}
	err := c.call(ctx, "", &out, "folder-workspace", "create", "--group", group, "--path", path, "--name", name)
	return out.Folder, err
}

func (c *Client) RenameWorkspace(ctx context.Context, id, name, comment string) error {
	if strings.HasPrefix(id, "folder:") {
		return c.call(ctx, "", nil, "folder-workspace", "update", "--workspace", strings.TrimPrefix(id, "folder:"), "--name", name, "--comment", comment)
	}
	return c.call(ctx, "", nil, "worktree", "set", "--worktree", workspaceSelector(id), "--display-name", name, "--comment", comment)
}

func (c *Client) RemoveFolder(ctx context.Context, id string) error {
	return c.call(ctx, "", nil, "folder-workspace", "rm", "--workspace", strings.TrimPrefix(id, "folder:"))
}

func (c *Client) ProjectGroups(ctx context.Context) ([]ProjectGroup, error) {
	var out struct {
		Groups []ProjectGroup `json:"groups"`
	}
	err := c.call(ctx, "", &out, "project-group", "list")
	return out.Groups, err
}

func (c *Client) CreateGroup(ctx context.Context, name, path string) (ProjectGroup, error) {
	var out struct {
		Group ProjectGroup `json:"group"`
	}
	err := c.call(ctx, "", &out, "project-group", "create", "--name", name, "--path", path)
	return out.Group, err
}
