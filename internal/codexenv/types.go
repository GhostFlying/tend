// Package codexenv manages local Codex capability snapshots independently of
// the business ledger and of account-scoped plugin installation.
package codexenv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"time"
)

var (
	ErrConflict    = errors.New("codex environment revision or request conflict")
	ErrNotFound    = errors.New("codex environment not found")
	ErrUnsupported = errors.New("strict Codex environments require Linux and bubblewrap")
	validID        = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,79}$`)
)

// Item identifies a complete local package. Projection paths may be omitted:
// skills then resolve below SourceSkills and plugins below SourceHome's cache.
type Item struct {
	ID   string `json:"id"`
	Path string `json:"path,omitempty"`
}
type Selection struct {
	Mode      string `json:"mode"`
	Allow     []Item `json:"allow,omitempty"`
	Installed []Item `json:"installed,omitempty"`
}
type Agents struct {
	Mode   string `json:"mode"`
	Source string `json:"source,omitempty"`
}
type Profile struct {
	Format          int       `json:"format_version"`
	ID              string    `json:"id"`
	Home            string    `json:"home"`
	UserHome        string    `json:"user_home"`
	SourceHome      string    `json:"source_home,omitempty"`
	SourceSkills    string    `json:"source_skills,omitempty"`
	AuthSource      string    `json:"auth_source,omitempty"`
	BaseConfig      string    `json:"base_config,omitempty"`
	Skills          Selection `json:"skills"`
	Plugins         Selection `json:"plugins"`
	Agents          Agents    `json:"agents"`
	BundledSkills   *bool     `json:"bundled_skills,omitempty"`
	AutoSync        *bool     `json:"auto_sync,omitempty"`
	IntervalSeconds int       `json:"interval_seconds,omitempty"`
	Enabled         bool      `json:"enabled"`
}
type Package struct {
	Kind       string `json:"kind"`
	ID         string `json:"id"`
	LocalID    string `json:"local_id"`
	Source     string `json:"source"`
	SHA256     string `json:"sha256"`
	Version    string `json:"version,omitempty"`
	Projection bool   `json:"projection"`
}
type Snapshot struct {
	ID             string    `json:"id"`
	ConfigRevision uint64    `json:"config_revision"`
	Packages       []Package `json:"packages"`
	CreatedAt      time.Time `json:"created_at"`
}
type Adoption struct {
	ResumeOnly        bool   `json:"resume_original_thread,omitempty"`
	PendingGeneration string `json:"pending_generation,omitempty"`
	PendingReason     string `json:"pending_reason,omitempty"`
	Previous          string `json:"previous,omitempty"`
	BlockedGeneration string `json:"blocked_generation,omitempty"`
	BlockedRevision   uint64 `json:"blocked_revision,omitempty"`
	Execution         string `json:"execution"`
	Generation        string `json:"generation"`
	State             string `json:"state"`
}
type Status struct {
	SyncHeld          bool                `json:"sync_held,omitempty"`
	SourceStamp       string              `json:"source_stamp,omitempty"`
	AdoptionsNext     string              `json:"adoptions_next,omitempty"`
	AdoptionsMore     bool                `json:"adoptions_more,omitempty"`
	PendingRequestIDs []string            `json:"pending_request_ids,omitempty"`
	WatchEnabled      bool                `json:"watch_enabled,omitempty"`
	Profile           Profile             `json:"profile"`
	Revision          uint64              `json:"revision"`
	Latest            string              `json:"latest,omitempty"`
	Snapshots         []Snapshot          `json:"snapshots,omitempty"`
	Adoptions         map[string]Adoption `json:"adoptions,omitempty"`
	LastError         string              `json:"last_error,omitempty"`
	Deleted           bool                `json:"deleted,omitempty"`
}
type Request struct {
	After            string   `json:"after,omitempty"`
	Limit            int      `json:"limit,omitempty"`
	Action           string   `json:"action"`
	ID               string   `json:"id,omitempty"`
	RequestID        string   `json:"request_id,omitempty"`
	ExpectedRevision uint64   `json:"expected_control_revision"`
	Profile          *Profile `json:"profile,omitempty"`
	Kind             string   `json:"kind,omitempty"`
	Item             Item     `json:"item,omitempty"`
	Generation       string   `json:"generation,omitempty"`
	DryRun           bool     `json:"dry_run,omitempty"`
}
type Result struct {
	PluginCache  *CacheInventory   `json:"plugin_cache,omitempty"`
	RequestID    string            `json:"request_id,omitempty"`
	Status       *Status           `json:"status,omitempty"`
	Environments []Status          `json:"environments,omitempty"`
	Candidate    *Snapshot         `json:"candidate,omitempty"`
	Changed      bool              `json:"changed"`
	RequestState string            `json:"request_state,omitempty"`
	Checks       map[string]string `json:"checks,omitempty"`
}
type receipt struct {
	Error   string  `json:"error,omitempty"`
	Hash    string  `json:"hash"`
	Request Request `json:"request"`
	State   string  `json:"state"`
	Result  Result  `json:"result"`
}
type record struct {
	Status   Status             `json:"status"`
	Requests map[string]receipt `json:"requests"`
}

// Service dependencies are explicit; no process-global current environment.
type Service struct {
	Root      string
	UserHome  string
	Binary    string
	Systemctl func(context.Context, []string) error
	Platform  func(context.Context) error
	Now       func() time.Time
	// Validate optionally performs the native no-model capability probe.
	Validate func(context.Context, Profile, string) error
}
type Bindings struct {
	Directory string            `json:"directory"`
	Runtimes  map[string]string `json:"runtimes"`
}
type Binding struct {
	Directory string
	ID        string
}

func (b *Bindings) Resolve(runtime string) *Binding {
	if b == nil || b.Runtimes[runtime] == "" {
		return nil
	}
	return &Binding{Directory: b.Directory, ID: b.Runtimes[runtime]}
}
func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
func (s *Service) directory(id string) (string, error) {
	if !validID.MatchString(id) || !filepath.IsAbs(s.Root) {
		return "", fmt.Errorf("invalid environment ID or store directory")
	}
	return filepath.Join(s.Root, id), nil
}
func defaults(p Profile) Profile {
	if p.Format == 0 {
		p.Format = 1
	}
	if p.Skills.Mode == "" {
		p.Skills.Mode = "clean"
	}
	if p.Plugins.Mode == "" {
		p.Plugins.Mode = "clean"
	}
	if p.Agents.Mode == "" {
		p.Agents.Mode = "independent"
	}
	if p.IntervalSeconds == 0 {
		p.IntervalSeconds = 60
	}
	if p.SourceHome != "" && p.AuthSource == "" {
		p.AuthSource = filepath.Join(p.SourceHome, "auth.json")
	}
	if p.SourceSkills == "" && p.UserHome != "" {
		p.SourceSkills = filepath.Join(p.UserHome, ".agents", "skills")
	}
	if p.Agents.Mode == "sync" && p.Agents.Source == "" {
		p.Agents.Source = filepath.Join(p.SourceHome, "AGENTS.md")
	}
	return p
}
func validate(p Profile) error {
	if p.Format != 1 || !validID.MatchString(p.ID) || !filepath.IsAbs(p.Home) || !filepath.IsAbs(p.UserHome) || p.Home == p.UserHome || p.IntervalSeconds < 1 {
		return fmt.Errorf("invalid environment description")
	}
	for _, path := range []string{p.SourceHome, p.SourceSkills, p.AuthSource, p.BaseConfig, p.Agents.Source} {
		if path != "" && !filepath.IsAbs(path) {
			return fmt.Errorf("environment paths must be absolute")
		}
	}
	if p.SourceHome != "" && filepath.Clean(p.SourceHome) == filepath.Clean(p.Home) {
		return fmt.Errorf("runtime home must differ from source home")
	}
	for _, selection := range []Selection{p.Skills, p.Plugins} {
		if selection.Mode != "clean" && selection.Mode != "projection" {
			return fmt.Errorf("extension mode must be clean or projection")
		}
		if selection.Mode == "clean" && len(selection.Allow) > 0 {
			return fmt.Errorf("clean mode cannot contain projected items")
		}
		seen := map[string]bool{}
		for _, item := range append(append([]Item{}, selection.Allow...), selection.Installed...) {
			if item.ID == "" || seen[item.ID] || item.ID == ".system" {
				return fmt.Errorf("empty, duplicate or reserved extension identity")
			}
			seen[item.ID] = true
			if item.Path != "" && !filepath.IsAbs(item.Path) {
				return fmt.Errorf("package paths must be absolute")
			}
		}
		for _, item := range selection.Installed {
			if item.Path == "" {
				return fmt.Errorf("installed packages require a local source path")
			}
		}
	}
	if p.Agents.Mode != "independent" && p.Agents.Mode != "sync" {
		return fmt.Errorf("AGENTS mode must be independent or sync")
	}
	return nil
}
func clone[T any](v T) T {
	raw, _ := json.Marshal(v)
	var out T
	_ = json.Unmarshal(raw, &out)
	return out
}
