package codexenv

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

func (s *Service) prepare(ctx context.Context, p Profile, revision uint64) (Snapshot, string, error) {
	var out Snapshot
	dir, err := s.directory(p.ID)
	if err != nil {
		return out, "", err
	}
	if err = os.MkdirAll(filepath.Join(dir, "staging"), 0700); err != nil {
		return out, "", err
	}
	stage, err := os.MkdirTemp(filepath.Join(dir, "staging"), "snapshot-")
	if err != nil {
		return out, "", err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(stage)
		}
	}()
	for _, path := range []string{"skills", "legacy-skills", "plugins/cache", "marketplaces", "empty"} {
		if err = os.MkdirAll(filepath.Join(stage, path), 0700); err != nil {
			return out, "", err
		}
	}
	config := map[string]any{}
	if p.BaseConfig != "" {
		if _, err = toml.DecodeFile(p.BaseConfig, &config); err != nil {
			return out, "", fmt.Errorf("independent base configuration is unavailable or invalid")
		}
	}
	// Writable state never follows a source-home override.
	config["sqlite_home"] = p.Home
	config["log_dir"] = filepath.Join(p.Home, "logs")
	config["cli_auth_credentials_store"] = "file"
	skillsConfig, _ := config["skills"].(map[string]any)
	if skillsConfig == nil {
		skillsConfig = map[string]any{}
	}
	skillsConfig["bundled"] = map[string]any{"enabled": p.BundledSkills == nil || *p.BundledSkills}
	config["skills"] = skillsConfig
	pluginConfig := map[string]any{"_default": map[string]any{"enabled": false}}
	basePlugins, _ := config["plugins"].(map[string]any)
	marketConfig := map[string]any{}
	for _, kind := range []string{"skill", "plugin"} {
		selection := p.Skills
		if kind == "plugin" {
			selection = p.Plugins
		}
		for _, group := range []struct {
			items      []Item
			projection bool
		}{{selection.Allow, true}, {selection.Installed, false}} {
			for _, item := range group.items {
				source := item.Path
				if source == "" {
					if kind == "skill" {
						if !safeSegment(item.ID) {
							return out, "", fmt.Errorf("skill identity must select a single source directory")
						}
						source = filepath.Join(p.SourceSkills, item.ID)
					} else {
						source, err = cacheSource(p.SourceHome, item.ID)
						if err != nil {
							return out, "", err
						}
					}
				}
				real, err := filepath.EvalSymlinks(source)
				if err != nil {
					return out, "", fmt.Errorf("selected package is missing: %w", err)
				}
				key := digest([]byte(item.ID))[:16]
				dest := filepath.Join(stage, "skills", key)
				if kind == "plugin" {
					dest = filepath.Join(stage, "marketplaces", "tend-"+key, "plugins", "package")
				}
				if err = materialize(ctx, real, dest); err != nil {
					return out, "", err
				}
				first, err := treeDigest(ctx, dest)
				if err != nil {
					return out, "", err
				}
				verify := filepath.Join(stage, "verify")
				if err = materialize(ctx, real, verify); err != nil {
					return out, "", err
				}
				second, err := treeDigest(ctx, verify)
				if err != nil {
					return out, "", err
				}
				if err = os.RemoveAll(verify); err != nil {
					return out, "", err
				}
				if first != second {
					return out, "", fmt.Errorf("selected package changed while copying")
				}
				pkg := Package{Kind: kind, ID: item.ID, LocalID: item.ID, Source: real, SHA256: first, Projection: group.projection}
				if !group.projection {
					pkg.Source = "local-install:" + item.ID
				}
				if kind == "skill" {
					raw, err := os.ReadFile(filepath.Join(dest, "SKILL.md"))
					if err != nil {
						return out, "", fmt.Errorf("skill requires SKILL.md: %w", err)
					}
					if !bytes.HasPrefix(raw, []byte("---")) || !bytes.Contains(raw, []byte("name:")) || !bytes.Contains(raw, []byte("description:")) {
						return out, "", fmt.Errorf("skill frontmatter is incomplete")
					}
				} else {
					raw, err := os.ReadFile(filepath.Join(dest, ".codex-plugin", "plugin.json"))
					if err != nil {
						return out, "", fmt.Errorf("plugin manifest missing: %w", err)
					}
					var manifest struct {
						Name    string `json:"name"`
						Version string `json:"version"`
					}
					if err = json.Unmarshal(raw, &manifest); err != nil {
						return out, "", fmt.Errorf("plugin manifest invalid: %w", err)
					}
					if !safeSegment(manifest.Name) {
						return out, "", fmt.Errorf("plugin manifest name is invalid")
					}
					if manifest.Version == "" {
						manifest.Version = "local"
					}
					if !safeSegment(manifest.Version) {
						return out, "", fmt.Errorf("plugin version is invalid")
					}
					name, _, _ := strings.Cut(item.ID, "@")
					if manifest.Name != name {
						return out, "", fmt.Errorf("plugin identity differs from manifest")
					}
					market := "tend-" + key
					pkg.LocalID = manifest.Name + "@" + market
					pkg.Version = manifest.Version
					cache := filepath.Join(stage, "plugins", "cache", market, manifest.Name, manifest.Version)
					if err = materialize(ctx, dest, cache); err != nil {
						return out, "", err
					}
					marketplace := map[string]any{"name": market, "plugins": []any{map[string]any{"name": manifest.Name, "source": map[string]any{"source": "local", "path": "./plugins/package"}}}}
					if err = writeJSON(filepath.Join(stage, "marketplaces", market, ".agents", "plugins", "marketplace.json"), marketplace); err != nil {
						return out, "", err
					}
					policy := map[string]any{"enabled": true}
					ownPolicy, _ := basePlugins[pkg.LocalID].(map[string]any)
					if ownPolicy == nil {
						ownPolicy, _ = basePlugins[pkg.ID].(map[string]any)
					}
					if mcp, ok := ownPolicy["mcp_servers"]; ok {
						policy["mcp_servers"] = mcp
					}
					pluginConfig[pkg.LocalID] = policy
					marketConfig[market] = map[string]any{"source_type": "local", "source": filepath.Join(p.Home, ".tend-capabilities", "marketplaces", market)}
				}
				out.Packages = append(out.Packages, pkg)
			}
		}
	}
	config["plugins"] = pluginConfig
	config["marketplaces"] = marketConfig
	var encoded bytes.Buffer
	if err = toml.NewEncoder(&encoded).Encode(config); err != nil {
		return out, "", err
	}
	if err = os.WriteFile(filepath.Join(stage, "config.toml"), encoded.Bytes(), 0600); err != nil {
		return out, "", err
	}
	agents := filepath.Join(p.Home, "AGENTS.md")
	if p.Agents.Mode == "sync" {
		agents = p.Agents.Source
	}
	raw, err := os.ReadFile(agents)
	if err != nil && !(os.IsNotExist(err) && p.Agents.Mode == "independent") {
		return out, "", fmt.Errorf("read AGENTS source: %w", err)
	}
	if err = os.WriteFile(filepath.Join(stage, "AGENTS.md"), raw, 0600); err != nil {
		return out, "", err
	}
	if err = os.WriteFile(filepath.Join(stage, "AGENTS.override.md"), nil, 0600); err != nil {
		return out, "", err
	}
	if err = writeJSON(filepath.Join(stage, "provenance.json"), out.Packages); err != nil {
		return out, "", err
	}
	out.ID, err = treeDigest(ctx, stage)
	if err != nil {
		return out, "", err
	}
	out.ConfigRevision = revision
	out.CreatedAt = s.now()
	ok = true
	return out, stage, nil
}

