package domain

import (
	"regexp"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

// Workspace describes the repositories a task works on. Each mount becomes
// one worktree, in its own sub-directory of the task's workspace.
type Workspace struct {
	// Mode chooses a new Git workspace, an existing resource, or a plain directory.
	Mode string `json:"mode,omitempty"`
	// SourceTaskID identifies the task whose managed workspace is reused.
	SourceTaskID string `json:"source_task_id,omitempty"`
	// IncludeExistingChanges authorizes committing inherited uncommitted changes.
	IncludeExistingChanges bool `json:"include_existing_changes,omitempty"`
	// Template names the configured template the mounts were expanded
	// from. It records provenance only; the mounts are always explicit.
	Template string  `json:"template,omitempty"`
	Mounts   []Mount `json:"mounts"`
	// Profile selects a workspace-level prepare step on the runner, run
	// after every mount is prepared.
	Profile string `json:"profile,omitempty"`
}

// Mount is one repository in a workspace.
type Mount struct {
	// Repo is the logical name of a registered repository.
	Repo string `json:"repo"`
	// Path is the sub-directory in the workspace. Normalize defaults it to
	// Repo.
	Path string `json:"path,omitempty"`
	// Base is where the task branch starts. The zero value means the
	// remote trunk as resolved by the runner.
	Base Base `json:"base,omitzero"`
	// Profile selects the runner's prepare step for this mount.
	Profile string `json:"profile,omitempty"`
}

// Base pins the starting point of a mount. At most one field is set.
type Base struct {
	Branch string `json:"branch,omitempty"`
	Commit string `json:"commit,omitempty"`
}

// Requirements constrain which hosts may run the task.
type Requirements struct {
	HostID string   `json:"host_id,omitempty"`
	OS     string   `json:"os,omitempty"`
	Labels []string `json:"labels,omitempty"`
}

// NamePattern is the shape of repository names, mount paths, workspace
// names and profiles: one lowercase path segment.
var NamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

var commitPattern = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// Normalize returns the workspace with defaults filled in, so stored specs
// are explicit.
func (w Workspace) Normalize() Workspace {
	w.Mounts = slices.Clone(w.Mounts)
	for i := range w.Mounts {
		if w.Mounts[i].Path == "" {
			w.Mounts[i].Path = w.Mounts[i].Repo
		}
	}
	return w
}

// Validate checks a normalized workspace. Whether the repositories are
// registered is checked by the caller, which has the registry.
func (w Workspace) Validate() error {
	switch w.ModeValue() {
	case WorkspaceNew:
		if w.SourceTaskID != "" || w.IncludeExistingChanges {
			return invalid("new workspaces cannot reference an existing task or its changes; continuing a source_task_id requires explicit workspace.mode=reuse and separate review of include_existing_changes for commit scope")
		}
	case WorkspaceTemporary:
		if len(w.Mounts) != 0 || w.Template != "" || w.SourceTaskID != "" || w.IncludeExistingChanges {
			return invalid("temporary workspaces cannot have repository mounts or a source task")
		}
	case WorkspaceReuse:
		if strings.TrimSpace(w.SourceTaskID) == "" {
			return invalid("reuse requires source_task_id")
		}
	default:
		return invalid("workspace mode %q is not supported", w.Mode)
	}
	if len(w.Mounts) == 0 && w.ModeValue() == WorkspaceNew {
		return invalid("workspace needs at least one mount")
	}
	if w.Profile != "" && !NamePattern.MatchString(w.Profile) {
		return invalid("workspace profile %q is malformed", w.Profile)
	}
	paths := map[string]bool{}
	repos := map[string]bool{}
	for _, m := range w.Mounts {
		switch {
		case !NamePattern.MatchString(m.Repo):
			return invalid("repo %q is malformed", m.Repo)
		case !NamePattern.MatchString(m.Path):
			return invalid("mount path %q must be a single lowercase directory name", m.Path)
		case m.Profile != "" && !NamePattern.MatchString(m.Profile):
			return invalid("mount profile %q is malformed", m.Profile)
		case paths[m.Path]:
			return invalid("mount path %q is used twice", m.Path)
		case repos[m.Repo]:
			// Mounts of one repository would share its clone and the
			// task branch name.
			return invalid("repo %q is mounted twice", m.Repo)
		case m.Base.Branch != "" && m.Base.Commit != "":
			return invalid("mount %q sets both a base branch and a base commit", m.Path)
		case m.Base.Commit != "" && !commitPattern.MatchString(m.Base.Commit):
			return invalid("base commit of mount %q must be a full lowercase hex sha", m.Path)
		case m.Base.Branch != "" && !ValidBranch(m.Base.Branch):
			return invalid("base branch %q of mount %q is malformed", m.Base.Branch, m.Path)
		}
		paths[m.Path] = true
		repos[m.Repo] = true
	}
	return nil
}

const (
	WorkspaceNew       = "new"
	WorkspaceReuse     = "reuse"
	WorkspaceTemporary = "temporary"
)

// ModeValue preserves the legacy wire representation while applying its default.
func (w Workspace) ModeValue() string {
	if w.Mode == "" {
		return WorkspaceNew
	}
	return w.Mode
}

// ValidBranch rejects branch names git would refuse (the rules of git
// check-ref-format --branch) and ones that could be read as options.
func ValidBranch(b string) bool {
	if b == "" || b == "@" || strings.HasPrefix(b, "-") || strings.HasSuffix(b, ".") ||
		strings.Contains(b, "..") || strings.Contains(b, "@{") {
		return false
	}
	if strings.ContainsFunc(b, func(r rune) bool {
		return r <= ' ' || r == 0x7f || strings.ContainsRune(`~^:?*[\`, r)
	}) {
		return false
	}
	for _, c := range strings.Split(b, "/") {
		if c == "" || strings.HasPrefix(c, ".") || strings.HasSuffix(c, ".lock") {
			return false
		}
	}
	return true
}

// TaskBranch is the branch a task works on in every repository of its
// workspace.
func TaskBranch(workspaceName string) string { return "tend/" + workspaceName }

// ValidWorkspaceName reports whether name can be both the workspace
// directory and part of the task branch.
func ValidWorkspaceName(name string) bool {
	return NamePattern.MatchString(name) && ValidBranch(TaskBranch(name))
}

// Repos returns the repositories of the workspace.
func (w Workspace) Repos() []string {
	out := make([]string, 0, len(w.Mounts))
	for _, m := range w.Mounts {
		out = append(out, m.Repo)
	}
	return out
}

// Equal reports whether two workspaces ask for the same thing. Template is
// provenance and does not count.
func (w Workspace) Equal(o Workspace) bool {
	return w.ModeValue() == o.ModeValue() && w.SourceTaskID == o.SourceTaskID &&
		w.IncludeExistingChanges == o.IncludeExistingChanges && w.Profile == o.Profile && slices.Equal(w.Mounts, o.Mounts)
}

// Equal reports whether two requirement sets are the same.
func (r Requirements) Equal(o Requirements) bool {
	return r.HostID == o.HostID && r.OS == o.OS && slices.Equal(r.Labels, o.Labels)
}

// checkPinnedWorkspace allows only additions once the task's workspace
// exists on a host: removing or changing a mount would discard or silently
// rebase work, and new requirements could exclude the host that holds it.
func checkPinnedWorkspace(current, next Spec) error {
	if current.Workspace.ModeValue() != next.Workspace.ModeValue() || current.Workspace.SourceTaskID != next.Workspace.SourceTaskID {
		return invalid("workspace binding cannot change once assigned; create a new task instead")
	}
	if !current.Requirements.Equal(next.Requirements) {
		return invalid("requirements cannot change once the task's workspace exists; create a new task instead")
	}
	byPath := map[string]Mount{}
	for _, m := range next.Workspace.Mounts {
		byPath[m.Path] = m
	}
	for _, m := range current.Workspace.Mounts {
		n, ok := byPath[m.Path]
		if !ok {
			return invalid("mount %q cannot be removed once the task's workspace exists", m.Path)
		}
		if n.Repo != m.Repo || n.Base != m.Base {
			return invalid("the repo and base of mount %q cannot change once the task's workspace exists", m.Path)
		}
	}
	return nil
}

func invalid(format string, args ...any) error {
	return protocol.Errorf(protocol.CodeInvalidRequest, format, args...)
}
