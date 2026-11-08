package extensions

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

// CheckPackageConflicts checks package ownership and MCP names without parsing
// skill instructions. Native Codex still validates skill names and manifests.
func CheckPackageConflicts(ctx context.Context, combination protocol.ExtensionCombination, source ContentSource) error {
	mcpOwners := map[string]string{}
	pluginNames := map[string]string{}
	standalone := map[string]string{}
	bundled := map[string]string{}
	claim := func(name, owner string) error {
		if previous, ok := mcpOwners[name]; ok && previous != owner {
			return fmt.Errorf("managed MCP name %q conflicts between %s and %s", name, previous, owner)
		}
		mcpOwners[name] = owner
		return nil
	}
	for _, selected := range combination.Resources {
		r := selected.Resource
		owner := key(r.Kind, r.ID)
		if r.Kind == "mcp" {
			if err := claim(r.ID, owner); err != nil {
				return err
			}
			continue
		}
		f, err := source(ctx, r)
		if err != nil {
			return err
		}
		raw, err := io.ReadAll(io.LimitReader(f, MaxArchiveBytes+1))
		_ = f.Close()
		if err != nil {
			return err
		}
		if err = VerifyContent(r, raw); err != nil {
			return err
		}
		entries, err := ReadPackage(ctx, raw)
		if err != nil {
			return err
		}
		files := map[string][]byte{}
		for _, entry := range entries {
			files[entry.Name] = entry.Data
		}
		if r.Kind == "skill" {
			standalone[Hash(files["SKILL.md"])] = owner
			continue
		}
		var manifest struct {
			Name string          `json:"name"`
			MCP  json.RawMessage `json:"mcpServers"`
		}
		if err = json.Unmarshal(files[".codex-plugin/plugin.json"], &manifest); err != nil {
			return err
		}
		if previous, ok := pluginNames[manifest.Name]; ok && previous != owner {
			return fmt.Errorf("managed plugins share native name %q", manifest.Name)
		}
		pluginNames[manifest.Name] = owner
		for _, entry := range entries {
			if path.Base(entry.Name) == "SKILL.md" {
				bundled[Hash(entry.Data)] = owner
			}
		}
		paths := []string{}
		if _, ok := files[".mcp.json"]; ok {
			paths = append(paths, ".mcp.json")
		}
		if len(manifest.MCP) > 0 {
			var file string
			var names map[string]json.RawMessage
			var list []string
			switch {
			case json.Unmarshal(manifest.MCP, &file) == nil:
				paths = append(paths, strings.TrimPrefix(file, "./"))
			case json.Unmarshal(manifest.MCP, &list) == nil:
				for _, file := range list {
					paths = append(paths, strings.TrimPrefix(file, "./"))
				}
			case json.Unmarshal(manifest.MCP, &names) == nil:
				for name := range names {
					if err = claim(name, owner); err != nil {
						return err
					}
				}
			default:
				return fmt.Errorf("invalid managed plugin MCP declaration")
			}
		}
		for _, file := range paths {
			var config struct {
				Servers map[string]json.RawMessage `json:"mcpServers"`
			}
			body, ok := files[file]
			if !ok {
				return fmt.Errorf("plugin MCP definition is outside the complete package")
			}
			if err = json.Unmarshal(body, &config); err != nil {
				return fmt.Errorf("invalid plugin MCP JSON")
			}
			for name := range config.Servers {
				if err = claim(name, owner); err != nil {
					return err
				}
			}
		}
	}
	for digest, owner := range standalone {
		if plugin, ok := bundled[digest]; ok {
			return fmt.Errorf("%s is already provided by %s; keep the plugin-owned skill", owner, plugin)
		}
	}
	return nil
}
