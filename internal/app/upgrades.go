package app

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/store"
)

const capabilityUpdater = "runner-updater-v1"

// StartUpgrade receives an already verified immutable local bundle from the
// local control service. The remotely supplied payload cannot choose binaries.
type StartUpgrade struct {
	RunnerConfigurations map[string]protocol.RunnerConfiguration
	CurrentServerHash    string
	Manifest             release.Manifest
	ManifestSHA256       string
	ManifestSize         int64
	ServerHash           string
	ServerInstance       string
	DrainMillis          int64
	RestartMillis        int64
	GrantMillis          int64
}

func (StartUpgrade) Operation() string { return "upgrades.start" }
func (StartUpgrade) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalSystem)
}
func (c StartUpgrade) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	if err := c.Manifest.Validate(); err != nil || !release.ValidSHA(c.ManifestSHA256) || c.ManifestSize <= 0 || c.ServerInstance == "" || !release.ValidSHA(c.ServerHash) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "invalid verified release")
	}
	if c.Manifest.Protocol.Max < protocol.CurrentRunnerProtocol || c.Manifest.Protocol.Max > protocol.CurrentRunnerProtocol+1 || c.Manifest.Protocol.Min != max(0, c.Manifest.Protocol.Max-1) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "release must support exactly its current and previous protocol")
	}
	if !release.ValidID(x.Env.Target.ID) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "invalid upgrade identity")
	}
	if x.Env.ExpectedControlRevision == nil || *x.Env.ExpectedControlRevision != 0 {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "new upgrade requires revision zero")
	}
	if _, err := store.GetUpgrade(ctx, tx.Q(), x.Env.Target.ID); err == nil {
		return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "upgrade identity already belongs to a historical batch")
	} else if !errors.Is(err, store.ErrNotFound) {
		return Outcome{}, err
	}
	previous, err := store.LatestUpgrade(ctx, tx.Q())
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return Outcome{}, err
	}
	if err == nil && (previous.View.Status == "running" || previous.View.Status == "attention") {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "an unfinished upgrade already exists")
	}
	var schema int64
	if err := tx.Q().QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&schema); err != nil {
		return Outcome{}, err
	}
	if !c.Manifest.Storage.ServerSchema.Contains(schema) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "release cannot read the current server schema")
	}
	if c.DrainMillis == 0 {
		c.DrainMillis = (30 * time.Minute).Milliseconds()
	}
	if c.RestartMillis == 0 {
		c.RestartMillis = (120 * time.Second).Milliseconds()
	}
	if c.GrantMillis == 0 {
		c.GrantMillis = (300 * time.Second).Milliseconds()
	}
	if c.DrainMillis < 1000 || c.RestartMillis < 1000 || c.GrantMillis < 2*c.RestartMillis || c.GrantMillis > (15*time.Minute).Milliseconds() {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "invalid bounded upgrade timeouts")
	}
	retiredRuns, err := store.CountOpenRunsOnRetiredHosts(ctx, tx.Q())
	if err != nil {
		return Outcome{}, err
	}
	if retiredRuns != 0 {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "retired hosts still have unfinished runs that require reconciliation")
	}
	hosts, err := store.ListActiveHosts(ctx, tx.Q())
	if err != nil {
		return Outcome{}, err
	}
	busy := map[string]int64{}
	for _, h := range hosts {
		n, err := store.CountOpenRuns(ctx, tx.Q(), h.HostID)
		if err != nil {
			return Outcome{}, err
		}
		busy[h.HostID] = n
	}
	slices.SortStableFunc(hosts, func(a, b store.HostRow) int { return cmp.Compare(busy[a.HostID], busy[b.HostID]) })
	if len(hosts) < 2 {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "rolling upgrades require another online execution host")
	}
	r := store.UpgradeRow{View: protocol.UpgradeView{UpgradeID: x.Env.Target.ID, ReleaseID: c.Manifest.ReleaseID, Revision: 1, Status: "running", Phase: "server_draining", CreatedAt: x.Now, UpdatedAt: x.Now, Hosts: []protocol.UpgradeHostView{}}, Manifest: c.Manifest, ManifestSHA256: c.ManifestSHA256, ManifestSize: c.ManifestSize, ServerHash: c.ServerHash, ServerInstance: c.ServerInstance, DrainMillis: c.DrainMillis, RestartMillis: c.RestartMillis, GrantMillis: c.GrantMillis, Deadline: x.Now.UnixMilli() + c.DrainMillis}
	if len(c.RunnerConfigurations) > 0 {
		if len(c.RunnerConfigurations) != 1 || c.ServerHash != c.CurrentServerHash {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "configuration maintenance requires one host and the current server binary")
		}
		if c.Manifest.Storage.ServerSchema.Max != schema || c.Manifest.Protocol.Max != protocol.CurrentRunnerProtocol {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "configuration maintenance must retain installed versions")
		}
		r.View.Phase = "runners"
	}
	totalBytes := c.ManifestSize
	for _, artifact := range c.Manifest.Artifacts {
		totalBytes += artifact.Size
	}
	for _, h := range hosts {
		rt, err := store.GetHostRuntime(ctx, tx.Q(), h.HostID)
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				return Outcome{}, err
			}
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "host %s needs the initial updater bootstrap", h.HostID)
		}
		control, err := runnerControl(ctx, tx.Q(), h.HostID, x.Now.UnixMilli())
		if err != nil {
			return Outcome{}, err
		}
		if !rt.Online || rt.LastHeartbeatAt <= x.Now.Add(-RunnerOfflineAfter).UnixMilli() || !c.Manifest.Protocol.Contains(rt.Runtime.Protocol) || rt.Runtime.JournalVersion <= 0 || !c.Manifest.Storage.RunnerJournal.Contains(rt.Runtime.JournalVersion) || !slices.Contains(rt.Runtime.Capabilities, capabilityUpdater) || control.Mode != protocol.RunnerNormal || rt.Runtime.Mode != protocol.RunnerNormal || rt.Runtime.AppliedControlRevision != control.Revision || rt.Runtime.ConfirmedCancelledUpgradeID != control.CancelledUpgradeID || h.Slots <= 0 {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "host %s is offline, incompatible, or unavailable for rolling upgrade", h.HostID)
		}
		artifact, ok := releaseArtifact(c.Manifest, "runner", h.OS, h.Arch)
		if !ok {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "release lacks runner for host %s", h.HostID)
		}
		runs, err := store.ListOpenRunsOnHost(ctx, tx.Q(), h.HostID)
		if err != nil {
			return Outcome{}, err
		}
		for _, run := range runs {
			if !c.Manifest.Protocol.Contains(run.Protocol) {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "unfinished run %s requires a retired protocol", run.RunID)
			}
		}
		var configuration *protocol.RunnerConfiguration
		if len(c.RunnerConfigurations) > 0 {
			if artifact.SHA256 != rt.Runtime.Build.BinarySHA256 || !installedRunnerMatches(rt.Runtime, c.Manifest) {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "configuration maintenance cannot replace binaries")
			}
			if target, ok := c.RunnerConfigurations[h.HostID]; ok {
				if !validRunnerConfiguration(target) || !slices.Contains(rt.Runtime.Capabilities, protocol.CapabilityRunnerConfiguration) {
					return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "host does not support this configuration target")
				}
				configuration = &target
			}
		}
		r.Hosts = append(r.Hosts, store.UpgradeHost{Configuration: configuration, HostID: h.HostID, PreviousHash: rt.Runtime.Build.BinarySHA256, TargetHash: artifact.SHA256})
		r.View.Hosts = append(r.View.Hosts, protocol.UpgradeHostView{HostID: h.HostID, Status: "queued", Phase: "queued", CurrentVersion: rt.Runtime.Build.Version, TargetVersion: c.Manifest.ReleaseID, ProtocolVersion: rt.Runtime.Protocol, TotalBytes: totalBytes})
	}
	if len(c.RunnerConfigurations) > 0 {
		found := false
		for i, h := range r.Hosts {
			if h.Configuration == nil {
				r.View.Hosts[i].Status = "skipped"
				r.View.Hosts[i].Phase = "skipped"
				continue
			}
			found = true
			ctl, err := runnerControl(ctx, tx.Q(), h.HostID, x.Now.UnixMilli())
			if err != nil {
				return Outcome{}, err
			}
			ctl.Revision++
			ctl.Mode = protocol.RunnerMaintenance
			ctl.UpgradeID = r.View.UpgradeID
			ctl.ReleaseID = r.View.ReleaseID
			ctl.Authorization = nil
			ctl.CancelledUpgradeID = ""
			if err := tx.PutHostControl(ctx, h.HostID, ctl, x.Now.UnixMilli()); err != nil {
				return Outcome{}, err
			}
			r.View.Hosts[i].Status = "running"
			r.View.Hosts[i].Phase = "draining"
		}
		if !found {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "configuration host is not active")
		}
	}
	if err := tx.PutUpgrade(ctx, r); err != nil {
		return Outcome{}, err
	}
	notice := fmt.Sprintf("开始升级至 `%s`。先等待主会话完成当前回复，再逐台更新执行主机。", r.View.ReleaseID)
	if len(c.RunnerConfigurations) > 0 {
		notice = "已暂停目标主机的新任务领取；等待当前运行与待确认操作结束后应用配置。"
	}
	if err := upgradeNotice(ctx, x, tx, r, "upgrade_started", "", notice); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: r.View, Events: []Event{upgradeEvent(r)}}, nil
}
func releaseArtifact(m release.Manifest, role, os, arch string) (release.Artifact, bool) {
	for _, a := range m.Artifacts {
		if a.Role == role && a.OS == os && a.Arch == arch {
			return a, true
		}
	}
	return release.Artifact{}, false
}
func upgradeEvent(r store.UpgradeRow) Event {
	return Event{ObjectType: "upgrade", ObjectID: r.View.UpgradeID, ObjectVersion: r.View.Revision, Kind: "upgrade.changed", Body: map[string]any{"phase": r.View.Phase, "status": r.View.Status}}
}
func upgradeNotice(ctx context.Context, x *Exec, tx *store.Tx, r store.UpgradeRow, kind, host, body string) error {
	id := x.IDs.New(protocol.PrefixMessage)
	seq, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: id, Author: "system", Body: body, CreatedAt: x.Now.UnixMilli()})
	if err != nil {
		return err
	}
	if err := tx.PutMessageNotice(ctx, store.MessageNoticeRow{MessageID: id, NoticeType: kind, UpgradeID: r.View.UpgradeID, HostID: host}); err != nil {
		return err
	}
	if err := tx.BindMessage(ctx, id, "upgrade", r.View.UpgradeID, "upgrade"); err != nil {
		return err
	}
	_, err = tx.InsertEvent(ctx, store.EventRow{EventID: x.IDs.New(protocol.PrefixEvent), Source: "app", ObjectType: "message", ObjectID: id, ObjectVersion: seq, Kind: "conversation.message", BodyJSON: `{"author":"system"}`, RequestID: x.Env.RequestID, TraceID: x.TraceID, ActorJSON: `{"kind":"system"}`, CreatedAt: x.Now.UnixMilli()})
	return err
}

