package runner

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"github.com/GhostFlying/tend/internal/updater"
)

func (r *Runner) rollingRPC(ctx context.Context, operation string, input any, revision *int64, out any) error {
	env, err := r.newEnvelope(operation, protocol.Target{Type: "host", ID: r.cfg.HostID}, input)
	if err != nil {
		return err
	}
	env.ExpectedControlRevision = revision
	op, err := r.sendOnce(ctx, r.c.host(), env)
	if err != nil {
		return err
	}
	return json.Unmarshal(op.Result, out)
}

func (r *Runner) AuthorizeRolling(ctx context.Context, in protocol.RollingAuthorization, journalPath string) (protocol.UpgradeGrant, error) {
	var result protocol.RollingRunnerGrant
	err := r.rollingRPC(ctx, "runner.rolling_authorize", in.Request, &in.Revision, &result)
	if err != nil || result.Allowed || result.PendingCheck == nil || in.PredecessorJournalID == nil || journalPath == "" {
		return result.UpgradeGrant, err
	}
	operations, err := journal.RollingPendingOperations(ctx, journalPath, *in.PredecessorJournalID, in.Request.Binding.UpgradeID, in.Revision, result.PendingCheck.Count)
	if err != nil || operations == nil {
		return result.UpgradeGrant, err
	}
	in.Request.PendingProof = &protocol.RollingPendingProof{
		Sequence:   result.PendingCheck.Sequence,
		JournalID:  *in.PredecessorJournalID,
		Operations: operations,
	}
	result = protocol.RollingRunnerGrant{}
	err = r.rollingRPC(ctx, "runner.rolling_authorize", in.Request, &in.Revision, &result)
	return result.UpgradeGrant, err
}

// BootstrapRolling uses the already selected release and the existing host
// identity without opening the working runner's journal or its tsnet state.
func (r *Runner) BootstrapRolling(ctx context.Context, id, dir string) (updater.State, error) {
	var empty updater.State
	bundle, err := release.Load(dir)
	if err != nil {
		return empty, err
	}
	if bundle.Set == nil || bundle.Set.Format != 2 {
		return empty, errors.New("runner: complete rolling release required")
	}
	state, err := updater.CallSocket(ctx, r.cfg.UpdaterControlSocket, "status", updater.ControlRequest{})
	if err != nil {
		return empty, err
	}
	if state.InstanceID != r.cfg.HostID {
		return empty, errors.New("runner: local supervisor host mismatch")
	}
	if op := state.Operation; op != nil && op.ID == id {
		if op.Rolling == nil || op.Candidate.ManifestSHA256 != bundle.ManifestSHA256 || op.Rolling.Observation.Receipt == nil || op.Rolling.Observation.Receipt.ReleaseSetSHA256 != bundle.SetSHA256 {
			return empty, errors.New("runner: original installation selection differs")
		}
		return state, nil
	}
	if prior, ok := state.Terminals[id]; ok {
		if prior.Binding.ManifestSHA256 != bundle.ManifestSHA256 || prior.RollingSetSHA256 != bundle.SetSHA256 {
			return empty, errors.New("runner: prior handoff selected a different release")
		}
		return state, nil
	}
	var target protocol.RollingTarget
	err = r.rollingRPC(ctx, "runner.rolling_target", protocol.RollingTargetRequest{UpgradeID: id, ManifestSHA256: bundle.ManifestSHA256, ReleaseSetSHA256: bundle.SetSHA256, Predecessor: state.Process}, nil, &target)
	if err != nil {
		return empty, err
	}
	return r.submitRolling(ctx, id, dir, bundle.SetSHA256, target)
}
func (r *Runner) submitRolling(ctx context.Context, id, dir, set string, target protocol.RollingTarget) (updater.State, error) {
	var empty updater.State
	if !target.Updater.Authorized || target.Revision <= 0 {
		return empty, errors.New("runner: rolling supervisor update requires host authority")
	}
	observation, err := updater.EnsureSelfUpdate(ctx, r.cfg.UpdaterControlSocket, updater.SelfUpdateRequest{Target: target.Updater, UpgradeID: id, Release: dir, ReleaseSetSHA256: set})
	if err != nil {
		return empty, err
	}
	launch := updater.RollingLaunch{Observation: observation, Predecessor: observation.SupervisedProcess, Revision: target.Revision}
	return updater.CallSocket(ctx, r.cfg.UpdaterControlSocket, "rolling/launch", updater.ControlRequest{ID: id, Release: dir, Rolling: &launch})
}
