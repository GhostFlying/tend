package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func (a *App) readObservedDeployment(ctx context.Context) (protocol.ObservedDeployment, error) {
	d := protocol.ObservedDeployment{ProofStatus: "unavailable"}
	var err error
	if a.observeDeployment != nil {
		d, err = a.observeDeployment(ctx)
	}
	d.ObservedAt = a.now().UnixMilli()
	return d, err
}

// ReadIssueDeploymentPreparation reads this service's fixed target facts. The
// caller supplies owner authentication; managed callers use the scoped method.
func (a *App) ReadIssueDeploymentPreparation(ctx context.Context, issueID string) (protocol.ObservedDeployment, error) {
	return a.ReadScopedIssueDeploymentPreparation(ctx, "", issueID)
}

// ReadScopedIssueDeploymentPreparation rechecks the manager's root in the same
// snapshot as the returned ledger. Process observation stays outside the read
// transaction and never acquires installation or provider mutation authority.
func (a *App) ReadScopedIssueDeploymentPreparation(ctx context.Context, rootIssueID, issueID string) (protocol.ObservedDeployment, error) {
	if issueID == "" {
		return protocol.ObservedDeployment{}, protocol.Errorf(protocol.CodeInvalidRequest, "issue_id is required")
	}
	check := func(q store.Querier) error {
		if _, err := store.GetCanonicalIssue(ctx, q, issueID); err != nil {
			return errNotFound(err, "Issue does not exist")
		}
		if rootIssueID == "" {
			return nil
		}
		var allowed bool
		if err := q.QueryRowContext(ctx, `WITH RECURSIVE tree(id) AS (SELECT ? UNION ALL SELECT i.issue_id FROM issue_controls i JOIN tree p ON i.parent_issue_id=p.id) SELECT EXISTS(SELECT 1 FROM tree WHERE id=?)`, rootIssueID, issueID).Scan(&allowed); err != nil {
			return err
		}
		if !allowed {
			return protocol.Errorf(protocol.CodeForbidden, "Issue is outside manager read scope")
		}
		return nil
	}
	if err := a.store.Read(ctx, func(tx *store.ReadTx) error { return check(tx.Q()) }); err != nil {
		return protocol.ObservedDeployment{}, err
	}
	d, err := a.readObservedDeployment(ctx)
	if err != nil {
		return protocol.ObservedDeployment{}, err
	}
	err = a.store.Read(ctx, func(tx *store.ReadTx) error {
		q := tx.Q()
		if err := check(q); err != nil {
			return err
		}
		seq, err := store.LatestEventSeq(ctx, q)
		if err != nil {
			return err
		}
		return readDeploymentLedger(ctx, q, &d, []string{issueID}, "", "", a.now().UnixMilli(), seq)
	})
	if err != nil {
		return protocol.ObservedDeployment{}, err
	}
	return d, nil
}

func observedUpgrade(row store.UpgradeRow) *protocol.ObservedUpgrade {
	u := row.View
	v := &protocol.ObservedUpgrade{UpgradeID: u.UpgradeID, ReleaseID: u.ReleaseID, ManifestSHA256: row.ManifestSHA256, Revision: u.Revision, Status: u.Status, Phase: u.Phase, Hosts: []protocol.ObservedUpgradeHost{}}
	if !u.UpdatedAt.IsZero() {
		v.UpdatedAt = u.UpdatedAt.UnixMilli()
	}
	for _, h := range u.Hosts {
		v.Hosts = append(v.Hosts, protocol.ObservedUpgradeHost{HostID: h.HostID, Status: h.Status, Phase: h.Phase})
	}
	return v
}