// ServerObservation is supplied by the local controller, never by remote peers.
type ServerObservation struct {
	UpdaterReceipt                              *protocol.UpdaterReceipt
	InstanceID, ProcessInstanceID, BinarySHA256 string
	Healthy, MainDrained                        bool
}

// TickUpgrade advances only durable, observed prerequisites. It does no network
// or process IO; updaters reconcile their own external phases separately.
func (a *App) TickUpgrade(ctx context.Context, o ServerObservation) (protocol.UpgradeView, error) {
	env := &protocol.Envelope{RequestID: a.ids.New(protocol.PrefixRequest), Operation: "upgrades.tick", Target: protocol.Target{Type: "upgrade"}, Payload: json.RawMessage(`{}`), Reason: "Reconcile persisted upgrade progress with current service observations."}
	op, err := a.Execute(ctx, protocol.Principal{Kind: protocol.PrincipalSystem}, env, tickUpgrade{Observation: o})
	if err != nil {
		return protocol.UpgradeView{}, err
	}
	if op.Error != nil {
		return protocol.UpgradeView{}, op.Error
	}
	var v protocol.UpgradeView
	if len(op.Result) > 0 {
		err = json.Unmarshal(op.Result, &v)
	}
	return v, err
}

type tickUpgrade struct{ Observation ServerObservation }

