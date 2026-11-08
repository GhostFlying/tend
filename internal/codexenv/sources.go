package codexenv

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// sourceStamp reads selected inputs without creating a staging tree. Links are
// resolved with the same cycle and file-type rules as package materialization.
func sourceStamp(ctx context.Context, p Profile) (string, error) {
	inputs := []string{}
	for _, selected := range []struct {
		kind string
		sel  Selection
	}{{"skill", p.Skills}, {"plugin", p.Plugins}} {
		for _, item := range append(append([]Item{}, selected.sel.Allow...), selected.sel.Installed...) {
			source := item.Path
			if source == "" {
				if selected.kind == "skill" {
					if !safeSegment(item.ID) {
						return "", fmt.Errorf("invalid skill source identity")
					}
					source = filepath.Join(p.SourceSkills, item.ID)
				} else {
					var err error
					source, err = cacheSource(p.SourceHome, item.ID)
					if err != nil {
						return "", err
					}
				}
			}
			sha, err := sourceDigest(ctx, source, map[string]bool{})
			if err != nil {
				return "", err
			}
			inputs = append(inputs, source, sha)
		}
	}
	agents := filepath.Join(p.Home, "AGENTS.md")
	if p.Agents.Mode == "sync" {
		agents = p.Agents.Source
	}
	for _, file := range []string{p.BaseConfig, agents} {
		var raw []byte
		var err error
		if file != "" {
			raw, err = os.ReadFile(file)
			if err != nil && !(os.IsNotExist(err) && file == agents && p.Agents.Mode == "independent") {
				return "", err
			}
		}
		inputs = append(inputs, digest(raw))
	}
	raw, err := json.Marshal(struct {
		Profile Profile
		Inputs  []string
	}{p, inputs})
	return digest(raw), err
}

func sourceDigest(ctx context.Context, source string, stack map[string]bool) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(source)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("package contains a non-regular file")
		}
		raw, err := os.ReadFile(real)
		return digest(append([]byte(fmt.Sprintf("%o:", info.Mode().Perm()&0111)), raw...)), err
	}
	if stack[real] {
		return "", fmt.Errorf("package contains a symlink cycle")
	}
	stack[real] = true
	defer delete(stack, real)
	entries, err := os.ReadDir(real)
	if err != nil {
		return "", err
	}
	children := [][2]string{}
	for _, entry := range entries {
		if entry.Name() == ".git" {
			continue
		}
		sha, err := sourceDigest(ctx, filepath.Join(real, entry.Name()), stack)
		if err != nil {
			return "", err
		}
		children = append(children, [2]string{entry.Name(), sha})
	}
	raw, err := json.Marshal(children)
	return digest(raw), err
}
