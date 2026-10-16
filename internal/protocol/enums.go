package protocol

import "slices"

// TaskLifecycle is the business stage of a task.
type TaskLifecycle string

// Task lifecycle stages.
const (
	TaskDraft              TaskLifecycle = "draft"
	TaskQueued             TaskLifecycle = "queued"
	TaskRunning            TaskLifecycle = "running"
	TaskAwaitingAcceptance TaskLifecycle = "awaiting_acceptance"
	TaskChangesRequired    TaskLifecycle = "changes_required"
	TaskCompleted          TaskLifecycle = "completed"
	TaskCancelled          TaskLifecycle = "cancelled"
)

// Valid reports whether l is a known lifecycle stage.
func (l TaskLifecycle) Valid() bool {
	return slices.Contains([]TaskLifecycle{TaskDraft, TaskQueued, TaskRunning, TaskAwaitingAcceptance, TaskChangesRequired, TaskCompleted, TaskCancelled}, l)
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
)

// Valid reports whether r is a known block reason.
func (r BlockReason) Valid() bool {
	return slices.Contains([]BlockReason{BlockNone, BlockWaitingResource, BlockWaitingDecision, BlockWaitingApproval, BlockAcceptanceEnvUnavailable, BlockBudgetExhausted}, r)
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
)

// Valid reports whether d is a known desired state.
func (d RunDesired) Valid() bool {
	return d == DesiredRun || d == DesiredPaused || d == DesiredStopped
}

// MessageState is how far the main agent got with an owner message.
type MessageState string

// Message states.
const (
	// MessageQueued: received, waiting for the main agent.
	MessageQueued MessageState = "queued"
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

// RunRole is the responsibility of a run.
type RunRole string

// Run roles.
const (
	RoleImplement RunRole = "implement"
	RoleAccept    RunRole = "accept"
)

// Valid reports whether r is a known role.
func (r RunRole) Valid() bool { return r == RoleImplement || r == RoleAccept }

// ActionKind is the kind of work a plan action commits to.
type ActionKind string

// Action kinds.
const (
	ActionImplement ActionKind = "implement"
	ActionAccept    ActionKind = "accept"
	ActionDeliver   ActionKind = "deliver"
)

// Valid reports whether k is a known action kind.
func (k ActionKind) Valid() bool {
	return k == ActionImplement || k == ActionAccept || k == ActionDeliver
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
