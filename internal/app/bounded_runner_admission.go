package app

import (
	"context"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// ReleaseBoundedRunnerAdmission authorizes new allocation separately from
// reconciliation of any original native execution retained by the replacement.
type ReleaseBoundedRunnerAdmission struct{}

func (ReleaseBoundedRunnerAdmission) Operation() string {
	return protocol.OpReleaseBoundedRunnerAdmission
}

func (ReleaseBoundedRunnerAdmission) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner, protocol.PrincipalSystem)
}

func (ReleaseBoundedRunnerAdmission) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.BoundedRunnerAdmissionRelease
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if p.RequestID != x.Env.RequestID || p.RequestID == p.RecoveryAuthorizationID || p.HostID == "" || p.HostID != x.Env.Target.ID || x.Env.Target.Type != "host" || !protocol.ValidID(protocol.PrefixRequest, p.RecoveryAuthorizationID) || p.RunnerGeneration < 1 || strings.TrimSpace(p.Reason) == "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "independent admission release request and exact original recovery required")
	}
	control, err := runnerControl(ctx, tx.Q(), p.HostID, x.Now.UnixMilli())
	if err != nil {
		return Outcome{}, err
	}
	if x.Env.ExpectedControlRevision == nil || *x.Env.ExpectedControlRevision != p.ExpectedControlRevision || control.Revision != p.ExpectedControlRevision {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "runner host control revision changed")
	}
	records, err := boundedRunnerProtectionRecords(ctx, tx.Q(), p.HostID)
	if err != nil {
		return Outcome{}, err
	}
	if len(records) == 0 {
		return Outcome{}, protocol.Errorf(protocol.CodeNotFound, "runner recovery authority does not exist")
	}
	grant := records[len(records)-1].Grant
	if grant.RecoveryAuthorizationID != p.RecoveryAuthorizationID || grant.Plan.Target != p.Successor {
		return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "runner recovery authority or successor changed")
	}
	runtime, err := store.GetHostRuntime(ctx, tx.Q(), p.HostID)
	if err != nil {
		return Outcome{}, err
	}
	rt := runtime.Runtime
	if rt.InstanceID != p.Successor.ProcessInstanceID || rt.Build.BinarySHA256 != p.Successor.ArtifactSHA256 || rt.Generation != p.RunnerGeneration || rt.Generation <= grant.Plan.RunnerGeneration || rt.BoundedRecoveryAuthorizationID != p.RecoveryAuthorizationID || rt.JournalVersion < 17 || !slices.Contains(rt.Capabilities, protocol.CapabilityBoundedRunnerRecovery) {
		return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "exact bounded recovery successor observation required")
	}
	originalJournal := grant.Plan.Journal
	if p.Journal.Device != originalJournal.Device || p.Journal.Inode != originalJournal.Inode || p.Journal.Incarnation != originalJournal.Incarnation || p.Journal.Path != originalJournal.Path || p.Journal.Version != rt.JournalVersion {
		return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "runner journal identity changed")
	}
	// Missing ACKs, offline observations and retained unknown work do not veto
	// this explicit owner action. The receiver rechecks its actual incarnation
	// before lifting only the global admission fence. Per-run fences remain.
	control.Revision++
	control.Mode = protocol.RunnerNormal
	control.BoundedRecoveryRelease = &p
	if err := tx.PutHostControl(ctx, p.HostID, control, x.Now.UnixMilli()); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: control, Events: []Event{{ObjectType: "host", ObjectID: p.HostID, ObjectVersion: control.Revision, Kind: "host.bounded_admission_released", Body: map[string]any{"release": p, "original_work_retained": true, "ready_confirmed": false}}}}, nil
}