func (s *Service) publish(ctx context.Context, r *record) (*Snapshot, error) {
	stamp, err := sourceStamp(ctx, r.Status.Profile)
	if err != nil {
		return nil, err
	}
	shot, stage, err := s.prepare(ctx, r.Status.Profile, r.Status.Revision+1)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	after, err := sourceStamp(ctx, r.Status.Profile)
	if err != nil {
		return nil, err
	}
	if after != stamp {
		return nil, fmt.Errorf("selected environment sources changed during staging")
	}
	if shot.ID == r.Status.Latest {
		r.Status.SourceStamp = stamp
		r.Status.LastError = ""
		return &shot, nil
	}
	if s.Validate != nil {
		err = s.Validate(ctx, r.Status.Profile, stage)
	} else {
		err = s.validateNative(ctx, r.Status.Profile, stage)
	}
	if err != nil {
		return nil, fmt.Errorf("snapshot validation: %w", err)
	}
	dir, _ := s.directory(r.Status.Profile.ID)
	dest := filepath.Join(dir, "generations", shot.ID)
	if err = os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
		return nil, err
	}
	if _, err = os.Stat(dest); os.IsNotExist(err) {
		if err = writeJSON(filepath.Join(stage, "snapshot.json"), shot); err != nil {
			return nil, err
		}
		if err = syncTree(ctx, stage); err != nil {
			return nil, err
		}
		if err = os.Rename(stage, dest); err != nil {
			return nil, err
		}
		if err = syncParents(dest, s.Root); err != nil {
			return nil, err
		}
	}
	found := false
	for _, old := range r.Status.Snapshots {
		found = found || old.ID == shot.ID
	}
	if !found {
		r.Status.Snapshots = append(r.Status.Snapshots, shot)
	}
	r.Status.Latest = shot.ID
	r.Status.SourceStamp = stamp
	r.Status.LastError = ""
	return &shot, nil
}