func observedRunner(ctx context.Context, q store.Querier, host string, now int64) (*protocol.ObservedRunner, *store.HostRuntimeRow, error) {
	runtime, err := store.GetHostRuntime(ctx, q, host)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	control, err := runnerControl(ctx, q, host, now)
	if err != nil {
		return nil, nil, err
	}
	rt := runtime.Runtime
	v := &protocol.ObservedRunner{Source: "server.host_runtime/host_controls", HostID: host, InstanceID: rt.InstanceID, Online: runtime.Online && now-runtime.LastHeartbeatAt < RunnerOfflineAfter.Milliseconds() && now >= runtime.LastHeartbeatAt, LastHeartbeatAt: runtime.LastHeartbeatAt, Mode: rt.Mode, ControlMode: control.Mode, ControlRevision: control.Revision, AppliedControlRevision: rt.AppliedControlRevision, Build: rt.Build, Capabilities: slices.Clone(rt.Capabilities), PendingOperations: &rt.PendingOperations, PendingClaims: &rt.PendingClaims, UpgradeID: rt.UpgradeID, UpgradePhase: rt.UpgradePhase, BoundedRecoveryAuthorizationID: rt.BoundedRecoveryAuthorizationID}
	if release := control.BoundedRecoveryRelease; release != nil {
		v.PublishedRecoveryReleaseID, v.PublishedRecoveryAuthorizationID = release.RequestID, release.RecoveryAuthorizationID
	}
	return v, &runtime, nil
}

