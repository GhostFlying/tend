package codexenv

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/GhostFlying/tend/internal/protocol"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const skillExclusionInterval = 6 * time.Hour

// SkillExclusions is a local inventory, not a persistent directory deny rule.
// Paths added after the last scan remain eligible until a later launch refreshes it.
type SkillExclusions struct {
	LastAttempt time.Time `json:"last_attempt"`
	RefreshedAt time.Time `json:"refreshed_at"`
	Paths       []string  `json:"paths"`
	LastError   string    `json:"last_error,omitempty"`
}

func exclusionDirectory(user string) string {
	return filepath.Join(canonicalPath(user), ".cache", "tend", "codex-skill-exclusions")
}

func readExclusions(user string) (SkillExclusions, error) {
	var out SkillExclusions
	raw, err := os.ReadFile(filepath.Join(exclusionDirectory(user), "inventory.json"))
	if err != nil {
		return out, err
	}
	if len(raw) > 8<<20 {
		return out, fmt.Errorf("skill exclusion cache exceeds size limit")
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("invalid skill exclusion cache: %w", err)
	}
	for _, path := range out.Paths {
		if !filepath.IsAbs(path) {
			return out, fmt.Errorf("invalid skill exclusion path")
		}
	}
	return out, nil
}

// ExclusionSummary reports freshness without publishing local source paths.
func ExclusionSummary(user string) *protocol.ExtensionSkillExclusionStatus {
	cache, err := readExclusions(user)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return &protocol.ExtensionSkillExclusionStatus{LastError: "personal skill exclusion cache is unreadable"}
	}
	out := &protocol.ExtensionSkillExclusionStatus{ExcludedPaths: len(cache.Paths), LastError: cache.LastError}
	if !cache.LastAttempt.IsZero() {
		out.LastAttempt = cache.LastAttempt.Format(time.RFC3339Nano)
		out.NextRefreshAt = cache.LastAttempt.Add(skillExclusionInterval).Format(time.RFC3339Nano)
	}
	if !cache.RefreshedAt.IsZero() {
		out.RefreshedAt = cache.RefreshedAt.Format(time.RFC3339Nano)
	}
	return out
}

func refreshExclusions(ctx context.Context, user string, now time.Time, scan func(context.Context, string) ([]string, error)) (SkillExclusions, error) {
	dir := exclusionDirectory(user)
	unlock, err := lock(ctx, dir)
	if err != nil {
		return SkillExclusions{}, err
	}
	defer unlock()
	out, err := readExclusions(user)
	if err != nil && !os.IsNotExist(err) {
		return out, err
	}
	if !out.LastAttempt.IsZero() && now.Before(out.LastAttempt.Add(skillExclusionInterval)) {
		if out.RefreshedAt.IsZero() {
			return out, fmt.Errorf("personal skill inventory unavailable; next refresh is throttled")
		}
		return out, nil
	}
	// Persist admission before the scan: cancellation or process death must
	// not turn every subsequent agent launch into another full traversal.
	out.LastAttempt = now.UTC()
	out.LastError = "personal skill scan did not complete; last successful exclusion list retained"
	raw, err := json.Marshal(out)
	if err != nil {
		return out, err
	}
	if err := atomicWrite(filepath.Join(dir, "inventory.json"), raw); err != nil {
		return out, err
	}
	paths, scanErr := scan(ctx, filepath.Join(user, ".agents", "skills"))
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	if scanErr == nil {
		slices.Sort(paths)
		out.Paths = slices.Compact(paths)
		out.RefreshedAt = now.UTC()
		out.LastError = ""
	} else {
		// Do not put arbitrary filesystem error text (or skill content) in status.
		out.LastError = "personal skill scan failed; last successful exclusion list retained"
	}
	raw, err = json.Marshal(out)
	if err != nil {
		return out, err
	}
	if err := atomicWrite(filepath.Join(dir, "inventory.json"), raw); err != nil {
		return out, err
	}
	if out.RefreshedAt.IsZero() {
		return out, fmt.Errorf("personal skill inventory unavailable: %w", scanErr)
	}
	return out, nil
}

// scanPersonalSkills finds filenames only. Codex remains the skill parser.
// Directory links are followed; canonical identities prevent link cycles.
func scanPersonalSkills(ctx context.Context, root string) ([]string, error) {
	paths := []string{}
	seen := map[string]bool{}
	remaining := 100000
	var walk func(string) error
	walk = func(path string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		remaining--
		if remaining < 0 {
			return fmt.Errorf("personal skill inventory exceeds entry limit")
		}
		real, err := filepath.EvalSymlinks(path)
		if os.IsNotExist(err) {
			return nil // Missing root and broken installer links are not skills.
		}
		if err != nil {
			return err
		}
		info, err := os.Stat(real)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			if info.Mode().IsRegular() && strings.EqualFold(filepath.Base(path), "SKILL.md") {
				paths = append(paths, real)
			}
			return nil
		}
		if seen[real] {
			return nil
		}
		seen[real] = true
		entries, err := os.ReadDir(real)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			if err := walk(filepath.Join(real, entry.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	err := walk(root)
	return paths, err
}
