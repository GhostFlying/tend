// Package domain holds the pure business rules of the ledger: task state
// transitions, completion conditions and the derivation of follow-up
// actions. Functions take the current state and command parameters and
// return a Decision; they perform no IO and read no clock or randomness.
package domain

import (
	"fmt"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
)

// DefaultMaxImplementAttempts bounds the implement generations per spec
// version when a spec does not set its own budget.
const DefaultMaxImplementAttempts = 3

// Spec is the content of one immutable task spec version.
type Spec struct {
	Goal        string   `json:"goal"`
	Scope       string   `json:"scope,omitempty"`
	Constraints []string `json:"constraints,omitempty"`
	Acceptance  []string `json:"acceptance"`
	// Delivery describes what completes the task. The MVP supports only
	// "commit": an accepted commit on the task branch.
	Delivery string `json:"delivery"`
	// MaxImplementAttempts bounds implement generations for this version;
	// zero means DefaultMaxImplementAttempts.
	MaxImplementAttempts int64 `json:"max_implement_attempts,omitempty"`
}

// DeliveryCommit is the only delivery kind in the MVP.
const DeliveryCommit = "commit"

// Executable reports whether the spec is concrete enough to start work.
func (s Spec) Executable() bool {
	return strings.TrimSpace(s.Goal) != "" && len(s.Acceptance) > 0
}

// Validate checks the spec for values the ledger cannot act on.
func (s Spec) Validate() error {
	switch {
	case s.Delivery != DeliveryCommit:
		return protocol.Errorf(protocol.CodeInvalidRequest, "delivery %q is not supported", s.Delivery)
	case s.MaxImplementAttempts < 0:
		return protocol.Errorf(protocol.CodeInvalidRequest, "max_implement_attempts must not be negative")
	}
	return nil
}

// Budget returns the effective implement attempt budget.
func (s Spec) Budget() int64 {
	if s.MaxImplementAttempts == 0 {
		return DefaultMaxImplementAttempts
	}
	return s.MaxImplementAttempts
}

// Equal reports whether two specs ask for the same thing.
func (s Spec) Equal(o Spec) bool {
	return s.Goal == o.Goal && s.Scope == o.Scope && s.Delivery == o.Delivery &&
		s.Budget() == o.Budget() && slices.Equal(s.Constraints, o.Constraints) && slices.Equal(s.Acceptance, o.Acceptance)
}

// Task is the projection of a task that the rules operate on.
type Task struct {
	ID              string
	Lifecycle       protocol.TaskLifecycle
	Control         protocol.ControlMode
	ControlRevision int64
	SpecVersion     int64
	Priority        int64
	Block           protocol.BlockReason
	// ImplementGeneration is the generation of the latest implement action
	// for the current spec version.
	ImplementGeneration int64
	// CurrentArtifactID is the artifact under acceptance for the current
	// spec version, if any.
	CurrentArtifactID string
	// Budget is the implement attempt budget of the current spec version.
	Budget int64
}

// ActionDraft is a follow-up action the caller must create.
type ActionDraft struct {
	Kind        protocol.ActionKind
	TaskID      string
	SpecVersion int64
	ArtifactID  string
	Generation  int64
}

// EffectKey identifies the business effect of an action. The store keeps it
// unique, so deriving the same effect twice can never create two actions.
func (a ActionDraft) EffectKey() string {
	artifact := a.ArtifactID
	if artifact == "" {
		artifact = "-"
	}
	return fmt.Sprintf("%s|%s|%d|%s|%d", a.Kind, a.TaskID, a.SpecVersion, artifact, a.Generation)
}

// EventDraft is a domain event to append to the ledger.
type EventDraft struct {
	Kind string
	Body map[string]any
}

// DecisionDraft is a question that needs the owner.
type DecisionDraft struct {
	Question string
	Options  []string
}

// Decision is the outcome of applying a command to a task.
type Decision struct {
	// Task is the new task state; it equals the input when only facts were
	// recorded.
	Task    Task
	Events  []EventDraft
	Actions []ActionDraft
	// SupersedeOpenActions asks the caller to mark every open action of the
	// task whose spec version is older than Task.SpecVersion as superseded.
	SupersedeOpenActions bool
	// CancelOpenActions asks the caller to cancel every open action.
	CancelOpenActions bool
	OpenDecisions     []DecisionDraft
}

func errTransition(t Task, op string) error {
	return protocol.Errorf(protocol.CodeInvalidTransition, "cannot %s a task in %s", op, t.Lifecycle)
}

func checkRevision(t Task, expected int64) error {
	if expected != t.ControlRevision {
		return protocol.Errorf(protocol.CodeStaleRevision, "control revision is %d, request expected %d", t.ControlRevision, expected)
	}
	return nil
}

