package app

import (
	"context"
	"encoding/json"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// AuthorizeRunnerUpgrade is requested by the old process through its
// authenticated transport. Only the prepared candidate can use the grant.
type AuthorizeRunnerUpgrade struct{}

func (AuthorizeRunnerUpgrade) Operation() string { return "runner.upgrade_authorize" }
func (AuthorizeRunnerUpgrade) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalRunner)
}
func (AuthorizeRunnerUpgrade) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var b protocol.UpgradeBinding
	if err := decodePayload(x.Env, &b); err != nil {
		return Outcome{}, err
	}
	r, err := store.GetUpgrade(ctx, tx.Q(), b.UpgradeID)
	if err != nil {
		return Outcome{}, err
	}
	if b.InstanceID != x.Principal.Subject || !bindingMatches(r, b) || r.View.Status != "running" || r.View.Phase != "runners" || b.ProcessInstanceID == "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "runner upgrade is not authorized")
	}
	for i := range r.Hosts {
		h := &r.Hosts[i]
		v := &r.View.Hosts[i]
		if h.HostID != b.InstanceID {
			continue
		}
		if b.ArtifactSHA256 != h.TargetHash || (h.CandidateInstance != "" && h.CandidateInstance != b.ProcessInstanceID) {
			return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "candidate identity changed")
		}
		ctl, err := runnerControl(ctx, tx.Q(), h.HostID, x.Now.UnixMilli())
		if err != nil {
			return Outcome{}, err
		}
		if x.Env.ExpectedControlRevision == nil || *x.Env.ExpectedControlRevision != ctl.Revision {
			return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "runner control revision changed")
		}
		rt, err := store.GetHostRuntime(ctx, tx.Q(), h.HostID)
		if err != nil {
			return Outcome{}, err
		}
		n, err := store.CountOpenRuns(ctx, tx.Q(), h.HostID)
		if err != nil {
			return Outcome{}, err
		}
		if h.Configuration != nil && rt.Runtime.ConfigurationSHA256 != h.Configuration.PreviousSHA256 {
			return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "loaded configuration no longer matches maintenance baseline")
		}
		if ctl.UpgradeID != b.UpgradeID || ctl.Mode != protocol.RunnerMaintenance || ctl.Upgrade == nil || rt.Runtime.Mode != protocol.RunnerReady || rt.Runtime.AppliedControlRevision != ctl.Revision || rt.Runtime.PendingClaims != 0 || rt.Runtime.PendingOperations != 0 || len(rt.Runtime.Blockers) != 0 || n != 0 || !rt.Online || x.Now.UnixMilli()-rt.LastHeartbeatAt >= RunnerOfflineAfter.Milliseconds() {
			return Outcome{Result: protocol.UpgradeGrant{Binding: b}, NoEffect: true}, nil
		}
		available, err := anotherAvailableHost(ctx, tx.Q(), h.HostID, x.Now.UnixMilli())
		if err != nil {
			return Outcome{}, err
		}
		if !available {
			return Outcome{Result: protocol.UpgradeGrant{Binding: b}, NoEffect: true}, nil
		}
		if h.GrantDeadline == 0 {
			h.CandidateInstance = b.ProcessInstanceID
			h.GrantDeadline = x.Now.UnixMilli() + r.GrantMillis
			v.Phase = "installing"
			r.View.Revision++
			r.View.UpdatedAt = x.Now
			if err := tx.PutUpgrade(ctx, r); err != nil {
				return Outcome{}, err
			}
		}
		return Outcome{Result: upgradeGrant(b, h.GrantDeadline, x.Now), Events: []Event{upgradeEvent(r)}}, nil
	}
	return Outcome{}, protocol.Errorf(protocol.CodeNotFound, "host is not in the upgrade")
}
func bindingMatches(r store.UpgradeRow, b protocol.UpgradeBinding) bool {
	return r.View.UpgradeID == b.UpgradeID && r.View.ReleaseID == b.ReleaseID && r.ManifestSHA256 == b.ManifestSHA256
}
func upgradeGrant(b protocol.UpgradeBinding, deadline int64, now time.Time) protocol.UpgradeGrant {
	return protocol.UpgradeGrant{Binding: b, Allowed: deadline > now.UnixMilli(), ExpiresAt: time.UnixMilli(deadline).UTC(), RemainingMillis: max(0, deadline-now.UnixMilli())}
}

