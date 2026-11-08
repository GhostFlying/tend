package codexenv

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// prepareProcessHome isolates configuration and writable plugin state without
// changing HOME or creating a filesystem/user namespace. Runtime history and
// credentials remain in the original fixed state home.
func prepareProcessHome(ctx context.Context, p Profile, shot, work string, exclusions SkillExclusions) error {
	for _, name := range []string{"skills", "plugins"} {
		if err := os.RemoveAll(filepath.Join(work, name)); err != nil {
			return err
		}
		if err := materialize(ctx, filepath.Join(shot, name), filepath.Join(work, name)); err != nil {
			return err
		}
	}
	for _, name := range []string{"AGENTS.md", "AGENTS.override.md", "provenance.json"} {
		raw, err := os.ReadFile(filepath.Join(shot, name))
		if err != nil {
			return err
		}
		if err := atomicWrite(filepath.Join(work, name), raw); err != nil {
			return err
		}
	}
	for _, name := range []string{"sessions", "archived_sessions", "thread-writer-locks"} {
		target := filepath.Join(p.Home, name)
		if err := os.MkdirAll(target, 0700); err != nil {
			return err
		}
		if err := nativeStateLink(target, filepath.Join(work, name)); err != nil {
			return err
		}
	}
	for _, name := range []string{"auth.json", "history.jsonl", "session_index.jsonl"} {
		if err := nativeStateLink(filepath.Join(p.Home, name), filepath.Join(work, name)); err != nil {
			return err
		}
	}
	var config map[string]any
	if _, err := toml.DecodeFile(filepath.Join(shot, "config.toml"), &config); err != nil {
		return err
	}
	config["sqlite_home"] = p.Home
	config["log_dir"] = filepath.Join(p.Home, "logs")
	if markets, ok := config["marketplaces"].(map[string]any); ok {
		for _, value := range markets {
			if market, ok := value.(map[string]any); ok {
				if source, ok := market["source"].(string); ok {
					market["source"] = strings.Replace(source, filepath.Join(p.Home, ".tend-capabilities"), shot, 1)
				}
			}
		}
	}
	skills, _ := config["skills"].(map[string]any)
	if skills == nil {
		skills = map[string]any{}
	}
	rules := []map[string]any{}
	switch existing := skills["config"].(type) {
	case nil:
	case []map[string]any:
		rules = append(rules, existing...)
	case []any:
		for _, value := range existing {
			rule, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("invalid native skill configuration rule")
			}
			rules = append(rules, rule)
		}
	default:
		return fmt.Errorf("invalid native skill configuration rules")
	}
	for _, path := range exclusions.Paths {
		rules = append(rules, map[string]any{"path": path, "enabled": false})
	}
	if len(rules) > 0 {
		skills["config"] = rules
	}
	config["skills"] = skills
	var encoded bytes.Buffer
	if err := toml.NewEncoder(&encoded).Encode(config); err != nil {
		return err
	}
	return atomicWrite(filepath.Join(work, "config.toml"), encoded.Bytes())
}

func nativeStateLink(target, alias string) error {
	if existing, err := os.Readlink(alias); err == nil {
		if existing == target {
			return nil
		}
		return fmt.Errorf("native state alias has a different target")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("native state alias is not the managed link: %w", err)
	}
	return os.Symlink(target, alias)
}
