package app

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// SyncConfig handles config.sync: tend brings the hosts, repositories,
// workspace templates and projects of its configuration file into the
// ledger at startup. Hosts, repositories and projects missing from the file
// are retired rather than deleted, since runs, specs and tasks refer to
// them.
//
// To stop new tasks from mounting a repository while existing tasks finish,
// keep it in the file with retired set: hosts may go on serving it. A
// repository dropped from the file is retired too, but no host can serve
// it any more.
type SyncConfig struct{}

// ConfigPayload is the payload of config.sync.
type ConfigPayload struct {
	Hosts     []HostConfig     `json:"hosts"`
	Repos     []RepoConfig     `json:"repos"`
	Templates []TemplateConfig `json:"templates"`
	// Projects may be empty: tend then keeps every task in the implicit
	// default project.
	Projects []ProjectConfig `json:"projects"`
}

// HostConfig is a configured execution host.
type HostConfig struct {
	HostID string   `json:"host_id"`
	OS     string   `json:"os"`
	Arch   string   `json:"arch"`
	Labels []string `json:"labels,omitempty"`
	// Repos lists the registered repositories the host serves.
	Repos []string `json:"repos"`
	Slots int64    `json:"slots"`
	// TokenHash is the lowercase hex SHA-256 of the host's runner token.
	TokenHash string `json:"token_hash"`
	// Nodes lists the tailnet nodes allowed to act as this host.
	Nodes []string `json:"nodes"`
}

// RepoConfig registers a repository.
type RepoConfig struct {
	Repo      string `json:"repo"`
	RemoteURL string `json:"remote_url"`
	// BaseCandidates are tried in order to find the default base branch,
	// before the runner's heuristics.
	BaseCandidates []string `json:"base_candidates,omitempty"`
	// Retired stops new tasks from mounting the repository. Tasks that
	// already mount it keep running on the hosts that serve it.
	Retired bool `json:"retired,omitempty"`
}

// TemplateConfig names a reusable workspace layout.
type TemplateConfig struct {
	Name      string           `json:"name"`
	Workspace domain.Workspace `json:"workspace"`
}

// ProjectConfig defines a project (D52, D58).
type ProjectConfig struct {
	Name  string `json:"name"`
	Title string `json:"title,omitempty"`
	// Weight multiplies the rating of the project's tasks in the queue
	// order. Default 1.
	Weight int64 `json:"weight,omitempty"`
	// Repos lists the registered repositories the project works in. An
	// agent session in one of them is bound to the project.
	Repos []string `json:"repos"`
	// Template is the workspace template of tasks created without a
	// workspace.
	Template string `json:"template,omitempty"`
}

// DefaultProject is the project of every task when none is configured.
const DefaultProject = "default"

