package protocol

import "slices"

// TaskLifecycle is the business stage of a task.
type TaskLifecycle string

// Task lifecycle stages.
const (
	TaskDraft              TaskLifecycle = "draft"
	TaskQueued             TaskLifecycle = "queued"
	TaskRunning            TaskLifecycle = "running"
	TaskWaiting            TaskLifecycle = "waiting"
	TaskAwaitingAcceptance TaskLifecycle = "awaiting_acceptance"
	TaskChangesRequired    TaskLifecycle = "changes_required"
	TaskCompleted          TaskLifecycle = "completed"
	TaskCancelled          TaskLifecycle = "cancelled"
)

// Valid reports whether l is a known lifecycle stage.
func (l TaskLifecycle) Valid() bool {
	return slices.Contains([]TaskLifecycle{TaskDraft, TaskQueued, TaskRunning, TaskWaiting, TaskAwaitingAcceptance, TaskChangesRequired, TaskCompleted, TaskCancelled}, l)
}

// Terminal reports whether no further transition is possible.
func (l TaskLifecycle) Terminal() bool { return l == TaskCompleted || l == TaskCancelled }

// ControlMode says who is allowed to drive a task.
type ControlMode string

// Control modes.
const (
	ControlManaged      ControlMode = "managed"
	ControlPaused       ControlMode = "paused"
	ControlHumanControl ControlMode = "human_control"
)

// Valid reports whether m is a known control mode.
func (m ControlMode) Valid() bool {
	return m == ControlManaged || m == ControlPaused || m == ControlHumanControl
}

// Rating is how much the owner wants a task done (D60). It orders the
// queue; an unrated task queues as p2 until someone rates it.
type Rating string

// Ratings.
const (
	RatingNone Rating = ""
	RatingP0   Rating = "p0" // urgent: ahead of everything else
	RatingP1   Rating = "p1" // high
	RatingP2   Rating = "p2" // normal
	RatingP3   Rating = "p3" // low
)

// Valid reports whether r is a known rating, including none.
func (r Rating) Valid() bool {
	return slices.Contains([]Rating{RatingNone, RatingP0, RatingP1, RatingP2, RatingP3}, r)
}

// Label names the rating for people, "unrated" for none.
func (r Rating) Label() string {
	if r == RatingNone {
		return "unrated"
	}
	return string(r)
}

// BlockReason explains why a task cannot progress, independently of its
// lifecycle stage.
type BlockReason string

// Block reasons.
const (
	BlockNone                     BlockReason = "none"
	BlockWaitingResource          BlockReason = "waiting_resource"
	BlockWaitingDecision          BlockReason = "waiting_decision"
	BlockWaitingApproval          BlockReason = "waiting_approval"
	BlockAcceptanceEnvUnavailable BlockReason = "acceptance_env_unavailable"
	BlockBudgetExhausted          BlockReason = "budget_exhausted"
	// BlockWorkspaceUnavailable: the host holding the task's workspace is
	// retired or has not been seen for too long (D62).
	BlockWorkspaceUnavailable BlockReason = "workspace_unavailable"
)

// Valid reports whether r is a known block reason.
func (r BlockReason) Valid() bool {
	return slices.Contains([]BlockReason{BlockNone, BlockWaitingResource, BlockWaitingDecision, BlockWaitingApproval, BlockAcceptanceEnvUnavailable,
		BlockBudgetExhausted, BlockWorkspaceUnavailable}, r)
}

// RunObserved is the last confirmed fact about a run.
type RunObserved string

// Run observed states.
const (
	RunStarting    RunObserved = "starting"
	RunRunning     RunObserved = "running"
	RunWaiting     RunObserved = "waiting"
	RunFinished    RunObserved = "finished"
	RunFailed      RunObserved = "failed"
	RunInterrupted RunObserved = "interrupted"
	RunUnknown     RunObserved = "unknown"
)

// Valid reports whether s is a known observed state.
func (s RunObserved) Valid() bool {
	return slices.Contains([]RunObserved{RunStarting, RunRunning, RunWaiting, RunFinished, RunFailed, RunInterrupted, RunUnknown}, s)
}

// RunDesired is what tend wants a run to do; it may differ from what is
// observed.
type RunDesired string

// Run desired states.
const (
	DesiredRun     RunDesired = "run"
	DesiredPaused  RunDesired = "paused"
	DesiredStopped RunDesired = "stopped"
	// DesiredHeld is a run the owner has taken over (D67): its worker
	// leaves the agent to them, untouched, and waits.
	DesiredHeld RunDesired = "held"
)

// Valid reports whether d is a known desired state.
func (d RunDesired) Valid() bool {
	return d == DesiredRun || d == DesiredPaused || d == DesiredStopped || d == DesiredHeld
}

// MessageState is how far the main agent got with an owner message.
type MessageState string

