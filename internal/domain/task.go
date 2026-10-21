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
	// Workspace lists the repositories the task works on.
	Workspace Workspace `json:"workspace"`
	// Requirements constrain the hosts that may run the task.
	Requirements Requirements `json:"requirements,omitzero"`
}

// Normalize returns the spec with defaults filled in. Specs are stored
// normalized.
func (s Spec) Normalize() Spec {
	s.Workspace = s.Workspace.Normalize()
	return s
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
	for _, l := range s.Requirements.Labels {
		if !NamePattern.MatchString(l) {
			return protocol.Errorf(protocol.CodeInvalidRequest, "label %q is malformed", l)
		}
	}
	return s.Workspace.Normalize().Validate()
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
		s.Budget() == o.Budget() && slices.Equal(s.Constraints, o.Constraints) && slices.Equal(s.Acceptance, o.Acceptance) &&
		s.Workspace.Normalize().Equal(o.Workspace.Normalize()) && s.Requirements.Equal(o.Requirements)
}

// Task is the projection of a task that the rules operate on.
type Task struct {
	ID string
	// Project is the project the task belongs to (D52). Which projects
	// exist is the caller's concern.
	Project         string
	Lifecycle       protocol.TaskLifecycle
	Control         protocol.ControlMode
	ControlRevision int64
	SpecVersion     int64
	Rating          protocol.Rating
	Block           protocol.BlockReason
	// ImplementGeneration is the generation of the latest implement action
	// for the current spec version.
	ImplementGeneration int64
	// CurrentArtifactID is the artifact under acceptance for the current
	// spec version, if any.
	CurrentArtifactID string
	// Budget is the implement attempt budget of the current spec version.
	Budget int64
	// WorkspaceAssigned reports that the task's workspace exists on a host.
	// From then on its mounts can only be added to.
	WorkspaceAssigned bool
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
	// Effects maps the options tend carries out itself to what it does
	// (D53). Options that need judgment, such as revising the spec, have
	// none and are left to the main agent.
	Effects map[string]protocol.DecisionEffect
}

// Options of the decisions the rules open.
const (
	optionRetryImplement = "retry the implementation"
	optionRaiseBudget    = "raise the budget"
	optionRetryAccept    = "retry acceptance"
	optionReviseSpec     = "revise the spec"
	optionFixAcceptEnv   = "fix the acceptance environment"
	optionCancel         = "cancel the task"
	optionWaitForHost    = "wait for the host"
	optionStartOver      = "start over on another host"
)

// failedImplementDecision asks how to go on after an implementation could
// not produce an artifact.
func failedImplementDecision(question string) DecisionDraft {
	return DecisionDraft{
		Question: question,
		Options:  []string{optionRetryImplement, optionReviseSpec, optionCancel},
		Effects:  map[string]protocol.DecisionEffect{optionRetryImplement: protocol.EffectImplement, optionCancel: protocol.EffectCancel},
	}
}

// budgetDecision asks how to go on after the implement budget ran out.
func budgetDecision(question string) DecisionDraft {
	return DecisionDraft{
		Question: question,
		Options:  []string{optionRaiseBudget, optionReviseSpec, optionCancel},
		// Requesting another generation raises the budget to it.
		Effects: map[string]protocol.DecisionEffect{optionRaiseBudget: protocol.EffectImplement, optionCancel: protocol.EffectCancel},
	}
}

// acceptanceDecision asks how to go on after acceptance could not reach a
// verdict.
func acceptanceDecision(question string) DecisionDraft {
	return DecisionDraft{
		Question: question,
		Options:  []string{optionRetryAccept, optionFixAcceptEnv, optionCancel},
		Effects:  map[string]protocol.DecisionEffect{optionRetryAccept: protocol.EffectAccept, optionCancel: protocol.EffectCancel},
	}
}