var (
	hostOSes   = []string{"linux", "darwin", "windows"}
	hashFormat = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func (p *ConfigPayload) validate() error {
	repos := map[string]bool{}  // every configured repository
	active := map[string]bool{} // those new tasks may mount
	for _, r := range p.Repos {
		switch {
		case !domain.NamePattern.MatchString(r.Repo):
			return invalid("repo name %q is malformed", r.Repo)
		case repos[r.Repo]:
			return invalid("repo %q is configured twice", r.Repo)
		case r.RemoteURL == "":
			return invalid("repo %q has no remote_url", r.Repo)
		}
		for _, b := range r.BaseCandidates {
			if !domain.ValidBranch(b) {
				return invalid("base candidate %q of repo %q is malformed", b, r.Repo)
			}
		}
		repos[r.Repo] = true
		active[r.Repo] = !r.Retired
	}
	hosts := map[string]bool{}
	for _, h := range p.Hosts {
		switch {
		case !domain.NamePattern.MatchString(h.HostID):
			return invalid("host id %q is malformed", h.HostID)
		case hosts[h.HostID]:
			return invalid("host %q is configured twice", h.HostID)
		case !slices.Contains(hostOSes, h.OS):
			return invalid("host %q has unsupported os %q", h.HostID, h.OS)
		case h.Arch == "":
			return invalid("host %q has no arch", h.HostID)
		case h.Slots < 0:
			return invalid("host %q has negative slots", h.HostID)
		case !hashFormat.MatchString(h.TokenHash):
			return invalid("token_hash of host %q must be a lowercase hex SHA-256", h.HostID)
		case len(h.Nodes) == 0 || slices.Contains(h.Nodes, ""):
			return invalid("host %q needs at least one tailnet node", h.HostID)
		}
		for _, l := range h.Labels {
			if !domain.NamePattern.MatchString(l) {
				return invalid("label %q of host %q is malformed", l, h.HostID)
			}
		}
		for _, r := range h.Repos {
			if !repos[r] {
				return invalid("host %q serves unknown repo %q", h.HostID, r)
			}
		}
		hosts[h.HostID] = true
	}
	templates := map[string]bool{}
	for i, t := range p.Templates {
		switch {
		case !domain.NamePattern.MatchString(t.Name):
			return invalid("template name %q is malformed", t.Name)
		case templates[t.Name]:
			return invalid("template %q is configured twice", t.Name)
		case t.Workspace.Template != "":
			return invalid("template %q must list its mounts, not refer to another template", t.Name)
		}
		p.Templates[i].Workspace = t.Workspace.Normalize()
		if err := p.Templates[i].Workspace.Validate(); err != nil {
			return fmt.Errorf("template %q: %w", t.Name, err)
		}
		for _, r := range t.Workspace.Repos() {
			if !active[r] {
				return invalid("template %q mounts unknown or retired repo %q", t.Name, r)
			}
		}
		templates[t.Name] = true
	}
	projects := map[string]bool{}
	for i, pr := range p.Projects {
		switch {
		case !domain.NamePattern.MatchString(pr.Name):
			return invalid("project name %q is malformed", pr.Name)
		case projects[pr.Name]:
			return invalid("project %q is configured twice", pr.Name)
		case pr.Weight < 0:
			return invalid("project %q has a negative weight", pr.Name)
		case pr.Template != "" && !templates[pr.Template]:
			return invalid("project %q uses unknown template %q", pr.Name, pr.Template)
		}
		for _, r := range pr.Repos {
			if !repos[r] {
				return invalid("project %q works in unknown repo %q", pr.Name, r)
			}
		}
		if pr.Title == "" {
			p.Projects[i].Title = pr.Name
		}
		if pr.Weight == 0 {
			p.Projects[i].Weight = 1
		}
		projects[pr.Name] = true
	}
	if len(p.Projects) == 0 {
		// The implicit project holds every repository, so sessions in any
		// of them are bound to it.
		var all []string
		for _, r := range p.Repos {
			all = append(all, r.Repo)
		}
		p.Projects = []ProjectConfig{{Name: DefaultProject, Title: "Default", Weight: 1, Repos: all}}
	}
	return nil
}

// Operation implements Command.
func (SyncConfig) Operation() string { return "config.sync" }

// Authorize implements Command.
func (SyncConfig) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalSystem)
}

// Apply implements Command.
func (SyncConfig) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p ConfigPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if err := p.validate(); err != nil {
		return Outcome{}, err
	}
	now := x.Now.UnixMilli()
	var repoNames, hostIDs []string
	for _, r := range p.Repos {
		if err := tx.UpsertRepo(ctx, store.RepoRow{
			Repo: r.Repo, RemoteURL: r.RemoteURL, BaseCandidates: r.BaseCandidates, Retired: r.Retired,
		}, now); err != nil {
			return Outcome{}, err
		}
		if !r.Retired {
			repoNames = append(repoNames, r.Repo)
		}
	}
	if err := tx.RetireReposExcept(ctx, repoNames, now); err != nil {
		return Outcome{}, err
	}
	for _, h := range p.Hosts {
		if err := tx.UpsertHost(ctx, store.HostRow{
			HostID: h.HostID, OS: h.OS, Arch: h.Arch, Labels: h.Labels, Repos: h.Repos, Slots: h.Slots,
			TokenHash: h.TokenHash, Nodes: h.Nodes,
		}, now); err != nil {
			return Outcome{}, err
		}
		hostIDs = append(hostIDs, h.HostID)
	}
	if err := tx.RetireHostsExcept(ctx, hostIDs, now); err != nil {
		return Outcome{}, err
	}
	templates := map[string]string{}
	for _, t := range p.Templates {
		b, err := json.Marshal(t.Workspace)
		if err != nil {
			return Outcome{}, fmt.Errorf("app: encode template: %w", err)
		}
		templates[t.Name] = string(b)
	}
	if err := tx.PutTemplates(ctx, templates, now); err != nil {
		return Outcome{}, err
	}
	var projectNames []string
	for _, pr := range p.Projects {
		if err := tx.UpsertProject(ctx, store.ProjectRow{
			Project: pr.Name, Title: pr.Title, Weight: pr.Weight, Repos: pr.Repos, Template: pr.Template,
		}, now); err != nil {
			return Outcome{}, err
		}
		projectNames = append(projectNames, pr.Name)
	}
	if err := tx.RetireProjectsExcept(ctx, projectNames, now); err != nil {
		return Outcome{}, err
	}
	counts := map[string]any{"hosts": len(p.Hosts), "repos": len(p.Repos), "templates": len(p.Templates), "projects": len(p.Projects)}
	return Outcome{
		Result: counts,
		Events: []Event{{ObjectType: "config", ObjectID: "server", Kind: "config.synced", Body: counts}},
	}, nil
}

func invalid(format string, args ...any) error {
	return protocol.Errorf(protocol.CodeInvalidRequest, format, args...)
}
