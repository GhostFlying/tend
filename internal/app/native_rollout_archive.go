package app

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type nativeRolloutKey struct{}

// ArchiveNativeRollout retains facts owned by an authenticated host; it cannot
// create work or authorize replay, and remains usable for stopped historical Runs.
type ArchiveNativeRollout struct{}

func (ArchiveNativeRollout) Operation() string { return protocol.OpRunnerRolloutArchive }
func (ArchiveNativeRollout) Authorize(ctx context.Context, x *Exec, q store.Querier) error {
	if err := allowKinds(x.Principal, protocol.PrincipalRunner); err != nil {
		return err
	}
	var p protocol.NativeRolloutChunk
	if err := decodePayload(x.Env, &p); err != nil {
		return err
	}
	if x.Env.Target.Type != "run" || x.Env.Target.ID != p.RunID {
		return protocol.Errorf(protocol.CodeInvalidRequest, "native original target differs from its Run")
	}
	run, err := store.GetRun(ctx, q, p.RunID)
	if err != nil {
		return err
	}
	if run.HostID != x.Principal.Subject {
		return protocol.Errorf(protocol.CodeForbidden, "native original requires its own host execution")
	}
	return nil
}
func (ArchiveNativeRollout) Apply(ctx context.Context, _ *Exec, tx *store.Tx) (Outcome, error) {
	prepared, _ := ctx.Value(nativeRolloutKey{}).(*store.PreparedNativeRollout)
	receipt, err := tx.ApplyNativeRollout(ctx, prepared)
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: receipt, ProjectionOnly: true}, nil
}