// workspaceDecision asks how to go on while the host holding the task's
// workspace is unavailable (D62).
func workspaceDecision(question string) DecisionDraft {
	return DecisionDraft{
		Question: question,
		Options:  []string{optionWaitForHost, optionStartOver, optionCancel},
		Effects:  map[string]protocol.DecisionEffect{optionStartOver: protocol.EffectStartOver, optionCancel: protocol.EffectCancel},
	}
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
	// AbandonWorkspace asks the caller to give up the task's workspace, so
	// that the next implementation lays out a new one (D62).
	AbandonWorkspace bool
	OpenDecisions    []DecisionDraft
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
func NewTask(id, project string, spec Spec, rating protocol.Rating) (Decision, error) {
	if err := spec.Validate(); err != nil {
		return Decision{}, err
	}
	if err := checkRating(rating); err != nil {
		return Decision{}, err
	}
	t := Task{
		ID:              id,
		Project:         project,
		Lifecycle:       protocol.TaskDraft,
		Control:         protocol.ControlManaged,
		ControlRevision: 1,
		SpecVersion:     1,
		Rating:          rating,
		Block:           protocol.BlockNone,
		Budget:          spec.Budget(),
	}
	d := Decision{Events: []EventDraft{{Kind: "task.created", Body: map[string]any{"spec_version": 1, "rating": rating, "project": project}}}}
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
	if t.WorkspaceAssigned {
		if err := checkPinnedWorkspace(current.Normalize(), spec.Normalize()); err != nil {
			return Decision{}, err
		}
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

// SetRating rates the task. The rating affects queueing only; running work
// is never preempted. A task once rated cannot become unrated.
func SetRating(t Task, rating protocol.Rating, expectedRevision int64) (Decision, error) {
	if err := checkRevision(t, expectedRevision); err != nil {
		return Decision{}, err
	}
	if rating == protocol.RatingNone {
		return Decision{}, protocol.Errorf(protocol.CodeInvalidRequest, "rating must be one of p0, p1, p2 or p3")
	}
	if err := checkRating(rating); err != nil {
		return Decision{}, err
	}
	if rating == t.Rating {
		return Decision{Task: t}, nil
	}
	if t.Lifecycle.Terminal() {
		return Decision{}, errTransition(t, "rate")
	}
	from := t.Rating
	t.Rating = rating
	t.ControlRevision++
	return Decision{Task: t, Events: []EventDraft{{Kind: "task.rating_changed", Body: map[string]any{"rating": rating, "from": from}}}}, nil
}

func checkRating(r protocol.Rating) error {
	if !r.Valid() {
		return protocol.Errorf(protocol.CodeInvalidRequest, "unknown rating %q; use p0, p1, p2 or p3", r)
	}
	return nil
}

// SetProject moves the task to another project. Like the rating, the
// project affects context and queueing only.
func SetProject(t Task, project string, expectedRevision int64) (Decision, error) {
	if err := checkRevision(t, expectedRevision); err != nil {
		return Decision{}, err
	}
	if project == t.Project {
		return Decision{Task: t}, nil
	}
	if t.Lifecycle.Terminal() {
		return Decision{}, errTransition(t, "move")
	}
	from := t.Project
	t.Project = project
	t.ControlRevision++
	return Decision{Task: t, Events: []EventDraft{{Kind: "task.project_changed", Body: map[string]any{"project": project, "from": from}}}}, nil
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
	// A cancelled task waits for nothing, as a completed one.
	t.Block = protocol.BlockNone
	t.ControlRevision++
	return Decision{Task: t, CancelOpenActions: true, Events: []EventDraft{{Kind: "task.cancelled"}}}, nil
}

// RequestImplement asks for another implement generation of the current spec
// version, for example after the owner decided to retry or raise the budget.
// An explicit request may exceed the spec's budget.
//
// A request for a generation that already exists returns that generation's
// action unchanged, so the store reports it as a duplicate effect instead of
// a stale revision; replaying an intent is not a conflict.
func RequestImplement(t Task, generation int64, expectedRevision int64) (Decision, error) {
	if generation >= 1 && generation <= t.ImplementGeneration {
		d := implementAction(t)
		d.Generation = generation
		return Decision{Task: t, Actions: []ActionDraft{d}}, nil
	}
	if err := checkRevision(t, expectedRevision); err != nil {
		return Decision{}, err
	}
	if t.Lifecycle != protocol.TaskQueued && t.Lifecycle != protocol.TaskChangesRequired {
		return Decision{}, errTransition(t, "request another implementation of")
	}
	if generation != t.ImplementGeneration+1 {
		return Decision{}, protocol.Errorf(protocol.CodeInvalidRequest, "next implement generation is %d, request asked for %d", t.ImplementGeneration+1, generation)
	}
	t.ImplementGeneration = generation
	t.ControlRevision++
	t.Block = protocol.BlockNone
	if t.Budget < generation {
		t.Budget = generation
	}
	return Decision{
		Task:    t,
		Actions: []ActionDraft{implementAction(t)},
		Events:  []EventDraft{{Kind: "task.implement_requested", Body: map[string]any{"generation": generation}}},
	}, nil
}

// RequestAcceptance asks for another acceptance of the artifact under
// acceptance after the previous attempt could not reach a verdict, for
// example because its environment was unavailable. latest is the newest
// acceptance generation of that artifact.
//
// As with RequestImplement, asking for an existing generation returns its
// action unchanged so the store reports a duplicate effect.
func RequestAcceptance(t Task, latest, generation, expectedRevision int64) (Decision, error) {
	action := ActionDraft{Kind: protocol.ActionAccept, TaskID: t.ID, SpecVersion: t.SpecVersion, ArtifactID: t.CurrentArtifactID, Generation: generation}
	if t.CurrentArtifactID != "" && generation >= 1 && generation <= latest {
		return Decision{Task: t, Actions: []ActionDraft{action}}, nil
	}
	if err := checkRevision(t, expectedRevision); err != nil {
		return Decision{}, err
	}
	if t.Lifecycle != protocol.TaskAwaitingAcceptance || t.CurrentArtifactID == "" {
		return Decision{}, errTransition(t, "request another acceptance of")
	}
	if t.Block != protocol.BlockAcceptanceEnvUnavailable {
		return Decision{}, protocol.Errorf(protocol.CodeInvalidTransition, "acceptance can only be retried after an attempt could not reach a verdict")
	}
	if generation != latest+1 {
		return Decision{}, protocol.Errorf(protocol.CodeInvalidRequest, "next acceptance generation is %d, request asked for %d", latest+1, generation)
	}
	t.ControlRevision++
	t.Block = protocol.BlockNone
	return Decision{
		Task:    t,
		Actions: []ActionDraft{action},
		Events:  []EventDraft{{Kind: "task.acceptance_requested", Body: map[string]any{"artifact_id": t.CurrentArtifactID, "generation": generation}}},
	}, nil
}