// readDeploymentLedger is shared by owner/managed reads and both worker roles.
// Only public fixed-target facts and explicitly allowed Issue deliveries enter it.
func readDeploymentLedger(ctx context.Context, q store.Querier, d *protocol.ObservedDeployment, issueIDs []string, ownHost, excludeTask string, now, seq int64) error {
	archive, err := store.ArchivePreparation(ctx, q)
	if err != nil {
		return err
	}
	fenced, err := store.CommittedMaintenanceFenced(ctx, q)
	if err != nil {
		return err
	}
	d.Admission = &protocol.ObservedServerAdmission{Source: "server.archive_preparation/committed_maintenances", ObservedAt: now, ArchivePrepared: archive.Prepared, ArchiveRevision: archive.Revision, ArchiveUpdatedAt: archive.UpdatedAtMillis, CommittedMaintenanceFence: fenced}
	if ownHost != "" {
		d.Runner, _, err = observedRunner(ctx, q, ownHost, now)
		if err != nil {
			return err
		}
	}
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&d.SchemaVersion); err != nil {
		return err
	}
	latest, err := store.LatestUpgrade(ctx, q)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if err == nil {
		d.LatestUpgrade = observedUpgrade(latest)
	}
	p := &protocol.ObservedDeploymentPreparation{Source: "server.fixed_deployment/ledger", ObservedAt: now, Seq: seq, IssueIDs: append([]string{}, issueIDs...), TargetSource: "server.upgrade_configuration/active_hosts", HostIDs: []string{}, Hosts: []protocol.ObservedDeploymentHost{}, UnsettledUpgrades: []protocol.ObservedUpgrade{}, Deliveries: []protocol.IssueDeliveryPage{}, Blockers: []protocol.ObservedDeploymentBlocker{}, Missing: []string{}, Complete: true}
	if d.Preparation != nil {
		p.ServerInstance = d.Preparation.ServerInstance
	}
	d.Preparation = p
	missing := func(reason string) { p.Complete = false; p.Missing = append(p.Missing, reason) }
	p.TargetComplete = p.ServerInstance != ""
	if !p.TargetComplete {
		missing("configured_server_instance")
	}
	if d.ActualServer == nil {
		missing("actual_server")
	}
	if d.ProofStatus != "confirmed" || d.CommittedProof == nil {
		missing("server_installation_proof")
	}
	if archive.Prepared {
		p.Blockers = append(p.Blockers, protocol.ObservedDeploymentBlocker{Source: d.Admission.Source, Kind: "archive_prepared", Revision: archive.Revision})
	}
	if fenced {
		p.Blockers = append(p.Blockers, protocol.ObservedDeploymentBlocker{Source: "server.committed_maintenances", Kind: "committed_maintenance_unlinked"})
	}
	recovery, err := store.ForwardRecoveryFenced(ctx, q)
	if err != nil {
		return err
	}
	if recovery {
		p.Blockers = append(p.Blockers, protocol.ObservedDeploymentBlocker{Source: "server.forward_recoveries", Kind: "forward_recovery_unlinked"})
	}
	available, err := deliveryTargetAvailable(ctx, q, excludeTask)
	if err != nil {
		return err
	}
	if p.TargetComplete {
		p.DeliveryTargetAvailable = &available
	}
	if !available {
		p.Blockers = append(p.Blockers, protocol.ObservedDeploymentBlocker{Source: "server.delivery_target_available", Kind: "controlled_target_busy"})
	}
	retiredRuns, err := store.CountOpenRunsOnRetiredHosts(ctx, q)
	if err != nil {
		return err
	}
	if retiredRuns > 0 {
		p.Blockers = append(p.Blockers, protocol.ObservedDeploymentBlocker{Source: "server.upgrade_admission/retired_hosts", Kind: "retired_hosts_open_runs", Status: "unfinished", Count: retiredRuns})
	}
	hosts, err := store.ListActiveHosts(ctx, q)
	if err != nil {
		return err
	}
	for _, host := range hosts {
		h := protocol.ObservedDeploymentHost{HostID: host.HostID, OS: host.OS, Arch: host.Arch, Slots: host.Slots, InstallationStatus: "unavailable", InstallationSource: "server.host_runtime/installation_receipts"}
		p.HostIDs = append(p.HostIDs, host.HostID)
		latestGrant, authorization, err := readBoundedRunnerAuthorization(ctx, q, host.HostID, "")
		if err != nil {
			return err
		}
		h.LatestBoundedAuthorization = authorization
		var runtime *store.HostRuntimeRow
		h.Runner, runtime, err = observedRunner(ctx, q, host.HostID, now)
		if err != nil {
			return err
		}
		if h.OpenRuns, err = store.CountOpenRuns(ctx, q, host.HostID); err != nil {
			return err
		}
		if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE host_id=? AND observed_state='unknown'`, host.HostID).Scan(&h.UnknownRuns); err != nil {
			return err
		}
		if runtime != nil {
			h.UpdaterReceipt = runtime.Runtime.UpdaterReceipt
			h.PendingBlockers = int64(len(runtime.Runtime.Blockers))
			h.InstallationStatus, h.InstallationReason, err = observedRunnerInstallation(ctx, q, host.HostID, *runtime)
			if err != nil {
				return err
			}
			if h.InstallationStatus == "confirmed" && !h.Runner.Online {
				h.InstallationStatus, h.InstallationReason = "unknown", "current_runtime_unavailable"
			}
			if authorization != nil && latestGrant != nil && h.InstallationStatus == "confirmed" {
				authorization.CurrentInstallation = latestGrant.Plan.HostID == host.HostID && latestGrant.Plan.Target.InstanceID == host.HostID && boundedRunnerInstallationMatches(*latestGrant, runtime.Runtime)
			}
		}
		if h.InstallationStatus != "confirmed" {
			missing("host_installation:" + host.HostID)
		}
		p.Hosts = append(p.Hosts, h)
	}
	if err := readPreparationUpgrades(ctx, q, p); err != nil {
		return err
	}
	p.Deliveries, err = views.DeploymentPreparationDeliveries(ctx, q, issueIDs)
	if err != nil {
		return err
	}
	for _, page := range p.Deliveries {
		for _, delivery := range page.Items {
			o := delivery.Observation
			if o.CheckedAt == 0 || o.RefreshPending || o.ErrorKind != "" || o.State == "unknown" {
				missing("delivery_observation:" + page.IssueID + ":" + delivery.Delivery.DeliveryID)
			}
		}
	}
	return nil
}

func observedRunnerInstallation(ctx context.Context, q store.Querier, host string, runtime store.HostRuntimeRow) (string, string, error) {
	rt, receipt := runtime.Runtime, runtime.Runtime.UpdaterReceipt
	if receipt == nil {
		return "unconfirmed", "supervisor_receipt_missing", nil
	}
	row, err := store.GetUpgrade(ctx, q, receipt.Binding.UpgradeID)
	if errors.Is(err, store.ErrNotFound) {
		// Bounded takeover appends its own authority rather than inventing an
		// ordinary upgrade row. Read only this runtime's exact host authority;
		// a newer unexecuted grant must not replace current installation proof.
		// Its private plan never enters the projection.
		if rt.BoundedRecoveryAuthorizationID == "" {
			return "unconfirmed", "supervisor_receipt_batch_missing", nil
		}
		grant, _, err := readBoundedRunnerAuthorization(ctx, q, host, rt.BoundedRecoveryAuthorizationID)
		if err != nil {
			return "", "", err
		}
		if grant == nil {
			return "unconfirmed", "supervisor_receipt_batch_missing", nil
		}
		if grant.Plan.HostID == host && grant.Plan.Target.InstanceID == host && boundedRunnerInstallationMatches(*grant, rt) {
			return "confirmed", "", nil
		}
		return "mismatch", "supervisor_receipt_does_not_match_current_bounded_installation", nil
	}
	if err != nil {
		return "", "", err
	}
	for _, h := range row.Hosts {
		if h.HostID == host {
			if committedMatches(row, h, receipt, rt) {
				return "confirmed", "", nil
			}
			return "mismatch", "supervisor_receipt_does_not_match_current_runtime", nil
		}
	}
	return "mismatch", "supervisor_receipt_host_not_in_batch", nil
}

func readBoundedRunnerAuthorization(ctx context.Context, q store.Querier, host, request string) (*protocol.BoundedRunnerMaintenanceGrant, *protocol.ObservedBoundedRunnerAuthorization, error) {
	query := `SELECT json_extract(body_json,'$.grant'),request_id,event_seq FROM events WHERE source='app' AND object_type='host' AND object_id=? AND kind=?`
	args := []any{host, boundedRunnerAuthorizedEvent}
	if request != "" {
		query += ` AND request_id=?`
		args = append(args, request)
	}
	query += ` ORDER BY event_seq DESC LIMIT 1`
	var raw, requestID string
	var seq int64
	err := q.QueryRowContext(ctx, query, args...).Scan(&raw, &requestID, &seq)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var grant protocol.BoundedRunnerMaintenanceGrant
	if err := json.Unmarshal([]byte(raw), &grant); err != nil {
		return nil, nil, err
	}
	summary := &protocol.ObservedBoundedRunnerAuthorization{Source: "server.events/host.bounded_maintenance_authorized", RequestID: requestID, RecoveryAuthorizationID: grant.RecoveryAuthorizationID, ControlRevision: grant.ControlRevision, EventSeq: seq, ExpiresAtMillis: grant.Plan.Authorization.ExpiresAtMillis}
	return &grant, summary, nil
}

func readPreparationUpgrades(ctx context.Context, q store.Querier, p *protocol.ObservedDeploymentPreparation) error {
	var settled string
	err := q.QueryRowContext(ctx, `SELECT upgrade_id FROM upgrades WHERE status IN ('completed','cancelled') ORDER BY created_at DESC,rowid DESC LIMIT 1`).Scan(&settled)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		row, err := store.GetUpgrade(ctx, q, settled)
		if err != nil {
			return err
		}
		p.SettledBaseline = observedUpgrade(row)
	}
	rows, err := q.QueryContext(ctx, `SELECT upgrade_id FROM upgrades WHERE status IN ('running','attention') ORDER BY created_at,upgrade_id LIMIT 101`)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(ids) > 100 {
		p.Complete = false
		p.Missing = append(p.Missing, "unsettled_upgrades_truncated")
		ids = ids[:100]
	}
	for _, id := range ids {
		row, err := store.GetUpgrade(ctx, q, id)
		if err != nil {
			return err
		}
		p.UnsettledUpgrades = append(p.UnsettledUpgrades, *observedUpgrade(row))
	}
	return nil
}