// RefreshUpgradeGrant bounds replayed request receipts by the original cutoff.
func (a *App) RefreshUpgradeGrant(g protocol.UpgradeGrant) protocol.UpgradeGrant {
	g.RemainingMillis = max(0, g.ExpiresAt.UnixMilli()-a.now().UnixMilli())
	g.Allowed = g.Allowed && g.RemainingMillis > 0
	return g
}

func (a *App) RunnerUpgradeHealth(ctx context.Context, p protocol.Principal, b protocol.UpgradeBinding) (protocol.UpgradeHealth, error) {
	health := protocol.UpgradeHealth{Binding: b}
	if p.Kind != protocol.PrincipalRunner || p.Subject != b.InstanceID {
		return health, protocol.Errorf(protocol.CodeForbidden, "runner may only read its own upgrade")
	}
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		r, err := store.GetUpgrade(ctx, tx.Q(), b.UpgradeID)
		if err != nil {
			return err
		}
		if !bindingMatches(r, b) {
			return protocol.Errorf(protocol.CodeConflictPayload, "release binding changed")
		}
		for i, h := range r.Hosts {
			if h.HostID != b.InstanceID {
				continue
			}
			if h.CandidateInstance != b.ProcessInstanceID || h.TargetHash != b.ArtifactSHA256 {
				return protocol.Errorf(protocol.CodeConflictPayload, "candidate binding changed")
			}
			rt, err := store.GetHostRuntime(ctx, tx.Q(), h.HostID)
			if err != nil {
				return err
			}
			ctl, err := runnerControl(ctx, tx.Q(), h.HostID, a.now().UnixMilli())
			if err != nil {
				return err
			}
			health.ServerRevision = r.View.Revision
			health.ControlRevision = ctl.Revision
			runs, err := store.CountOpenRuns(ctx, tx.Q(), h.HostID)
			if err != nil {
				return err
			}
			health.ServerConfirmed = runnerCandidateMatches(r, h, rt.Runtime) && rt.Runtime.Build.BinarySHA256 == b.ArtifactSHA256 && rt.Online && a.now().UnixMilli()-rt.LastHeartbeatAt < RunnerOfflineAfter.Milliseconds()
			health.AdmissionFenced = health.ServerConfirmed && h.Committed == nil && !committedMatches(r, h, rt.Runtime.UpdaterReceipt, rt.Runtime) && rt.Runtime.InstanceID == h.CandidateInstance && ctl.UpgradeID == r.View.UpgradeID && rt.Runtime.UpgradeID == r.View.UpgradeID && ctl.Mode == protocol.RunnerMaintenance && rt.Runtime.AppliedControlRevision == ctl.Revision
			admitted := h.Committed != nil && ctl.Mode == protocol.RunnerNormal && rt.Runtime.Mode == protocol.RunnerNormal
			health.Healthy = runnerConfigurationMatches(h, rt.Runtime) && health.ServerConfirmed && h.HealthInstance == rt.Runtime.InstanceID && h.Healthy >= 2 && installedRunnerMatches(rt.Runtime, r.Manifest) && rt.Runtime.AppliedControlRevision == ctl.Revision && (admitted || runs == 0 && rt.Runtime.PendingClaims == 0 && rt.Runtime.PendingOperations == 0 && len(rt.Runtime.Blockers) == 0 && rt.Runtime.Mode == protocol.RunnerReady)
			health.HeartbeatSequence = uint64(max(0, rt.Runtime.Sequence))
			// Once claims are enabled, an old journal is never restored, even if no
			// claim was observed yet: a request may already be in flight.
			health.NewWorkAccepted = ctl.Mode == protocol.RunnerNormal || r.View.Hosts[i].Status == "completed"
			health.RollbackSafe = health.AdmissionFenced && !health.NewWorkAccepted && installedRunnerMatches(rt.Runtime, r.Manifest) && runs == 0 && len(rt.Runtime.Blockers) == 0 && rt.Runtime.Mode == protocol.RunnerReady && rt.Runtime.PendingClaims == 0 && rt.Runtime.PendingOperations == 0
			return nil
		}
		return protocol.Errorf(protocol.CodeNotFound, "host is not in upgrade")
	})
	return health, err
}

