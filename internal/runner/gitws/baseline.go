package gitws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

type baselineMount struct {
	Path         string               `json:"path"`
	Head         string               `json:"head"`
	TaskBase     string               `json:"task_base,omitempty"`
	Tree         string               `json:"tree"`
	Index        string               `json:"index_tree"`
	IndexEntries []baselineIndexEntry `json:"index_entries,omitempty"`
	Status       string               `json:"status"`
	Staged       string               `json:"staged_diff"`
	Unstaged     string               `json:"unstaged_diff"`
}

// baselineIndexEntry preserves the real index when unmerged stages cannot form a tree.
type baselineIndexEntry struct {
	Path     string `json:"path"`
	Mode     string `json:"mode"`
	ObjectID string `json:"object_id"`
	Stage    int    `json:"stage"`
}

func (w *Workspaces) baselineIndex(ctx context.Context, dir string) (string, []baselineIndexEntry, error) {
	raw, err := w.git.run(ctx, dir, "ls-files", "--stage", "-z")
	if err != nil {
		return "", nil, err
	}
	var entries []baselineIndexEntry
	unmerged := false
	for _, record := range strings.Split(raw, "\x00") {
		if record == "" {
			continue
		}
		metadata, path, ok := strings.Cut(record, "\t")
		fields := strings.Fields(metadata)
		if !ok || len(fields) != 3 {
			return "", nil, fmt.Errorf("gitws: malformed index entry")
		}
		stage, err := strconv.Atoi(fields[2])
		if err != nil || stage < 0 || stage > 3 {
			return "", nil, fmt.Errorf("gitws: invalid index stage %q", fields[2])
		}
		entries = append(entries, baselineIndexEntry{Path: path, Mode: fields[0], ObjectID: fields[1], Stage: stage})
		unmerged = unmerged || stage != 0
	}
	if unmerged {
		return "", entries, nil
	}
	tree, err := w.git.run(ctx, dir, "write-tree")
	return tree, nil, err
}

func (w *Workspaces) baseline(ctx context.Context, as protocol.Assignment, home string, st *state) (json.RawMessage, error) {
	var spec struct {
		Delivery     string   `json:"delivery"`
		Deliverables []string `json:"deliverables"`
		Workspace    struct {
			Include bool `json:"include_existing_changes"`
		} `json:"workspace"`
	}
	if len(as.Spec) > 0 {
		if err := json.Unmarshal(as.Spec, &spec); err != nil {
			return nil, err
		}
	}
	commit := len(spec.Deliverables) == 0
	for _, d := range spec.Deliverables {
		commit = commit || d == "commit"
	}
	path := filepath.Join(home, ".tend", "baselines", as.TaskID+".json")
	if b, err := os.ReadFile(path); err == nil {
		var mounts []baselineMount
		if err := json.Unmarshal(b, &mounts); err != nil {
			return nil, err
		}
		for _, m := range mounts {
			if as.Workspace.Reused && commit && m.Status != "" && !spec.Workspace.Include {
				return nil, w.inheritedChanges(ctx, as, home, *st)
			}
		}
		for _, m := range mounts {
			ms := st.Mounts[m.Path]
			if ms.BaselineTask != as.TaskID {
				if as.Workspace.Reused {
					ms.Head, ms.Tree = m.Head, m.Tree
				}
				ms.BaselineTask = as.TaskID
			}
			ms.TaskBase = m.TaskBase
			if ms.TaskBase == "" {
				ms.TaskBase = m.Head
			}
			st.Mounts[m.Path] = ms
		}
		return b, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	var mounts []baselineMount
	for _, m := range as.Workspace.Mounts {
		ms := st.Mounts[m.Path]
		dir := ms.dir(home, m.Path)
		now, err := w.read(ctx, home, dir)
		if err != nil {
			return nil, err
		}
		status, err := w.git.with("GIT_OPTIONAL_LOCKS=0").run(ctx, dir, "status", "--porcelain=v1", "--untracked-files=all")
		if err != nil {
			return nil, err
		}
		if as.Workspace.Reused && commit && status != "" && !spec.Workspace.Include {
			return nil, w.inheritedChanges(ctx, as, home, *st)
		}
		index, entries, err := w.baselineIndex(ctx, dir)
		if err != nil {
			return nil, err
		}
		staged, err := w.git.run(ctx, dir, "diff", "--cached", "--binary", "HEAD")
		if err != nil {
			return nil, err
		}
		unstaged, err := w.git.run(ctx, dir, "diff", "--binary")
		if err != nil {
			return nil, err
		}
		// Keep inherited untracked contents reachable even if later removed.
		if _, err := w.git.run(ctx, dir, "update-ref", "refs/tend/baselines/"+as.TaskID, now.tree); err != nil {
			return nil, err
		}
		taskStart := now.head
		if !as.Workspace.Reused {
			taskStart = ms.Base
		}
		mounts = append(mounts, baselineMount{Path: m.Path, Head: now.head, TaskBase: taskStart, Tree: now.tree, Index: index, IndexEntries: entries, Status: status, Staged: staged, Unstaged: unstaged})
		if as.Workspace.Reused {
			ms.Head, ms.Tree = now.head, now.tree
		}
		ms.BaselineTask, ms.TaskBase = as.TaskID, taskStart
		st.Mounts[m.Path] = ms
	}
	b, err := json.Marshal(mounts)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	tmp := path + ".tmp"
	defer os.Remove(tmp)
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, err
	}
	return b, nil
}