func (tickUpgrade) Operation() string { return "upgrades.tick" }
func (tickUpgrade) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalSystem)
}
func (c tickUpgrade) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	r, err := store.LatestUpgrade(ctx, tx.Q())
	if errors.Is(err, store.ErrNotFound) {
		return Outcome{NoEffect: true}, nil
	}
	if err != nil {
		return Outcome{}, err
	}
	if r.View.Status != "running" && r.View.Status != "attention" {
		return Outcome{Result: r.View, NoEffect: true}, nil
	}
	before, _ := json.Marshal(r)
	now := x.Now.UnixMilli()
	attention := func(reason string) error {
		r.View.Status = "attention"
		r.View.Reason = reason
		if err := cancelUncommittedRunners(ctx, x, tx, r); err != nil {
			return err
		}
		if err := restoreUnchangedHosts(ctx, x, tx, r); err != nil {
			return err
		}
		return upgradeNotice(ctx, x, tx, r, "upgrade_attention", "", "升级需要处理："+reason+"。现有任务和工作目录均保留。")
	}
	rolledBack, err := reconcileUpgradeRollbacks(ctx, x, tx, &r, c.Observation)
	if err != nil {
		return Outcome{}, err
	}
	if rolledBack {
		if err := attention(restorationReason(r)); err != nil {
			return Outcome{}, err
		}
	} else if r.View.Status == "attention" || (r.Deadline > 0 && now >= r.Deadline) {
		if r.View.Status != "attention" {
			if err := attention("等待当前阶段超时，已暂停后续升级"); err != nil {
				return Outcome{}, err
			}
		}
		// Expired stop authority forbids another installation, but does not
		// invalidate evidence from a candidate already bound to this batch.
		if err := reconcilePausedCandidates(ctx, x, tx, &r, c.Observation); err != nil {
			return Outcome{}, err
		}
	} else {
		switch r.View.Phase {
		case "server_draining":
			if c.Observation.MainDrained {
				if c.Observation.BinarySHA256 == r.ServerHash {
					matches, err := installedServerMatches(ctx, tx.Q(), r.Manifest)
					if err != nil {
						return Outcome{}, err
					}
					if !matches {
						if err := attention("发布清单与当前 server 的协议或数据库版本不一致"); err != nil {
							return Outcome{}, err
						}
						break
					}
					r.View.Phase = "runners"
					r.Deadline = now + r.DrainMillis
					break
				}
				r.ServerPreviousHash = c.Observation.BinarySHA256
				for _, h := range r.Hosts {
					ctl, err := runnerControl(ctx, tx.Q(), h.HostID, now)
					if err != nil {
						return Outcome{}, err
					}
					runs, err := store.ListOpenRunsOnHost(ctx, tx.Q(), h.HostID)
					if err != nil {
						return Outcome{}, err
					}
					grant := &protocol.MaintenanceAuthorization{ID: x.IDs.New(protocol.PrefixLease), DeadlineMillis: now + r.GrantMillis, RunIDs: []string{}}
					for _, run := range runs {
						lease, err := store.GetLeaseByRun(ctx, tx.Q(), run.RunID)
						if err != nil {
							return Outcome{}, err
						}
						if lease.Status != store.LeaseActive || run.ObservedState == protocol.RunUnknown {
							return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "run %s must reconcile before server restart", run.RunID)
						}
						grant.RunIDs = append(grant.RunIDs, run.RunID)
						if err := tx.SetLease(ctx, lease.LeaseID, store.LeaseActive, store.LeaseActive, max(lease.ExpiresAt, grant.DeadlineMillis), now); err != nil {
							return Outcome{}, err
						}
					}
					ctl.Revision++
					ctl.Mode = protocol.RunnerMaintenance
					ctl.UpgradeID = r.View.UpgradeID
					if ctl.CancelledUpgradeID != r.View.UpgradeID {
						ctl.CancelledUpgradeID = ""
					}
					ctl.ReleaseID = r.View.ReleaseID
					ctl.Authorization = grant
					if err := tx.PutHostControl(ctx, h.HostID, ctl, now); err != nil {
						return Outcome{}, err
					}
				}
				r.View.Phase = "server_ready"
				r.Deadline = now + r.GrantMillis - r.RestartMillis
			}
		case "server_ready", "server_checking":
			reason, err := reconcileServerCandidate(ctx, x, tx, &r, c.Observation)
			if err != nil {
				return Outcome{}, err
			}
			if reason != "" {
				if err := attention(reason); err != nil {
					return Outcome{}, err
				}
			}
		case "runners":
			if err := advanceRunnerUpgrade(ctx, x, tx, &r, attention); err != nil {
				return Outcome{}, err
			}
		}
	}
	after, _ := json.Marshal(r)
	if string(before) == string(after) {
		return Outcome{Result: r.View, NoEffect: true}, nil
	}
	r.View.Revision++
	r.View.UpdatedAt = x.Now
	if err := tx.PutUpgrade(ctx, r); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: r.View, Events: []Event{upgradeEvent(r)}}, nil
}