type authorizeServerUpgrade struct{ Binding protocol.UpgradeBinding }

func (authorizeServerUpgrade) Operation() string { return "upgrades.authorize_server" }
func (authorizeServerUpgrade) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalSystem)
}
func (c authorizeServerUpgrade) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	b := c.Binding
	r, err := store.GetUpgrade(ctx, tx.Q(), b.UpgradeID)
	if err != nil {
		return Outcome{}, err
	}
	if !bindingMatches(r, b) || b.InstanceID != r.ServerInstance || b.ArtifactSHA256 != r.ServerHash || b.ProcessInstanceID == "" || r.View.Status != "running" || r.View.Phase != "server_ready" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "server is not ready for replacement")
	}
	if r.ServerProcess != "" && r.ServerProcess != b.ProcessInstanceID {
		return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "server candidate changed")
	}
	cutoff := x.Now.UnixMilli() + r.GrantMillis
	for _, h := range r.Hosts {
		ctl, err := runnerControl(ctx, tx.Q(), h.HostID, x.Now.UnixMilli())
		if err != nil {
			return Outcome{}, err
		}
		rt, err := store.GetHostRuntime(ctx, tx.Q(), h.HostID)
		if err != nil {
			return Outcome{}, err
		}
		if ctl.Authorization == nil || rt.Runtime.AuthorizationID != ctl.Authorization.ID || rt.Runtime.AppliedControlRevision != ctl.Revision || !rt.Online || x.Now.UnixMilli()-rt.LastHeartbeatAt >= RunnerOfflineAfter.Milliseconds() || ctl.Authorization.DeadlineMillis-x.Now.UnixMilli() < 2*r.RestartMillis {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "runner restart coverage is not confirmed or too short")
		}
		// The runner reports a server-time lower bound for its conservative
		// local deadline, accounting for the full grant response RTT. Reserve
		// the entire restart budget before allowing the old process to stop.
		coveredUntil := min(ctl.Authorization.DeadlineMillis, rt.Runtime.AuthorizationDeadlineMillis)
		cutoff = min(cutoff, coveredUntil-r.RestartMillis)
	}
	if cutoff <= x.Now.UnixMilli() {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "confirmed runner coverage cannot fit the restart budget")
	}
	if r.ServerGrantDeadline == 0 {
		r.ServerGrantDeadline = cutoff
		r.ServerProcess = b.ProcessInstanceID
		r.Deadline = cutoff + r.RestartMillis
		r.View.Revision++
		r.View.UpdatedAt = x.Now
		if err := tx.PutUpgrade(ctx, r); err != nil {
			return Outcome{}, err
		}
	}
	return Outcome{Result: upgradeGrant(b, r.ServerGrantDeadline, x.Now), Events: []Event{upgradeEvent(r)}}, nil
}
func (a *App) ServerUpgradeAuthorize(ctx context.Context, b protocol.UpgradeBinding) (protocol.UpgradeGrant, error) {
	raw, _ := json.Marshal(b)
	// Binding is additionally persisted on the batch, so a lost local receipt
	// cannot renew authorization or authorize a different candidate.
	env := &protocol.Envelope{RequestID: a.ids.New(protocol.PrefixRequest), Operation: "upgrades.authorize_server", Target: protocol.Target{Type: "upgrade", ID: b.UpgradeID}, Payload: raw, Reason: "Authorize the pinned server candidate after confirmed restart coverage."}
	op, err := a.Execute(ctx, protocol.Principal{Kind: protocol.PrincipalSystem}, env, authorizeServerUpgrade{Binding: b})
	if err != nil {
		return protocol.UpgradeGrant{}, err
	}
	if op.Error != nil {
		return protocol.UpgradeGrant{}, op.Error
	}
	var g protocol.UpgradeGrant
	err = json.Unmarshal(op.Result, &g)
	return a.RefreshUpgradeGrant(g), err
}
func (a *App) ServerUpgradeHealth(ctx context.Context, b protocol.UpgradeBinding) (protocol.UpgradeHealth, error) {
	h := protocol.UpgradeHealth{Binding: b}
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		r, err := store.GetUpgrade(ctx, tx.Q(), b.UpgradeID)
		if err != nil {
			return err
		}
		if !bindingMatches(r, b) || r.ServerProcess != b.ProcessInstanceID || r.ServerInstance != b.InstanceID || r.ServerHash != b.ArtifactSHA256 {
			return protocol.Errorf(protocol.CodeConflictPayload, "server candidate binding changed")
		}
		h.ServerRevision = r.View.Revision
		h.ServerConfirmed = r.ServerHealthy > 0
		h.Healthy = r.ServerHealthy >= 2
		h.HeartbeatSequence = uint64(max(0, a.now().UnixMilli()))
		// The new server may acknowledge owner/runner writes immediately. Never
		// claim a pre-upgrade ledger is safe once the candidate has answered.
		h.NewWorkAccepted = h.ServerConfirmed
		h.RollbackSafe = false
		return nil
	})
	return h, err
}