// implementAction returns the implement action for the task's current spec
// version and generation.
func implementAction(t Task) ActionDraft {
	return ActionDraft{Kind: protocol.ActionImplement, TaskID: t.ID, SpecVersion: t.SpecVersion, Generation: t.ImplementGeneration}
}

// NewTask creates a task from its first spec. An executable spec queues the
// task and commits to its first implement action; otherwise it stays a
// draft until the spec is completed.
func NewTask(id string, spec Spec, priority int64) (Decision, error) {
	if err := spec.Validate(); err != nil {
		return Decision{}, err
	}
	t := Task{
		ID:              id,
		Lifecycle:       protocol.TaskDraft,
		Control:         protocol.ControlManaged,
		ControlRevision: 1,
		SpecVersion:     1,
		Priority:        priority,
		Block:           protocol.BlockNone,
		Budget:          spec.Budget(),
	}
	d := Decision{Events: []EventDraft{{Kind: "task.created", Body: map[string]any{"spec_version": 1, "priority": priority}}}}
	if spec.Executable() {
		t.Lifecycle = protocol.TaskQueued
		t.ImplementGeneration = 1
		d.Actions = append(d.Actions, implementAction(t))
	}
	d.Task = t
	return d, nil
}

// UpdateSpec records a new spec version. A spec that asks for the same thing
// is a no-op. Otherwise the version advances, work planned for older
// versions is superseded and the task returns to the queue.
func UpdateSpec(t Task, spec Spec, current Spec, expectedRevision int64) (Decision, error) {
	if err := checkRevision(t, expectedRevision); err != nil {
		return Decision{}, err
	}
	if t.Lifecycle.Terminal() {
		return Decision{}, errTransition(t, "change the spec of")
	}
	if err := spec.Validate(); err != nil {
		return Decision{}, err
	}
	if spec.Equal(current) {
		return Decision{Task: t}, nil
	}
	t.SpecVersion++
	t.ControlRevision++
	t.Budget = spec.Budget()
	t.CurrentArtifactID = ""
	t.Block = protocol.BlockNone
	d := Decision{
		SupersedeOpenActions: true,
		Events:               []EventDraft{{Kind: "task.spec_updated", Body: map[string]any{"spec_version": t.SpecVersion}}},
	}
	if spec.Executable() {
		t.Lifecycle = protocol.TaskQueued
		t.ImplementGeneration = 1
		d.Actions = append(d.Actions, implementAction(t))
	} else {
		t.Lifecycle = protocol.TaskDraft
		t.ImplementGeneration = 0
	}
	d.Task = t
	return d, nil
}

// SetPriority changes the task's priority. Priority affects queueing only;
// running work is never preempted.
func SetPriority(t Task, priority int64, expectedRevision int64) (Decision, error) {
	if err := checkRevision(t, expectedRevision); err != nil {
		return Decision{}, err
	}
	if t.Lifecycle.Terminal() {
		return Decision{}, errTransition(t, "reprioritize")
	}
	if priority == t.Priority {
		return Decision{Task: t}, nil
	}
	t.Priority = priority
	t.ControlRevision++
	return Decision{Task: t, Events: []EventDraft{{Kind: "task.priority_changed", Body: map[string]any{"priority": priority}}}}, nil
}

// SetControlMode switches between managed, paused and human control. Actions
// are still recorded while a task is not managed; dispatch is what stops.
func SetControlMode(t Task, mode protocol.ControlMode, expectedRevision int64) (Decision, error) {
	if err := checkRevision(t, expectedRevision); err != nil {
		return Decision{}, err
	}
	if !mode.Valid() {
		return Decision{}, protocol.Errorf(protocol.CodeInvalidRequest, "unknown control mode %q", mode)
	}
	if t.Lifecycle.Terminal() {
		return Decision{}, errTransition(t, "change control of")
	}
	if mode == t.Control {
		return Decision{Task: t}, nil
	}
	t.Control = mode
	t.ControlRevision++
	return Decision{Task: t, Events: []EventDraft{{Kind: "task.control_changed", Body: map[string]any{"control_mode": mode}}}}, nil
}

// Cancel stops the task for good and cancels its open actions. Runs already
// in flight are stopped separately; cancellation does not claim they halted.
func Cancel(t Task, expectedRevision int64) (Decision, error) {
	if err := checkRevision(t, expectedRevision); err != nil {
		return Decision{}, err
	}
	if t.Lifecycle.Terminal() {
		return Decision{}, errTransition(t, "cancel")
	}
	t.Lifecycle = protocol.TaskCancelled
	t.ControlRevision++
	return Decision{Task: t, CancelOpenActions: true, Events: []EventDraft{{Kind: "task.cancelled"}}}, nil
}
