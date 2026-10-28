package app

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

// WithDeploymentObserver supplies process facts from the server composition root.
// The callback must only observe; it never controls the updater or reads user files.
func WithDeploymentObserver(f func(context.Context) (protocol.ObservedDeployment, error)) Option {
	return func(a *App) { a.observeDeployment = f }
}

func checkObservationScope(ctx context.Context, q store.Querier, scope *protocol.ObservationScope) error {
	if scope == nil {
		return nil
	}
	for _, id := range scope.WorkItemIDs {
		if _, err := store.GetIssue(ctx, q, id); err != nil {
			return errNotFound(err, "observation work item does not exist")
		}
	}
	return nil
}

func observationRun(ctx context.Context, q store.Querier, p protocol.Principal, now int64) (runContext, *protocol.ObservationScope, error) {
	if p.Kind != protocol.PrincipalRun {
		return runContext{}, nil, protocol.Errorf(protocol.CodeForbidden, "run identity is required")
	}
	rc, err := loadRun(ctx, q, p.Subject)
	if err != nil {
		return rc, nil, err
	}
	lease, err := store.GetLeaseByRun(ctx, q, rc.run.RunID)
	if err != nil {
		return rc, nil, err
	}
	// Recovery hello renews authority before the resumed worker's running event
	// is flushed. Read-only facts accept the same live states as reconciliation;
	// the current desired state, spec and unexpired active lease remain mandatory.
	if p.Role != rc.run.Role || !p.Role.Valid() || lease.Status != store.LeaseActive || lease.ExpiresAt <= now || desiredFor(rc.run, rc.action, rc.task) != protocol.DesiredRun || rc.run.DesiredState != protocol.DesiredRun || !slices.Contains(reconcilable, rc.run.ObservedState) || rc.action.TargetSpecVersion != rc.task.SpecVersion {
		return rc, nil, protocol.Errorf(protocol.CodeForbidden, "observation requires a current running task and active lease")
	}
	body, err := store.GetSpec(ctx, q, rc.run.TaskID, rc.action.TargetSpecVersion)
	if err != nil {
		return rc, nil, err
	}
	var spec domain.Spec
	if err := json.Unmarshal([]byte(body), &spec); err != nil {
		return rc, nil, err
	}
	if spec.Observation == nil || !spec.Observation.Deployment && len(spec.Observation.WorkItemIDs) == 0 {
		return rc, nil, protocol.Errorf(protocol.CodeForbidden, "task spec does not authorize observation")
	}
	return rc, spec.Observation, nil
}

