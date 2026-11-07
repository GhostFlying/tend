package app

import (
	"context"
	"database/sql"
	"errors"
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
	var originalRelease string
	err = tx.Q().QueryRowContext(ctx, `SELECT request_id FROM events WHERE source='app' AND object_type='host' AND object_id=? AND kind='host.bounded_admission_released' AND json_extract(body_json,'$.release.recovery_authorization_id')=? ORDER BY event_seq LIMIT 1`, p.HostID, p.RecoveryAuthorizationID).Scan(&originalRelease)
	if err == nil {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "runner admission was already released; reconcile original release request %s", originalRelease)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Outcome{}, err
	}
	runtime, err := store.GetHostRuntime(ctx, tx.Q(), p.HostID)
	if err != nil {
		return Outcome{}, err
	}
	rt := runtime.Runtime
	if rt.Generation != p.RunnerGeneration || !boundedRunnerRuntimeMatches(grant, rt) {
		return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "exact bounded recovery successor observation required")
	}
	if !boundedRunnerInstallationMatches(grant, rt) {
		return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "committed runner installation receipt required before admission release")
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