// GetUpgrade returns one historical batch without mutating it.
func (a *App) GetUpgrade(ctx context.Context, id string) (store.UpgradeRow, error) {
	var r store.UpgradeRow
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		if id == "" {
			r, err = store.LatestUpgrade(ctx, tx.Q())
		} else {
			r, err = store.GetUpgrade(ctx, tx.Q(), id)
		}
		return err
	})
	return r, err
}

// ControlUpgrade pauses, retries or cancels a durable release batch. Cancellation
// never treats an installing/unknown process as a proven old runner.
type ControlUpgrade struct{}

func (ControlUpgrade) Operation() string { return "upgrades.control" }
func (ControlUpgrade) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner, protocol.PrincipalSystem)
}
func (ControlUpgrade) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p struct {
		Action string `json:"action"`
	}
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	r, err := store.GetUpgrade(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, err
	}
	if x.Env.ExpectedControlRevision == nil || *x.Env.ExpectedControlRevision != r.View.Revision {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "upgrade revision changed")
	}
	if r.View.Status == "completed" || r.View.Status == "cancelled" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "upgrade is already terminal")
	}
	switch p.Action {
	case "retry":
		if r.ServerRollback != nil {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "cancel this restored batch before starting a new release attempt")
		}
		for _, h := range r.Hosts {
			if h.Rollback != nil {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "cancel this restored batch before starting a new release attempt")
			}
			if h.CandidateInstance != "" && h.Committed == nil {
				ctl, err := runnerControl(ctx, tx.Q(), h.HostID, x.Now.UnixMilli())
				if err != nil {
					return Outcome{}, err
				}
				if ctl.CancelledUpgradeID == r.View.UpgradeID {
					return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "wait for candidate cancellation to reconcile, then cancel this batch before starting a new attempt")
				}
			}
		}
		if r.View.Status != "attention" {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "upgrade is not paused")
		}
		// Fixed grants are never extended by retry. Expired authorization requires
		// explicit reconciliation/cancellation before starting a new batch.
		for i, h := range r.Hosts {
			if r.View.Hosts[i].Status == "completed" || r.View.Hosts[i].Status == "skipped" {
				continue
			}
			if h.GrantDeadline > 0 && h.GrantDeadline <= x.Now.UnixMilli() {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "expired candidate authorization requires reconciliation")
			}
		}
		if r.ServerGrantDeadline > 0 && r.View.Phase != "runners" && r.ServerGrantDeadline <= x.Now.UnixMilli() {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "expired server authorization requires reconciliation")
		}
		if r.ServerProcess == "" && r.View.Phase != "runners" {
			r.View.Phase = "server_draining"
		}
		if r.View.Phase == "runners" {
			for i := range r.Hosts {
				if r.Hosts[i].CandidateInstance == "" && r.View.Hosts[i].Status != "completed" && r.View.Hosts[i].Status != "skipped" {
					r.View.Hosts[i].Status = "queued"
					r.View.Hosts[i].Phase = "queued"
					r.View.Hosts[i].Reason = ""
				}
			}
		}
		r.View.Status = "running"
		r.View.Reason = ""
		r.Deadline = x.Now.UnixMilli() + r.DrainMillis
	case "cancel":
		if r.ServerProcess != "" && r.ServerHealthy < 2 && r.ServerRollback == nil {
			return Outcome{}, protocol.Errorf(protocol.CodeUnknownOutcome, "server replacement must reconcile before cancellation")
		}

		for _, h := range r.Hosts {
			rt, err := store.GetHostRuntime(ctx, tx.Q(), h.HostID)
			if err != nil {
				return Outcome{}, err
			}
			if h.CandidateInstance != "" && h.Rollback == nil && (!committedMatches(r, h, h.Committed, rt.Runtime) || h.HealthInstance != rt.Runtime.InstanceID || h.Healthy < 2 || !rt.Online || x.Now.UnixMilli()-rt.LastHeartbeatAt >= RunnerOfflineAfter.Milliseconds() || !installedRunnerMatches(rt.Runtime, r.Manifest)) {
				return Outcome{}, protocol.Errorf(protocol.CodeUnknownOutcome, "candidate commitment must reconcile before cancellation")
			}
			ctl, err := runnerControl(ctx, tx.Q(), h.HostID, x.Now.UnixMilli())
			if err != nil {
				return Outcome{}, err
			}
			if ctl.UpgradeID != r.View.UpgradeID {
				if ctl.UpgradeID == "" {
					// A prior attention transition may already have released this
					// unchanged host while its local supervisor remains staged.
					ctl.Revision++
					ctl.CancelledUpgradeID = r.View.UpgradeID
					if err := tx.PutHostControl(ctx, h.HostID, ctl, x.Now.UnixMilli()); err != nil {
						return Outcome{}, err
					}
				}
				continue
			}
			if !rt.Online || x.Now.UnixMilli()-rt.LastHeartbeatAt >= RunnerOfflineAfter.Milliseconds() {
				return Outcome{}, protocol.Errorf(protocol.CodeUnknownOutcome, "offline runner must reconcile before cancellation")
			}
			ctl.Revision++
			ctl.Mode = protocol.RunnerNormal
			ctl.Upgrade = nil
			ctl.Authorization = nil
			ctl.UpgradeID = ""
			ctl.ReleaseID = ""
			ctl.CancelledUpgradeID = r.View.UpgradeID
			if err := tx.PutHostControl(ctx, h.HostID, ctl, x.Now.UnixMilli()); err != nil {
				return Outcome{}, err
			}
		}
		r.View.Status = "cancelled"
		r.View.Phase = "cancelled"
		r.Deadline = 0
		if err := upgradeNotice(ctx, x, tx, r, "upgrade_cancelled", "", "本次升级已取消。已完成的版本保留，剩余主机恢复接收任务。"); err != nil {
			return Outcome{}, err
		}
	default:
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "action must be retry or cancel")
	}
	r.View.Revision++
	r.View.UpdatedAt = x.Now
	if err := tx.PutUpgrade(ctx, r); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: r.View, Events: []Event{upgradeEvent(r)}}, nil
}

// ListCancelledServerUpgrades lets the local controller reconcile all durable
// cancellation intents, including a request that arrived after a newer batch.
func (a *App) ListCancelledServerUpgrades(ctx context.Context) ([]string, error) {
	var ids []string
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		ids, err = store.ListCancelledUpgradeIDs(ctx, tx.Q())
		return err
	})
	return ids, err
}