// ReadRunObservation returns only the exact spec's authorized execution facts.
// External process observation occurs outside a DB transaction; access is checked
// again in the transaction that captures the ledger and its event watermark.
func (a *App) ReadRunObservation(ctx context.Context, p protocol.Principal) (protocol.RunObservation, error) {
	var scope *protocol.ObservationScope
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		_, scope, err = observationRun(ctx, tx.Q(), p, a.now().UnixMilli())
		return err
	})
	if err != nil {
		return protocol.RunObservation{}, err
	}
	var deployment *protocol.ObservedDeployment
	if scope.Deployment {
		d := protocol.ObservedDeployment{ObservedAt: a.now().UnixMilli(), ProofStatus: "unavailable"}
		if a.observeDeployment != nil {
			d, err = a.observeDeployment(ctx)
			if err != nil {
				return protocol.RunObservation{}, err
			}
			d.ObservedAt = a.now().UnixMilli()
		}
		deployment = &d
	}
	var result protocol.RunObservation
	err = a.store.Read(ctx, func(tx *store.ReadTx) error {
		q := tx.Q()
		now := a.now().UnixMilli()
		rc, allowed, err := observationRun(ctx, q, p, now)
		if err != nil {
			return err
		}
		result = protocol.RunObservation{Version: 1, ObservedAt: now, RunID: rc.run.RunID, TaskID: rc.run.TaskID, SpecVersion: rc.action.TargetSpecVersion, WorkItems: []protocol.ObservedWorkItem{}}
		result.Seq, err = store.LatestEventSeq(ctx, q)
		if err != nil {
			return err
		}
		for _, id := range allowed.WorkItemIDs {
			v, err := views.Issue(ctx, q, id)
			if err != nil {
				return err
			}
			w := protocol.ObservedWorkItem{WorkItemID: id, Revision: v.Revision, Status: v.Status, Column: v.Column, UpdatedAt: v.UpdatedAt, Stages: []protocol.ObservedStage{}, Tasks: []protocol.ObservedTask{}}
			w.Execution = v.Execution
			w.StageExecution = v.StageExecution
			w.FollowOns = []protocol.ObservedFollowOn{}
			for _, f := range v.FollowOns {
				w.FollowOns = append(w.FollowOns, protocol.ObservedFollowOn{FollowOnID: f.FollowOnID, Kind: f.Kind, Status: f.Status})
			}
			for _, s := range v.Stages {
				w.Stages = append(w.Stages, protocol.ObservedStage{StageID: s.StageID, Status: s.Status, TaskIDs: append([]string{}, s.TaskIDs...)})
			}
			for _, t := range v.Tasks {
				task := protocol.ObservedTask{TaskID: t.TaskID, Lifecycle: t.Lifecycle, ControlMode: t.ControlMode, ControlRevision: t.ControlRevision, SpecVersion: t.SpecVersion, BlockReason: t.BlockReason}
				if t.Execution != nil {
					task.Execution = t.Execution.State
					task.RunID = t.Execution.RunID
					task.Role = t.Execution.Role
				}
				runs, err := store.ListRunsByTask(ctx, q, t.TaskID)
				if err != nil {
					return err
				}
				task.Runs = []protocol.ObservedRun{}
				for _, r := range runs {
					task.Runs = append(task.Runs, protocol.ObservedRun{RunID: r.RunID, Role: r.Role, DesiredState: r.DesiredState, ObservedState: r.ObservedState})
				}
				w.Tasks = append(w.Tasks, task)
			}
			result.WorkItems = append(result.WorkItems, w)
		}
		if allowed.Deployment {
			result.Deployment = deployment
			runtime, err := store.GetHostRuntime(ctx, q, rc.run.HostID)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
			if err == nil {
				control, err := runnerControl(ctx, q, rc.run.HostID, now)
				if err != nil {
					return err
				}
				rt := runtime.Runtime
				deployment.Runner = &protocol.ObservedRunner{HostID: rc.run.HostID, InstanceID: rt.InstanceID, Online: runtime.Online && now-runtime.LastHeartbeatAt < RunnerOfflineAfter.Milliseconds() && now >= runtime.LastHeartbeatAt, LastHeartbeatAt: runtime.LastHeartbeatAt, Mode: rt.Mode, ControlMode: control.Mode, ControlRevision: control.Revision, AppliedControlRevision: rt.AppliedControlRevision, Build: rt.Build, Capabilities: append([]string{}, rt.Capabilities...)}
			}

			if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&deployment.SchemaVersion); err != nil {
				return err
			}
			latest, err := store.LatestUpgrade(ctx, q)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
			if err == nil {
				u := latest.View
				upgrade := &protocol.ObservedUpgrade{UpgradeID: u.UpgradeID, ReleaseID: u.ReleaseID, ManifestSHA256: latest.ManifestSHA256, Revision: u.Revision, Status: u.Status, Phase: u.Phase, Hosts: []protocol.ObservedUpgradeHost{}}
				for _, h := range u.Hosts {
					upgrade.Hosts = append(upgrade.Hosts, protocol.ObservedUpgradeHost{HostID: h.HostID, Status: h.Status, Phase: h.Phase})
				}
				deployment.LatestUpgrade = upgrade
			}
		}
		return nil
	})
	return result, err
}