// Message states.
const (
	// MessageQueued: received, waiting for the main agent.
	MessageQueued MessageState = "queued"
	// MessageAccepted: native accepted steer input, consumption is not yet witnessed.
	MessageAccepted MessageState = "accepted"
	// MessageProcessed: consumption and turn completion are witnessed, without a reply.
	MessageProcessed MessageState = "processed"
	// MessageProcessing: the main agent's turn for it is running.
	MessageProcessing MessageState = "processing"
	// MessageReplied: the turn completed.
	MessageReplied MessageState = "replied"
	// MessageInterrupted: the turn was stopped before it completed.
	MessageInterrupted MessageState = "interrupted"
	// MessageFailed: the turn failed or the message could not be delivered.
	MessageFailed MessageState = "failed"
	// MessageUnknown: whether the main agent handled it cannot be told.
	MessageUnknown MessageState = "unknown"
)

// ToolStatus is how far a tool use of the main agent got (D69).
type ToolStatus string

// Tool use states.
const (
	ToolRunning   ToolStatus = "running"
	ToolCompleted ToolStatus = "completed"
	ToolFailed    ToolStatus = "failed"
	// ToolInterrupted: the harness went away before the use ended, so
	// whether it finished cannot be told.
	ToolInterrupted ToolStatus = "interrupted"
)

// RunRole is the responsibility of a run.
type RunRole string

// Run roles.
const (
	RoleImplement    RunRole = "implement"
	RoleIssueManager RunRole = "issue_manager"
	RoleAccept       RunRole = "accept"
)

// Valid reports whether r is a known role.
func (r RunRole) Valid() bool { return r == RoleImplement || r == RoleAccept || r == RoleIssueManager }

// ActionKind is the kind of work a plan action commits to.
type ActionKind string

// Action kinds.
const (
	ActionImplement  ActionKind = "implement"
	ActionCoordinate ActionKind = "coordinate"
	ActionAccept     ActionKind = "accept"
	ActionDeliver    ActionKind = "deliver"
)

// Valid reports whether k is a known action kind.
func (k ActionKind) Valid() bool {
	return k == ActionCoordinate || k == ActionImplement || k == ActionAccept || k == ActionDeliver
}

// DecisionEffect is what tend does itself when an option of a decision it
// opened is chosen (D53).
type DecisionEffect string

// Decision effects.
const (
	// EffectImplement requests the next implement generation.
	EffectImplement DecisionEffect = "implement"
	// EffectAccept requests the next acceptance of the current artifact.
	EffectAccept DecisionEffect = "accept"
	// EffectCancel cancels the task.
	EffectCancel DecisionEffect = "cancel"
	// EffectStartOver gives up the task's workspace and starts the next
	// implement generation in a new one on another host (D62).
	EffectStartOver DecisionEffect = "start_over"
)

// ActionStatus is the progress of a plan action.
type ActionStatus string

// Action statuses.
const (
	ActionPending    ActionStatus = "pending"
	ActionReady      ActionStatus = "ready"
	ActionDispatched ActionStatus = "dispatched"
	ActionDone       ActionStatus = "done"
	ActionCancelled  ActionStatus = "cancelled"
	ActionSuperseded ActionStatus = "superseded"
)

// Valid reports whether s is a known action status.
func (s ActionStatus) Valid() bool {
	return slices.Contains([]ActionStatus{ActionPending, ActionReady, ActionDispatched, ActionDone, ActionCancelled, ActionSuperseded}, s)
}

// Verdict is the conclusion of an independent acceptance run.
type Verdict string

// Verdicts.
const (
	VerdictPassed          Verdict = "passed"
	VerdictChangesRequired Verdict = "changes_required"
	VerdictBlocked         Verdict = "blocked"
	VerdictInconclusive    Verdict = "inconclusive"
)

// Valid reports whether v is a known verdict.
func (v Verdict) Valid() bool {
	return slices.Contains([]Verdict{VerdictPassed, VerdictChangesRequired, VerdictBlocked, VerdictInconclusive}, v)
}

// WorkerKind is the harness a host runs its workers with (D64). The
// server's configuration names it for every host, and the runner reports
// the one it runs.
type WorkerKind string

// Worker kinds.
const (
	// WorkerAppServer drives traex through its app-server in workspaces
	// the runner lays out itself (D38, D41).
	WorkerAppServer WorkerKind = "appserver"
	// WorkerOrca runs agent TUIs in orca worktrees (D63).
	WorkerOrca WorkerKind = "orca"
)

// WorkerKinds lists the known worker kinds.
var WorkerKinds = []WorkerKind{WorkerAppServer, WorkerOrca}

// Valid reports whether k is a known worker kind.
func (k WorkerKind) Valid() bool { return slices.Contains(WorkerKinds, k) }

// WorkerCapabilities are what a worker kind can do that tend's own rules
// depend on (D63, D64).
type WorkerCapabilities struct {
	// MultiMount means a workspace may hold more than one mount.
	MultiMount bool
}

// Capabilities returns what the worker kind can do.
func (k WorkerKind) Capabilities() WorkerCapabilities {
	return WorkerCapabilities{MultiMount: k == WorkerAppServer}
}