func advanceRunnerUpgrade(ctx context.Context, x *Exec, tx *store.Tx, r *store.UpgradeRow, attention func(string) error) error {
	now := x.Now.UnixMilli()
	for i := range r.Hosts {
		h := &r.Hosts[i]
		v := &r.View.Hosts[i]
		if v.Status == "completed" {
			if err := reconcileCompletedRunner(ctx, x, tx, r, i); err != nil {
				return err
			}
			continue
		}
		if v.Status == "skipped" {
			continue
		}
		rt, err := store.GetHostRuntime(ctx, tx.Q(), h.HostID)
		if err != nil {
			return err
		}
		ctl, err := runnerControl(ctx, tx.Q(), h.HostID, now)
		if err != nil {
			return err
		}
		runs, err := store.CountOpenRuns(ctx, tx.Q(), h.HostID)
		if err != nil {
			return err
		}
		v.RemainingRuns = runs
		if ctl.Upgrade != nil && rt.Runtime.AppliedControlRevision == ctl.Revision && rt.Runtime.UpgradeID == r.View.UpgradeID {
			v.DownloadedBytes = rt.Runtime.DownloadedBytes
			if rt.Runtime.UpgradeError != "" {
				v.Reason = rt.Runtime.UpgradeError
				return attention("主机下载或更新器确认失败")
			}
		}

		if v.Phase != "restored" {
			available, err := anotherAvailableHost(ctx, tx.Q(), h.HostID, now)
			if err != nil {
				return err
			}
			if !available {
				pending, err := anotherHostAwaitingNormal(ctx, tx.Q(), h.HostID, r.View.UpgradeID, now)
				if err != nil {
					return err
				}
				if pending {
					return nil
				}
				return attention("其他执行主机不可用，已暂停后续升级")
			}
		}
		if v.Status == "queued" {
			if !rt.Online || now-rt.LastHeartbeatAt >= RunnerOfflineAfter.Milliseconds() {
				return attention("等待升级的主机离线")
			}
			if h.PreviousHash == h.TargetHash && h.Configuration == nil {
				if !installedRunnerMatches(rt.Runtime, r.Manifest) {
					return attention("当前 runner 的协议或 journal 版本与发布清单不一致")
				}
				v.Status = "skipped"
				v.Phase = "skipped"
				r.Deadline = now + r.DrainMillis
				continue
			}
			available, err := anotherAvailableHost(ctx, tx.Q(), h.HostID, now)
			if err != nil {
				return err
			}
			if !available {
				return nil
			}
			if ctl.Mode != protocol.RunnerNormal || rt.Runtime.Mode != protocol.RunnerNormal || rt.Runtime.AppliedControlRevision != ctl.Revision {
				return nil
			}
			ctl.Revision++
			ctl.Mode = protocol.RunnerMaintenance
			ctl.UpgradeID = r.View.UpgradeID
			if ctl.CancelledUpgradeID != r.View.UpgradeID {
				ctl.CancelledUpgradeID = ""
			}
			ctl.ReleaseID = r.View.ReleaseID
			if err := tx.PutHostControl(ctx, h.HostID, ctl, now); err != nil {
				return err
			}
			v.Status = "running"
			v.Phase = "draining"
			r.Deadline = now + r.DrainMillis
			return nil
		}
		handled, reason, err := reconcileRunnerCandidate(ctx, x, tx, r, i)
		if err != nil {
			return err
		}
		if reason != "" {
			return attention(reason)
		}
		if handled {
			return nil
		}
		if v.Phase == "draining" && rt.Runtime.Mode == protocol.RunnerReady && rt.Runtime.AppliedControlRevision == ctl.Revision && rt.Runtime.PendingClaims == 0 && rt.Runtime.PendingOperations == 0 && len(rt.Runtime.Blockers) == 0 && runs == 0 {
			ctl.Revision++
			ctl.Upgrade = &protocol.RunnerUpgradeTarget{Configuration: h.Configuration, ManifestPath: "/runner/v1/releases/" + r.View.UpgradeID + "/manifest", ManifestSHA256: r.ManifestSHA256, ManifestSize: r.ManifestSize, ArtifactBasePath: "/runner/v1/releases/" + r.View.UpgradeID + "/artifacts/"}
			if err := tx.PutHostControl(ctx, h.HostID, ctl, now); err != nil {
				return err
			}
			v.Phase = "ready"
		}
		return nil
	}
	r.View.Status = "completed"
	r.View.Phase = "completed"
	r.Deadline = 0
	return upgradeNotice(ctx, x, tx, *r, "upgrade_completed", "", fmt.Sprintf("升级 `%s` 已完成，所有执行主机恢复服务。", r.View.ReleaseID))
}
func anotherAvailableHost(ctx context.Context, q store.Querier, exclude string, now int64) (bool, error) {
	hosts, err := store.ListActiveHosts(ctx, q)
	if err != nil {
		return false, err
	}
	for _, h := range hosts {
		if h.HostID == exclude || h.Slots <= 0 {
			continue
		}
		rt, err := store.GetHostRuntime(ctx, q, h.HostID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			return false, err
		}
		ctl, err := runnerControl(ctx, q, h.HostID, now)
		if err != nil {
			return false, err
		}
		if rt.Online && now-rt.LastHeartbeatAt < RunnerOfflineAfter.Milliseconds() && ctl.Mode == protocol.RunnerNormal && rt.Runtime.Mode == protocol.RunnerNormal && rt.Runtime.AppliedControlRevision == ctl.Revision {
			return true, nil
		}
	}
	return false, nil
}

