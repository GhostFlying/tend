// Package scheduler decides which queued action a host runs next. It is a
// pure function of the facts the caller gathers, so the rules can be tested
// exhaustively; the runner claim evaluates it inside its transaction.
package scheduler

import (
	"cmp"
	"slices"
	"time"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
)

// Host is the claiming host as configured, with its current load.
type Host struct {
	IssueManagers bool
	Executors     []Executor
	ID            string
	OS            string
	Active        bool
	Labels        []string
	Repos         []string
	Slots         int64
	// Busy counts the host's runs that have not finished.
	WorkspaceProtocol int64
	Busy              int64
	// Worker is what the host's worker can do (D64).
	Worker              protocol.WorkerCapabilities
	TaskInputs          bool
	WorkerObservation   bool
	AcceptanceExecution bool
}

// Candidate is a pending action together with the facts the rules need.
type Candidate struct {
	ManagerAuthorizationInvalid bool
	Manager                     *protocol.IssueManagerSpec
	ManagerPin                  *protocol.RunExecutionPin
	// NamedExecutionAssigned is derived from existing run pins for this spec.
	NamedExecutionAssigned bool
	// WorkspaceLegacyBindings prevents changing the backend contract of queued legacy peers.
	WorkspaceLegacyBindings bool
	AgentSelection          *protocol.TaskAgentSelection
	AcceptanceProfile       *protocol.ExecutionProfile
	WorkspaceBackendID      string
	ActionID                string
	TaskID                  string
	Kind                    protocol.ActionKind
	Status                  protocol.ActionStatus
	Generation              int64
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
	Rating              protocol.Rating
	// Weight is the weight of the task's project (D60).
	Weight int64
	// DependenciesDone reports that every task this one depends on is
	// completed.
	DependenciesDone bool
	ContextPending   bool
	// DependencyTaskID optionally identifies an unfinished prerequisite for views.
	// It does not change eligibility; DependenciesDone remains authoritative.
	DependencyTaskID string

	Requirements domain.Requirements
	Repos        []string
	// Mounts counts the mounts of the task's workspace.
	WorkspaceMode               string
	RequiresWorkspaceProtocol   bool
	RequiresTaskInputs          bool
	RequiresObservation         bool
	RequiresAcceptanceExecution bool
	Mounts                      int
	// WorkspaceHost is the host holding the task's workspace, or empty
	// before the workspace exists.
	WorkspaceHost string
	// WorkspaceOccupied reports that a run of the task, of either role,
	// may still be using its workspace: it is starting, running, waiting,
	// interrupted or unknown. Implementation and acceptance share the
	// workspace (D41).
	WorkspaceOccupied bool
	// AbandonedHosts are the hosts the task gave up a workspace on (D62).
	AbandonedHosts []string
}

// MultiMountUnsupported is why a host whose worker lays out only
// single-repository workspaces passes over a task with several mounts.
const MultiMountUnsupported = "host's worker does not support workspaces with more than one mount"

// Skip explains why a candidate is not eligible on a host. The empty string
// means it is eligible.
func Skip(h Host, c Candidate) string {
	if reason := SkipReason(h, c); reason != nil {
		return reason.Message
	}
	return ""
}

// SkipReason is the structured explanation of the same eligibility rule.
func SkipReason(h Host, c Candidate) *protocol.QueueWaitReason {
	switch {
	case !h.Active:
		return waitReason(protocol.QueueHostUnavailable, "host is retired")
	case h.Busy >= h.Slots:
		return waitReason(protocol.QueueHostCapacity, "host has no free slot")
	}
	if reason := HeldReason(c); reason != nil {
		return reason
	}
	if c.Manager != nil {
		if !h.IssueManagers {
			return waitReason(protocol.QueueHostRequirements, "runner does not support issue managers")
		}
		if c.WorkspaceOccupied {
			return waitReason(protocol.QueueWorkspaceBusy, "manager execution requires reconciliation")
		}
		if _, reason := SelectExecution(h, c); reason != "" {
			return waitReason(protocol.QueueHostRequirements, reason)
		}
		return nil
	}
	switch {
	case c.Requirements.HostID != "" && c.Requirements.HostID != h.ID:
		return waitReason(protocol.QueueHostRequirements, "task requires another host")
	case c.Requirements.OS != "" && c.Requirements.OS != h.OS:
		return waitReason(protocol.QueueHostRequirements, "host OS does not match")
	case !containsAll(h.Labels, c.Requirements.Labels):
		return waitReason(protocol.QueueHostRequirements, "host lacks a required label")
	case !containsAll(h.Repos, c.Repos):
		return waitReason(protocol.QueueHostRequirements, "host does not serve every repository")
	case c.AgentSelection == nil && c.RequiresAcceptanceExecution && !h.AcceptanceExecution:
		return waitReason(protocol.QueueHostRequirements, "runner does not support acceptance model selection")
	case c.RequiresObservation && !h.WorkerObservation:
		return waitReason(protocol.QueueHostRequirements, "runner does not support scoped worker observation")
	case c.RequiresTaskInputs && !h.TaskInputs:
		return waitReason(protocol.QueueHostRequirements, "runner does not support task input attachments")
	case c.RequiresWorkspaceProtocol && h.WorkspaceProtocol < 1:
		return waitReason(protocol.QueueHostRequirements, "runner does not support workspace resources and composable deliverables")
	case c.AgentSelection == nil && c.Mounts > 1 && !h.Worker.MultiMount:
		return waitReason(protocol.QueueHostRequirements, MultiMountUnsupported)
	case c.WorkspaceHost != "" && c.WorkspaceHost != h.ID:
		// The workspace, and the artifact commits in its repositories,
		// exist only on that host.
		return waitReason(protocol.QueueHostRequirements, "task workspace is on another host")
	case c.WorkspaceHost == "" && slices.Contains(c.AbandonedHosts, h.ID):
		// The abandoned workspace and its branches are still there and
		// would be taken up again.
		return waitReason(protocol.QueueHostRequirements, "task gave up its workspace on this host")
	}
	if c.AgentSelection != nil {
		if _, reason := SelectExecution(h, c); reason != "" {
			return waitReason(protocol.QueueHostRequirements, reason)
		}
	}
	return nil
}

