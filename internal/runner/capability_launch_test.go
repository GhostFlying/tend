package runner

import (
	"context"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

// This adapter deliberately implements only the ordinary execution interface.
type declarationWorkersFixture struct{ starts int }

func (w *declarationWorkersFixture) Start(context.Context, harness.WorkerSpec) (harness.Worker, error) {
	w.starts++
	return nil, nil
}
func declarationAssignment() protocol.Assignment {
	pin := protocol.RunExecutionPin{Identity: protocol.AgentIdentity{ExecutionEnvironmentID: "host", LocalID: "agent"}, AgentRevision: 1, Runtime: protocol.AgentRuntimeReference{RuntimeID: "runtime", Revision: 1}, InventoryRevision: 1, SnapshotID: "snapshot", PublicSHA256: "sha256:" + strings.Repeat("a", 64), Profile: protocol.ExecutionProfile{Model: "model"}, Worker: protocol.WorkerAppServer, BackendID: "git"}
	refs := []protocol.CapabilityReference{{CapabilityID: "image_gen", DefinitionRevision: 1}}
	binding := protocol.RunCapabilityBinding{Pin: pin, Required: refs, RequiredSHA256: protocol.CapabilityRequiredDigest(refs)}
	return protocol.Assignment{Execution: &pin, CapabilityBinding: &binding}
}
func TestCapabilityLaunchUsesFrozenDeclarationAndOrdinaryStart(t *testing.T) {
	r := controlRunner(t)
	as := declarationAssignment()
	w := &declarationWorkersFixture{}
	resolved := ResolvedExecution{Workers: w, DeclaredCapabilities: as.CapabilityBinding.Required}
	start, err := r.capabilityWorkerStart(t.Context(), as, resolved)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := start(t.Context(), harness.WorkerSpec{}); err != nil || w.starts != 1 {
		t.Fatal(err, w.starts)
	}
	// Native evidence and its lifetime never become launch requirements.
	as.CapabilityBinding.Evidence = []protocol.ExecutorCapabilityEvidence{{Reason: "tool_absent", ExpiresAtMillis: 1}}
	if _, err := r.capabilityWorkerStart(t.Context(), as, resolved); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*protocol.Assignment, *ResolvedExecution){
		func(_ *protocol.Assignment, r *ResolvedExecution) { r.DeclaredCapabilities = nil },
		func(a *protocol.Assignment, _ *ResolvedExecution) { a.CapabilityBinding.Pin.AgentRevision++ },
		func(a *protocol.Assignment, _ *ResolvedExecution) { a.CapabilityBinding.RequiredSHA256 = "wrong" },
	} {
		original := declarationAssignment()
		resolution := resolved
		mutate(&original, &resolution)
		if _, err := r.capabilityWorkerStart(t.Context(), original, resolution); err == nil {
			t.Fatal("invalid declaration binding launched")
		}
	}
	if w.starts != 1 {
		t.Fatal("precheck started business", w.starts)
	}
}

type declarationExecutionResolver struct {
	ExecutionResolver
	resolved ResolvedExecution
}

func (r declarationExecutionResolver) Resolve(context.Context, protocol.RunExecutionPin) (ResolvedExecution, error) {
	return r.resolved, nil
}

func TestCapabilityPrecheckPreservesPriorStopProofBeforeAnyStart(t *testing.T) {
	r := controlRunner(t)
	as := declarationAssignment()
	as.RunID, as.TaskID, as.Role, as.Desired = "run_precheck", "task", protocol.RoleImplement, protocol.DesiredRun
	as.Workspace.BackendID = as.Execution.BackendID
	w := &declarationWorkersFixture{}
	r.cfg.Executions = declarationExecutionResolver{resolved: ResolvedExecution{Workers: w, Workspaces: completionWorkspaces{}}}
	ctx := t.Context()
	if err := r.cfg.Journal.AcceptAssignmentFromWorker(ctx, as, "", protocol.WorkerAppServer); err != nil {
		t.Fatal(err)
	}
	if err := r.cfg.Journal.SetPhase(ctx, as.RunID, journal.PhaseStarted); err != nil {
		t.Fatal(err)
	}
	if _, err := r.cfg.Journal.NextAttempt(ctx, as.RunID); err != nil {
		t.Fatal(err)
	}
	if err := r.cfg.Journal.ConfirmStopped(ctx, as.RunID); err != nil {
		t.Fatal(err)
	}
	if err := r.work(ctx, ctx, as, &active{as: as, desired: protocol.DesiredRun}); err == nil {
		t.Fatal("undeclared frozen configuration started")
	}
	run, err := r.cfg.Journal.Run(ctx, as.RunID)
	if err != nil || run.Attempts != 1 || !run.StoppedConfirmed || w.starts != 0 {
		t.Fatalf("precheck cleared prior stop proof: %+v starts=%d err=%v", run, w.starts, err)
	}
}