// anotherHostAwaitingNormal identifies only a bounded acknowledgment gap.
// It never makes the host available to authorize installation or start drain.
func anotherHostAwaitingNormal(ctx context.Context, q store.Querier, exclude, upgradeID string, now int64) (bool, error) {
	hosts, err := store.ListActiveHosts(ctx, q)
	if err != nil {
		return false, err
	}
	for _, h := range hosts {
		if h.HostID == exclude || h.Slots <= 0 {
			continue
		}
		rt, err := store.GetHostRuntime(ctx, q, h.HostID)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return false, err
		}
		if !rt.Online || now-rt.LastHeartbeatAt >= RunnerOfflineAfter.Milliseconds() || rt.Runtime.UpgradeID != upgradeID {
			continue
		}
		ctl, err := runnerControl(ctx, q, h.HostID, now)
		if err != nil {
			return false, err
		}
		if ctl.Mode == protocol.RunnerNormal && ctl.Upgrade == nil && (ctl.UpgradeID == "" || ctl.UpgradeID == upgradeID) && rt.Runtime.AppliedControlRevision < ctl.Revision {
			return true, nil
		}
	}
	return false, nil
}

// UpgradePausesMain gates new foreground turns throughout server replacement.
func (a *App) UpgradePausesMain(ctx context.Context) (bool, error) {
	paused := false
	err := a.store.Read(ctx, func(tx *store.ReadTx) error { var err error; paused, err = upgradePausesMain(ctx, tx.Q()); return err })
	return paused, err
}
func upgradePausesMain(ctx context.Context, q store.Querier) (bool, error) {
	r, err := store.LatestUpgrade(ctx, q)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if r.View.Status == "attention" && (r.ServerProcess == "" || r.ServerRollback != nil) {
		return false, nil
	}
	return (r.View.Status == "running" || r.View.Status == "attention") && r.View.Phase != "runners", nil
}