// HeldReason explains why a candidate cannot run on any host, or returns nil
// when only the hosts decide.
func HeldReason(c Candidate) *protocol.QueueWaitReason {
	switch {
	case c.ManagerAuthorizationInvalid:
		return waitReason(protocol.QueueTaskBlocked, "manager publication source changed or was withdrawn; explicit reauthorization is required")
	case c.Status != protocol.ActionPending:
		return waitReason(protocol.QueueTaskBlocked, "action is not pending")
	case c.Kind != protocol.ActionImplement && c.Kind != protocol.ActionAccept && c.Kind != protocol.ActionCoordinate:
		return waitReason(protocol.QueueTaskBlocked, "action is not runnable")
	case c.TargetSpecVersion != c.SpecVersion:
		return waitReason(protocol.QueueTaskBlocked, "action targets an older spec version")
	case c.Lifecycle.Terminal() || c.Lifecycle == protocol.TaskDraft:
		return waitReason(protocol.QueueTaskBlocked, "task is not runnable in its lifecycle")
	case c.Control != protocol.ControlManaged:
		return waitReason(protocol.QueueTaskBlocked, "task is not managed")
	case c.Block != protocol.BlockNone:
		return waitReason(protocol.QueueTaskBlocked, "task is blocked")
	case c.ContextPending:
		return waitReason(protocol.QueueTaskBlocked, "work item context review is incomplete")
	case !c.DependenciesDone:
		return waitReason(protocol.QueueDependency, "dependencies are not completed")
	case c.Kind == protocol.ActionImplement && c.Generation != c.ImplementGeneration:
		return waitReason(protocol.QueueTaskBlocked, "implement action is not the current generation")
	case c.Kind == protocol.ActionAccept && (c.Lifecycle != protocol.TaskAwaitingAcceptance || c.TargetArtifactID != c.CurrentArtifactID):
		return waitReason(protocol.QueueTaskBlocked, "acceptance targets an artifact that is no longer under acceptance")
	case c.AgentSelection != nil && c.WorkspaceLegacyBindings:
		return waitReason(protocol.QueueWorkspaceBusy, "named workspace promotion waits for unfinished legacy task bindings")
	case c.WorkspaceOccupied:
		// Includes unknown runs: losing contact does not prove the old
		// run stopped writing.
		return waitReason(protocol.QueueWorkspaceBusy, "a run may still be using the workspace")
	}
	return nil
}

func waitReason(code protocol.QueueWaitCode, message string) *protocol.QueueWaitReason {
	return &protocol.QueueWaitReason{Code: code, Message: message}
}

// Held preserves the textual scheduler explanation for existing callers.
func Held(c Candidate) string {
	if reason := HeldReason(c); reason != nil {
		return reason.Message
	}
	return ""
}

// Order is how candidates are ranked against each other (D60).
type Order struct {
	// Now is the time, in Unix milliseconds, waiting is measured up to.
	Now int64
	// WaitPerHour is what an hour in the queue adds to a score.
	WaitPerHour float64
}

// Score is a candidate's place in the queue apart from p0: its project's
// weight times its rating's value, plus what its waiting time adds.
func Score(c Candidate, o Order) float64 {
	weight := max(c.Weight, 1)
	waited := float64(max(o.Now-c.CreatedAt, 0)) / float64(time.Hour.Milliseconds())
	return float64(weight*value(c.Rating)) + waited*o.WaitPerHour
}

// Compare orders two candidates, the one to run first first: p0 ahead of
// everything, oldest first; then the higher score; among equal scores
// acceptance before implementation, since it moves along a task that
// already has a result, and then the older action.
func Compare(a, b Candidate, o Order) int {
	ap, bp := a.Rating == protocol.RatingP0, b.Rating == protocol.RatingP0
	switch {
	case ap && !bp:
		return -1
	case bp && !ap:
		return 1
	case !ap:
		if c := cmp.Compare(Score(b, o), Score(a, o)); c != 0 {
			return c
		}
		if c := cmp.Compare(kindRank(a.Kind), kindRank(b.Kind)); c != 0 {
			return c
		}
	}
	return cmp.Or(cmp.Compare(a.CreatedAt, b.CreatedAt), cmp.Compare(a.ActionID, b.ActionID))
}

// Pick returns the eligible candidate that comes first in the order.
// Running work is never preempted.
func Pick(h Host, candidates []Candidate, o Order) (Candidate, bool) {
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
		return Compare(a, b, o)
	}), true
}

// kindRank puts acceptance ahead of implementation.
func kindRank(k protocol.ActionKind) int {
	if k == protocol.ActionAccept {
		return 0
	}
	return 1
}

// value is what a rating is worth in a score; an unrated task counts as
// p2 (D60). p0 is ordered ahead of scores and has no value of its own.
func value(r protocol.Rating) int64 {
	switch r {
	case protocol.RatingP1:
		return 3
	case protocol.RatingP3:
		return 1
	}
	return 2
}

func containsAll(have, want []string) bool {
	for _, w := range want {
		if !slices.Contains(have, w) {
			return false
		}
	}
	return true
}
