// Package scheduler decides which queued action a host runs next. It is a
// pure function of the facts the caller gathers, so the rules can be tested
// exhaustively; the runner claim evaluates it inside its transaction.
package scheduler

import (
	"cmp"
	"slices"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
)

// Host is the claiming host as configured, with its current load.
type Host struct {
	ID     string
	OS     string
	Active bool
	Labels []string
	Repos  []string
	Slots  int64
	// Busy counts the host's runs that have not finished.
	Busy int64
}

// Candidate is a pending action together with the facts the rules need.
type Candidate struct {
	ActionID   string
	TaskID     string
	Kind       protocol.ActionKind
	Status     protocol.ActionStatus
	Generation int64
	// TargetSpecVersion and TargetArtifactID are what the action was
	// created for.
	TargetSpecVersion int64
	TargetArtifactID  string
	CreatedAt         int64

	Lifecycle           protocol.TaskLifecycle
	Control             protocol.ControlMode
	Block               protocol.BlockReason
	SpecVersion         int64
	ImplementGeneration int64
	CurrentArtifactID   string
	Priority            int64
	// DependenciesDone reports that every task this one depends on is
	// completed.
	DependenciesDone bool

	Requirements domain.Requirements
	Repos        []string
	// WorkspaceHost is the host holding the task's workspace, or empty
	// before the workspace exists.
	WorkspaceHost string
	// ImplementOccupied reports that an implement run of the task may
	// still write to its workspace: it is starting, running, waiting,
	// interrupted or unknown.
	ImplementOccupied bool
}

// Skip explains why a candidate is not eligible on a host. The empty string
// means it is eligible.
func Skip(h Host, c Candidate) string {
	switch {
	case !h.Active:
		return "host is retired"
	case h.Busy >= h.Slots:
		return "host has no free slot"
	case c.Status != protocol.ActionPending:
		return "action is not pending"
	case c.Kind != protocol.ActionImplement && c.Kind != protocol.ActionAccept:
		return "action is not runnable"
	case c.TargetSpecVersion != c.SpecVersion:
		return "action targets an older spec version"
	case c.Lifecycle.Terminal() || c.Lifecycle == protocol.TaskDraft:
		return "task is not runnable in its lifecycle"
	case c.Control != protocol.ControlManaged:
		return "task is not managed"
	case c.Block != protocol.BlockNone:
		return "task is blocked"
	case !c.DependenciesDone:
		return "dependencies are not completed"
	case c.Kind == protocol.ActionImplement && c.Generation != c.ImplementGeneration:
		return "implement action is not the current generation"
	case c.Kind == protocol.ActionAccept && (c.Lifecycle != protocol.TaskAwaitingAcceptance || c.TargetArtifactID != c.CurrentArtifactID):
		return "acceptance targets an artifact that is no longer under acceptance"
	case c.Requirements.OS != "" && c.Requirements.OS != h.OS:
		return "host OS does not match"
	case !containsAll(h.Labels, c.Requirements.Labels):
		return "host lacks a required label"
	case !containsAll(h.Repos, c.Repos):
		return "host does not serve every repository"
	case c.WorkspaceHost != "" && c.WorkspaceHost != h.ID:
		// The workspace, and the artifact commits in its repositories,
		// exist only on that host.
		return "task workspace is on another host"
	case c.Kind == protocol.ActionImplement && c.ImplementOccupied:
		// Includes unknown runs: losing contact does not prove the old
		// run stopped writing.
		return "an implement run may still be using the workspace"
	}
	return ""
}

// Pick returns the eligible candidate with the highest priority, oldest
// first among equals. Running work is never preempted.
func Pick(h Host, candidates []Candidate) (Candidate, bool) {
	var eligible []Candidate
	for _, c := range candidates {
		if Skip(h, c) == "" {
			eligible = append(eligible, c)
		}
	}
	if len(eligible) == 0 {
		return Candidate{}, false
	}
	return slices.MinFunc(eligible, func(a, b Candidate) int {
		return cmp.Or(cmp.Compare(b.Priority, a.Priority), cmp.Compare(a.CreatedAt, b.CreatedAt), cmp.Compare(a.ActionID, b.ActionID))
	}), true
}

func containsAll(have, want []string) bool {
	for _, w := range want {
		if !slices.Contains(have, w) {
			return false
		}
	}
	return true
}