func (a *App) CurrentUpgrade(ctx context.Context) (protocol.UpgradeView, error) {
	r, err := a.GetUpgrade(ctx, "")
	if errors.Is(err, store.ErrNotFound) {
		return protocol.UpgradeView{}, nil
	}
	return r.View, err
}

// A timed-out drain has not authorized replacement. Restore only those exact
// original processes; uncertain candidates retain their maintenance gate.
func restoreUnchangedHosts(ctx context.Context, x *Exec, tx *store.Tx, r store.UpgradeRow) error {
	if r.View.Phase != "runners" && r.ServerProcess != "" && r.ServerRollback == nil {
		return nil
	}
	for _, h := range r.Hosts {
		if h.CandidateInstance != "" {
			continue
		}
		ctl, err := runnerControl(ctx, tx.Q(), h.HostID, x.Now.UnixMilli())
		if err != nil {
			return err
		}
		if ctl.UpgradeID != r.View.UpgradeID {
			continue
		}
		rt, err := store.GetHostRuntime(ctx, tx.Q(), h.HostID)
		if err != nil {
			return err
		}
		if rt.Runtime.Build.BinarySHA256 != h.PreviousHash {
			continue
		}
		ctl.Revision++
		ctl.Mode = protocol.RunnerNormal
		ctl.Upgrade = nil
		ctl.UpgradeID = ""
		ctl.ReleaseID = ""
		ctl.Authorization = nil
		if err := tx.PutHostControl(ctx, h.HostID, ctl, x.Now.UnixMilli()); err != nil {
			return err
		}
	}
	return nil
}

// The artifact hash proves executable identity; observed formats also prevent
// a mislabeled manifest from publishing false compatibility for the next release.
func installedServerMatches(ctx context.Context, q store.Querier, m release.Manifest) (bool, error) {
	var schema int64
	if err := q.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&schema); err != nil {
		return false, err
	}
	return protocol.CurrentRunnerProtocol == m.Protocol.Max && schema == m.Storage.ServerSchema.Max, nil
}
func installedRunnerMatches(r protocol.RunnerRuntime, m release.Manifest) bool {
	return r.Protocol == m.Protocol.Max && r.ProtocolMax == m.Protocol.Max && r.JournalVersion == m.Storage.RunnerJournal.Max
}
