package app

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// runContext loads the run named by the envelope target together with its
// action and task.
type runContext struct {
	run    store.RunRow
	action store.ActionRow
	task   store.TaskRow
	spec   domain.Spec
}

func loadRun(ctx context.Context, q store.Querier, runID string) (runContext, error) {
	run, err := store.GetRun(ctx, q, runID)
	if err != nil {
		return runContext{}, errNotFound(err, "run %s does not exist", runID)
	}
	action, err := store.GetAction(ctx, q, run.ActionID)
	if err != nil {
		return runContext{}, err
	}
	task, spec, err := loadTask(ctx, q, run.TaskID)
	if err != nil {
		return runContext{}, err
	}
	return runContext{run: run, action: action, task: task, spec: spec}, nil
}

// fact describes the run as the rules see it. Spec version and generation
// come from the run's action, never from the caller.
func (rc runContext) fact() domain.RunFact {
	return domain.RunFact{RunID: rc.run.RunID, Role: rc.run.Role, SpecVersion: rc.action.TargetSpecVersion, Generation: rc.run.Generation}
}

// authorizeRunSelf allows a run to act only on itself, with its own role.
func authorizeRunSelf(x *Exec, role protocol.RunRole) error {
	if x.Principal.Kind != protocol.PrincipalRun {
		return protocol.Errorf(protocol.CodeForbidden, "only the run itself may perform this operation")
	}
	if x.Principal.Subject != x.Env.Target.ID {
		return protocol.Errorf(protocol.CodeForbidden, "a run may only act on itself")
	}
	if x.Principal.Role != role {
		return protocol.Errorf(protocol.CodeForbidden, "this operation needs a %s run", role)
	}
	return nil
}

// ReportRunStarted handles runs.report_started.
type ReportRunStarted struct{}

// Operation implements Command.
func (ReportRunStarted) Operation() string { return "runs.report_started" }

// Authorize implements Command.
func (ReportRunStarted) Authorize(ctx context.Context, x *Exec, q store.Querier) error {
	switch x.Principal.Kind {
	case protocol.PrincipalRun:
		if x.Principal.Subject != x.Env.Target.ID {
			return protocol.Errorf(protocol.CodeForbidden, "a run may only act on itself")
		}
		return nil
	case protocol.PrincipalRunner:
		run, err := store.GetRun(ctx, q, x.Env.Target.ID)
		if err == nil && run.HostID != x.Principal.Subject {
			return protocol.Errorf(protocol.CodeForbidden, "the run is assigned to another host")
		}
		return nil
	case protocol.PrincipalSystem:
		return nil
	}
	return protocol.Errorf(protocol.CodeForbidden, "%s callers may not report runs", x.Principal.Kind)
}

// Apply implements Command.
func (ReportRunStarted) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	rc, err := loadRun(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, err
	}
	if !domain.RunTransitionAllowed(rc.run.ObservedState, protocol.RunRunning, false) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "run is %s", rc.run.ObservedState)
	}
	if err := tx.UpdateRunObserved(ctx, rc.run.RunID, protocol.RunRunning, x.Now.UnixMilli()); err != nil {
		return Outcome{}, err
	}
	d := domain.RunStarted(toDomain(rc.task, rc.spec.Budget()), rc.fact())
	_, events, err := applyDecision(ctx, x, tx, rc.task, d, nil)
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: newTaskResult(d.Task, nil), Events: events}, nil
}

// SubmitArtifact handles runs.submit_artifact.
type SubmitArtifact struct{}

type submitArtifactPayload struct {
	Repo      string `json:"repo"`
	Branch    string `json:"branch"`
	CommitSHA string `json:"commit_sha"`
}

// Operation implements Command.
func (SubmitArtifact) Operation() string { return "runs.submit_artifact" }

// Authorize implements Command.
func (SubmitArtifact) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return authorizeRunSelf(x, protocol.RoleImplement)
}

// Apply implements Command.
func (SubmitArtifact) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p submitArtifactPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if p.Repo == "" || p.Branch == "" || p.CommitSHA == "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "repo, branch and commit_sha are required")
	}
	rc, err := loadRun(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, err
	}
	ref, err := json.Marshal(p)
	if err != nil {
		return Outcome{}, fmt.Errorf("app: encode artifact ref: %w", err)
	}
	artifactID := x.IDs.New(protocol.PrefixArtifact)
	// The artifact is kept as a fact even when it is stale.
	if err := tx.InsertArtifact(ctx, artifactID, rc.task.TaskID, rc.run.RunID, "git_commit", string(ref), p.CommitSHA, x.Now.UnixMilli()); err != nil {
		return Outcome{}, err
	}
	d := domain.ArtifactSubmitted(toDomain(rc.task, rc.spec.Budget()), rc.fact(), artifactID)
	actions, events, err := applyDecision(ctx, x, tx, rc.task, d, map[string]any{"on": "artifact.submitted", "artifact_id": artifactID})
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: map[string]any{"artifact_id": artifactID, "task": newTaskResult(d.Task, actions)}, Events: events}, nil
}

// SubmitAcceptance handles acceptances.submit.
type SubmitAcceptance struct{}

type submitAcceptancePayload struct {
	Verdict  protocol.Verdict `json:"verdict"`
	Evidence json.RawMessage  `json:"evidence"`
	Issues   json.RawMessage  `json:"issues,omitempty"`
}

// Operation implements Command.
func (SubmitAcceptance) Operation() string { return "acceptances.submit" }

// Authorize implements Command.
func (SubmitAcceptance) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return authorizeRunSelf(x, protocol.RoleAccept)
}

// Apply implements Command. The judged spec version and artifact come from
// the acceptance run's action, so a verdict cannot be pointed at a different
// artifact than the one the run was given.
func (SubmitAcceptance) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p submitAcceptancePayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if !p.Verdict.Valid() {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "unknown verdict %q", p.Verdict)
	}
	rc, err := loadRun(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, err
	}
	if rc.action.Kind != protocol.ActionAccept {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "run %s is not an acceptance run", rc.run.RunID)
	}
	evidence, issues := string(p.Evidence), string(p.Issues)
	if evidence == "" {
		evidence = "{}"
	}
	if issues == "" {
		issues = "[]"
	}
	acceptanceID := x.IDs.New(protocol.PrefixAcceptance)
	if err := tx.InsertAcceptance(ctx, acceptanceID, rc.task.TaskID, rc.run.RunID, rc.action.TargetSpecVersion,
		rc.action.TargetArtifactID, p.Verdict, evidence, issues, x.Now.UnixMilli()); err != nil {
		return Outcome{}, err
	}
	d, err := domain.AcceptanceSubmitted(toDomain(rc.task, rc.spec.Budget()), domain.AcceptanceFact{
		RunID: rc.run.RunID, Role: x.Principal.Role, SpecVersion: rc.action.TargetSpecVersion,
		ArtifactID: rc.action.TargetArtifactID, Verdict: p.Verdict,
	})
	if err != nil {
		return Outcome{}, err
	}
	actions, events, err := applyDecision(ctx, x, tx, rc.task, d, map[string]any{"on": "acceptance.submitted", "acceptance_id": acceptanceID})
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: map[string]any{"acceptance_id": acceptanceID, "task": newTaskResult(d.Task, actions)}, Events: events}, nil
}
