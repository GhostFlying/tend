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
	ForceMaintenance       bool
	ServerOnly             bool
	ServerUpdaterSupported bool
	ReleaseSet             *release.Set
	ReleaseSetSHA256       string
	ReleaseSetSize         int64
	ExecutionRequestID     string
	ParkIdleManagers       bool
	ServerCapabilities     []string
	DeliveryTaskID         string
	DeliveryDutyRevision   int64
	ArtifactSources        map[string]release.SourceIdentity
	RunnerConfigurations   map[string]protocol.RunnerConfiguration
	CurrentServerHash      string
	Manifest               release.Manifest
	ManifestSHA256         string
	ManifestSize           int64
	ServerHash             string
	ServerInstance         string
	DrainMillis            int64
	RestartMillis          int64
	GrantMillis            int64
}

func (StartUpgrade) Operation() string { return "upgrades.start" }
func (StartUpgrade) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalSystem)
}
func (c StartUpgrade) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	if prior, err := store.GetUpgrade(ctx, tx.Q(), x.Env.Target.ID); err == nil {
		if prior.StartRequestID != x.Env.RequestID || prior.ManifestSHA256 != c.ManifestSHA256 || prior.ServerHash != c.ServerHash || prior.ReleaseSetSHA256 != c.ReleaseSetSHA256 || prior.DeliveryTaskID != c.DeliveryTaskID || prior.DeliveryDutyRevision != c.DeliveryDutyRevision || prior.View.ServerOnly != c.ServerOnly || prior.View.ForceMaintenance != c.ForceMaintenance {
			return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "upgrade identity belongs to another admitted installation")
		}
		return Outcome{NoEffect: true, Result: prior.View}, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return Outcome{}, err
	}
	if err := checkUpgradeExecution(ctx, tx.Q(), c.ExecutionRequestID); err != nil {
		return Outcome{}, err
	}
	if prepared, err := archivePrepared(ctx, tx.Q()); err != nil {
		return Outcome{}, err
	} else if prepared {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "cancel archive preparation before starting an upgrade")
	}
	if err := c.Manifest.Validate(); err != nil || !release.ValidSHA(c.ManifestSHA256) || c.ManifestSize <= 0 || c.ServerInstance == "" || !release.ValidSHA(c.ServerHash) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "invalid verified release")
	}
	if c.ReleaseSet != nil {
		if err := c.ReleaseSet.ValidateMetadata(c.Manifest, c.ManifestSHA256); err != nil || !release.ValidSHA(c.ReleaseSetSHA256) || c.ReleaseSetSize <= 0 || c.ReleaseSetSize > release.MaxManifestBytes {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "invalid complete release set")
		}
	}
	if c.ReleaseSet != nil && c.ReleaseSet.Format >= 2 && !c.ServerUpdaterSupported {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "server updater requires standalone supervisor-first bootstrap")
	}
	if c.ServerOnly && (c.ReleaseSet == nil || c.ReleaseSet.Format != 2 || len(c.RunnerConfigurations) > 0 || !slices.Contains(c.ServerCapabilities, release.ServerCapabilityIndependentServerUpgrade)) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "server-only installation requires a capable complete release and no runner configuration changes")
	}
	if c.ForceMaintenance && (c.ReleaseSet == nil || c.ReleaseSet.Format != 2 || len(c.RunnerConfigurations) > 0 || c.DeliveryTaskID != "" || !slices.Contains(c.ServerCapabilities, release.ServerCapabilityWindowMaintenance)) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "forced maintenance requires a capable complete release and an explicit local upgrade")
	}
	if c.ParkIdleManagers && !slices.Contains(c.ServerCapabilities, release.ServerCapabilityManagerPark) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "candidate does not support manager park")
	}
	if c.Manifest.Protocol.Max < protocol.CurrentRunnerProtocol || c.Manifest.Protocol.Max > protocol.CurrentRunnerProtocol+1 || c.Manifest.Protocol.Min != max(protocol.MinimumRunnerProtocol, c.Manifest.Protocol.Max-1) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "release must support the current installation protocol floor")
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
	approved, err := checkDeliveryUpgradeStart(ctx, x, tx.Q(), c)
	if err != nil {
		return Outcome{}, err
	}
	var schema int64
	if err := tx.Q().QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&schema); err != nil {
		return Outcome{}, err
	}
	if !c.Manifest.Storage.ServerSchema.Contains(schema) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "release cannot read the current server schema")
	}
	if c.DrainMillis == 0 {
		c.DrainMillis = (6 * time.Minute).Milliseconds()
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
	if !slices.Contains(c.ServerCapabilities, release.ServerCapabilityRunnerJournals) {
		var journals bool
		if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM draining_runners UNION ALL SELECT 1 FROM host_runtime WHERE json_extract(runtime_json,'$.journal_id') <> '')`).Scan(&journals); err != nil {
			return Outcome{}, err
		}
		if journals {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "target server cannot route independent runner journals")
		}
	}
	if !c.ForceMaintenance {
		if err := checkDrainingWindow(ctx, tx.Q(), c.Manifest.Protocol); err != nil {
			return Outcome{}, err
		}
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
		_, n, err := store.HostSlotCounts(ctx, tx.Q(), h.HostID)
		if err != nil {
			return Outcome{}, err
		}
		busy[h.HostID] = n
	}
	slices.SortStableFunc(hosts, func(a, b store.HostRow) int { return cmp.Compare(busy[a.HostID], busy[b.HostID]) })
	sourceCommit := ""
	if source, ok := c.ArtifactSources[c.ServerHash]; ok && !source.Modified && validDeliverySHA(source.Commit) {
		sourceCommit = source.Commit
	}
	r := store.UpgradeRow{InitiatingExecutionID: c.ExecutionRequestID, StartRequestID: x.Env.RequestID, View: protocol.UpgradeView{ForceMaintenance: c.ForceMaintenance, ServerOnly: c.ServerOnly, SourceCommit: sourceCommit, UpgradeID: x.Env.Target.ID, ReleaseID: c.Manifest.ReleaseID, Revision: 1, Status: "running", Phase: "server_draining", CreatedAt: x.Now, UpdatedAt: x.Now, OverallDeadlineMillis: x.Now.Add(20 * time.Minute).UnixMilli(), Hosts: []protocol.UpgradeHostView{}}, DeliveryTaskID: c.DeliveryTaskID, DeliveryDutyRevision: c.DeliveryDutyRevision, Manifest: c.Manifest, ManifestSHA256: c.ManifestSHA256, ManifestSize: c.ManifestSize, ServerHash: c.ServerHash, ServerInstance: c.ServerInstance, DrainMillis: c.DrainMillis, RestartMillis: c.RestartMillis, GrantMillis: c.GrantMillis, Deadline: x.Now.UnixMilli() + c.DrainMillis}
	r.Deadline = capUpgradeDeadline(&r, r.Deadline)
	r.ReleaseSet, r.ReleaseSetSHA256, r.ReleaseSetSize = c.ReleaseSet, c.ReleaseSetSHA256, c.ReleaseSetSize
	if needsSelfUpdate(r) {
		r.View.Phase = "server_updater"
	}
	if len(c.RunnerConfigurations) > 0 {
		if len(c.RunnerConfigurations) != 1 || c.ServerHash != c.CurrentServerHash {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "configuration maintenance requires one host and the current server binary")
		}
		if c.Manifest.Storage.ServerSchema.Max != schema || c.Manifest.Protocol.Max != protocol.CurrentRunnerProtocol {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "configuration maintenance must retain installed versions")
		}
		r.View.Phase = "runners"
		if needsSelfUpdate(r) {
			r.SupervisorThenRunners = true
			r.View.Phase = "server_updater"
		}
	}
	totalBytes := c.ManifestSize
	artifacts := c.Manifest.Artifacts
	if c.ReleaseSet != nil {
		artifacts = c.ReleaseSet.Artifacts
		totalBytes += c.ReleaseSetSize
	}
	for _, artifact := range artifacts {
		totalBytes += artifact.Size
	}
	if c.ForceMaintenance {
		affectedHosts, err := store.FollowupIDs(ctx, tx.Q(), `SELECT DISTINCT host_id FROM draining_runners WHERE json_extract(runtime_json,'$.protocol') NOT BETWEEN ? AND ? ORDER BY host_id`, c.Manifest.Protocol.Min, c.Manifest.Protocol.Max)
		if err != nil {
			return Outcome{}, err
		}
		for _, host := range affectedHosts {
			if _, active := busy[host]; !active {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "inactive host %s must reconcile its incompatible runner", host)
			}
			if err := captureWindowStops(ctx, tx.Q(), &r, host); err != nil {
				return Outcome{}, err
			}
		}
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
		retired, err := store.HostInstanceRetired(ctx, tx.Q(), h.HostID, rt.Runtime.InstanceID)
		if err != nil {
			return Outcome{}, err
		}
		if retired || (c.ForceMaintenance && !c.Manifest.Protocol.Contains(rt.Runtime.Protocol)) {
			phase := "offline_maintenance"
			if !retired {
				phase = "stopping"
				if err := capturePrimaryStop(ctx, tx, &r, h.HostID, rt.Runtime, control); err != nil {
					return Outcome{}, err
				}
			}
			if err := checkWindowRunOwnership(ctx, tx.Q(), r, h.HostID); err != nil {
				return Outcome{}, err
			}
			r.Hosts = append(r.Hosts, store.UpgradeHost{HostID: h.HostID, PrimaryOffline: true, PreviousHash: rt.Runtime.Build.BinarySHA256, MaintenanceHandoff: control.Mode == protocol.RunnerMaintenance})
			r.View.Hosts = append(r.View.Hosts, protocol.UpgradeHostView{HostID: h.HostID, Status: "skipped", Phase: phase, CurrentVersion: rt.Runtime.Build.Version, ProtocolVersion: rt.Runtime.Protocol})
			continue
		}
		if !c.Manifest.Protocol.Contains(rt.Runtime.Protocol) {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "host %s runner %s uses protocol %d outside the target range [%d,%d]", h.HostID, rt.Runtime.InstanceID, rt.Runtime.Protocol, c.Manifest.Protocol.Min, c.Manifest.Protocol.Max)
		}
		if !c.ServerOnly && (rt.Runtime.JournalVersion <= 0 || !c.Manifest.Storage.RunnerJournal.Contains(rt.Runtime.JournalVersion) || !slices.Contains(rt.Runtime.Capabilities, capabilityUpdater)) {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "host %s cannot install the runner release", h.HostID)
		}
		if !rt.Online || rt.LastHeartbeatAt <= x.Now.Add(-RunnerOfflineAfter).UnixMilli() || !upgradeStartAdmission(control, rt.Runtime) || rt.Runtime.AppliedControlRevision != control.Revision || rt.Runtime.ConfirmedCancelledUpgradeID != control.CancelledUpgradeID || h.Slots <= 0 {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "host %s is offline, incompatible, or unavailable for rolling upgrade", h.HostID)
		}
		if control.Mode == protocol.RunnerMaintenance && !slices.Contains(c.ServerCapabilities, release.ServerCapabilityMaintenanceHandoff) {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "candidate server does not support maintenance handoff")
		}
		artifact, ok := releaseArtifact(c.Manifest, "runner", h.OS, h.Arch)
		_, configurationTarget := c.RunnerConfigurations[h.HostID]
		if !c.ServerOnly && needsSelfUpdate(r) && (!r.SupervisorThenRunners || configurationTarget) && !slices.Contains(rt.Runtime.Capabilities, protocol.CapabilityUpdaterSelfUpdate) {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "host %s requires supervisor-first bootstrap", h.HostID)
		}
		if !c.ServerOnly && c.ReleaseSet != nil && !slices.Contains(rt.Runtime.Capabilities, protocol.CapabilityCompleteReleaseSet) {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "host %s requires complete release set bootstrap", h.HostID)
		}
		if !ok && !c.ServerOnly {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "release lacks runner for host %s", h.HostID)
		}

		if err := checkWindowRunOwnership(ctx, tx.Q(), r, h.HostID); err != nil {
			return Outcome{}, err
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
		if c.ServerOnly {
			if !slices.Contains(rt.Runtime.Capabilities, protocol.CapabilityRunnerMaintenance) {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "host %s cannot acknowledge restart coverage", h.HostID)
			}
			r.Hosts = append(r.Hosts, store.UpgradeHost{HostID: h.HostID, PreviousHash: rt.Runtime.Build.BinarySHA256})
			r.View.Hosts = append(r.View.Hosts, protocol.UpgradeHostView{HostID: h.HostID, Status: "skipped", Phase: "retained", CurrentVersion: rt.Runtime.Build.Version, ProtocolVersion: rt.Runtime.Protocol})
			continue
		}
		r.Hosts = append(r.Hosts, store.UpgradeHost{MaintenanceHandoff: control.Mode == protocol.RunnerMaintenance && (len(c.RunnerConfigurations) == 0 || configurationTarget), Configuration: configuration, HostID: h.HostID, PreviousHash: rt.Runtime.Build.BinarySHA256, TargetHash: artifact.SHA256})
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

		}
		if !found {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "configuration host is not active")
		}
	}
	if approved != nil {
		hosts := make([]string, 0, len(r.Hosts))
		for _, host := range r.InstallationHosts() {
			hosts = append(hosts, host.HostID)
			if host.TargetHash != host.PreviousHash {
				source, ok := c.ArtifactSources[host.TargetHash]
				if !ok || source.Modified || source.Commit != r.View.SourceCommit {
					return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "changed runner artifact does not match the approved server source")
				}
			}
		}
		want := slices.Clone(approved.Deployment.HostIDs)
		slices.Sort(hosts)
		slices.Sort(want)
		if !slices.Equal(hosts, want) {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "upgrade host set differs from the approved complete target set")
		}
		if _, err := tx.Exec(ctx, `UPDATE issue_delivery_executions SET upgrade_id=? WHERE task_id=? AND upgrade_id=''`, r.View.UpgradeID, c.DeliveryTaskID); err != nil {
			return Outcome{}, err
		}
	}
	r.ParkIdleManagers = c.ParkIdleManagers
	r.InstallPhase, r.View.Phase = r.View.Phase, "preparing"
	if len(r.View.WindowStops) > 0 {
		r.View.Phase = windowMaintenance
		r.Deadline = r.View.OverallDeadlineMillis
	} else if err := prepareUpgradeHosts(ctx, x, tx, &r); err != nil {
		return Outcome{}, err
	}
	if err := tx.PutUpgrade(ctx, r); err != nil {
		return Outcome{}, err
	}
	notice := fmt.Sprintf("准备升级至 `%s`。先下发并校验发布包，原本正常的主机准备期间继续接单，临时维护主机保持停止接单。", r.View.ReleaseID)
	if len(r.View.WindowStops) > 0 {
		notice = fmt.Sprintf("升级 %s 正等待 %d 个越窗 runner 的退出回执。", r.View.ReleaseID, len(r.View.WindowStops))
	} else if c.ServerOnly {
		notice = fmt.Sprintf("准备单独升级 server 至 `%s`，原 runner 继续执行已领取的任务。", r.View.ReleaseID)
	}
	if len(c.RunnerConfigurations) > 0 {
		notice = "正在向目标主机准备配置更新；准备完成后进入短维护窗口。"
	}
	if err := upgradeNotice(ctx, x, tx, r, "upgrade_started", "", notice); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: r.View, Events: []Event{upgradeEvent(r)}}, nil
}

// Acknowledged standalone maintenance transfers admission responsibility in the
// start transaction. Another upgrade or fixed installation grant is never adopted.
func upgradeStartAdmission(control protocol.RunnerControl, runtime protocol.RunnerRuntime) bool {
	if control.Mode == protocol.RunnerNormal {
		return runtime.Mode == protocol.RunnerNormal
	}
	if control.UpgradeID != "" || control.Upgrade != nil || control.Authorization != nil || control.ReleaseID != "" || runtime.UpgradeID != "" {
		return false
	}
	return control.Mode == protocol.RunnerMaintenance && slices.Contains(runtime.Capabilities, protocol.CapabilityRunnerMaintenance) && (runtime.Mode == protocol.RunnerMaintenance || runtime.Mode == protocol.RunnerReady)
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
	ServerCancellations                                 []string
	Updater                                             *protocol.UpdaterSelfObservation
	UpdaterReceipt                                      *protocol.UpdaterReceipt
	InstanceID, ProcessInstanceID, BinarySHA256, Commit string
	Healthy, MainDrained, Modified                      bool
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
	events, err := recordServerCancellationConfirmations(ctx, x, tx, c.Observation)
	if err != nil {
		return Outcome{}, err
	}
	r, err := store.LatestUpgrade(ctx, tx.Q())
	if errors.Is(err, store.ErrNotFound) {
		recovered, e := recordExternalRecoveries(ctx, x, tx, nil, c.Observation)
		events = append(events, recovered...)
		return Outcome{Events: events, NoEffect: len(events) == 0}, e
	}
	if err != nil {
		return Outcome{}, err
	}
	recovered, err := recordExternalRecoveries(ctx, x, tx, &r, c.Observation)
	if err != nil {
		return Outcome{}, err
	}
	events = append(events, recovered...)
	if r.View.Status != "running" && r.View.Status != "attention" {
		return Outcome{Result: r.View, Events: events, NoEffect: len(events) == 0}, nil
	}
	before, _ := json.Marshal(r)
	now := x.Now.UnixMilli()
	if needsSelfUpdate(r) && selfUpdateMatches(r, r.ServerHash, c.Observation.Updater) && c.Observation.Updater.InstanceID == r.ServerInstance && c.Observation.Updater.SupervisedProcess.SHA256 == c.Observation.BinarySHA256 && c.Observation.Updater.SupervisedProcess.LaunchID == c.Observation.ProcessInstanceID {
		// Receipt reconciliation remains live after the batch's dispatch
		// authority expires. It neither resumes nor renews the handoff.
		r.View.ServerUpdater = c.Observation.Updater
	}
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
	if selfUpdateRejected(r, r.ServerInstance, c.Observation.Updater) {
		r.View.ServerUpdater = c.Observation.Updater
		if r.View.Status == "running" {
			if err := attention("server updater 准备被拒绝；尚未停止或替换监督器，可取消后另开批次"); err != nil {
				return Outcome{}, err
			}
		}
	}
	rolledBack, err := reconcileUpgradeRollbacks(ctx, x, tx, &r, c.Observation)
	if err != nil {
		return Outcome{}, err
	}
	if rolledBack {
		if err := attention(restorationReason(r)); err != nil {
			return Outcome{}, err
		}
	} else if r.View.Status == "attention" || upgradeBatchExpired(r, now) || (r.Deadline > 0 && now >= r.Deadline) {
		if r.View.Status != "attention" {
			reason := "等待当前阶段超时，已暂停后续升级"
			if upgradeBatchExpired(r, now) {
				reason = "升级批次整体期限已到；须对账或取消后另开批次，不续期"
			}
			if err := attention(reason); err != nil {
				return Outcome{}, err
			}
		}
		// Expired stop authority forbids another installation, but does not
		// invalidate evidence from a candidate already bound to this batch.
		if err := reconcilePausedCandidates(ctx, x, tx, &r, c.Observation); err != nil {
			return Outcome{}, err
		}
	} else {
		wasPreparing := r.View.Phase == "preparing"
		prepared, err := advanceUpgradePreparation(ctx, x, tx, &r)
		if err != nil {
			return Outcome{}, err
		}
		parkReady := false
		if prepared && !wasPreparing {
			parkReady, err = advanceManagerParks(ctx, x, tx, &r)
		}
		if err != nil {
			return Outcome{}, err
		}
		if parkReady {
			switch r.View.Phase {
			case "server_updater":
				if selfUpdateMatches(r, r.ServerHash, c.Observation.Updater) && c.Observation.Updater.InstanceID == r.ServerInstance && c.Observation.Updater.SupervisedProcess.SHA256 == c.Observation.BinarySHA256 && c.Observation.Updater.SupervisedProcess.LaunchID == c.Observation.ProcessInstanceID {
					r.View.ServerUpdater = c.Observation.Updater
					r.View.Phase = "server_draining"
					if r.SupervisorThenRunners {
						r.View.Phase = "runners"
					}
					r.Deadline = capUpgradeDeadline(&r, now+r.DrainMillis)
				}
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
						if r.DeliveryTaskID != "" || needsSelfUpdate(r) {
							if c.Observation.ProcessInstanceID == "" {
								return Outcome{}, protocol.Errorf(protocol.CodeUnknownOutcome, "current server identity is required for installation readback")
							}
							r.ServerProcess = c.Observation.ProcessInstanceID
							// Pin verification even when the updater read is unavailable.
							// Paused batches reconcile this phase without dispatching.
							r.View.Phase = "server_checking"
							reason, err := reconcileServerCandidate(ctx, x, tx, &r, c.Observation)
							if err != nil {
								return Outcome{}, err
							}
							if reason != "" {
								if err := attention(reason); err != nil {
									return Outcome{}, err
								}
							}
							break
						}
						r.View.Phase = "runners"
						r.Deadline = capUpgradeDeadline(&r, now+r.DrainMillis)
						break
					}
					r.ServerPreviousHash = c.Observation.BinarySHA256
					for i := range r.Hosts {
						h := &r.Hosts[i]
						if h.PrimaryOffline {
							if err := coverDrainingJournals(ctx, x, tx, &r, h.HostID); err != nil {
								return Outcome{}, err
							}
							continue
						}
						ctl, err := runnerControl(ctx, tx.Q(), h.HostID, now)
						if err != nil {
							return Outcome{}, err
						}
						// A retained host stays owner-controlled throughout preparation.
						// Capture its actual mode when acquiring the restart fence.
						if r.View.ServerOnly && ctl.UpgradeID != r.View.UpgradeID {
							h.MaintenanceHandoff = ctl.Mode == protocol.RunnerMaintenance
						}
						runtime, err := store.GetHostRuntime(ctx, tx.Q(), h.HostID)
						if err != nil {
							return Outcome{}, err
						}
						grant, err := issueJournalCoverage(ctx, x, tx, &r, h.HostID, runtime.Runtime.JournalID)
						if err != nil {
							return Outcome{}, err
						}
						if err := coverDrainingJournals(ctx, x, tx, &r, h.HostID); err != nil {
							return Outcome{}, err
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
					r.Deadline = capUpgradeDeadline(&r, now+r.GrantMillis)
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
				if needsSelfUpdate(r) {
					o := c.Observation
					if !selfUpdateMatches(r, r.ServerHash, o.Updater) || o.Updater.InstanceID != r.ServerInstance || o.Updater.SupervisedProcess.SHA256 != o.BinarySHA256 || o.Updater.SupervisedProcess.LaunchID != o.ProcessInstanceID {
						break
					}
					r.View.ServerUpdater = o.Updater
				}
				if err := advanceRunnerUpgrade(ctx, x, tx, &r, attention); err != nil {
					return Outcome{}, err
				}
			}
		}
	}
	after, _ := json.Marshal(r)
	if string(before) == string(after) {
		return Outcome{Result: r.View, Events: events, NoEffect: len(events) == 0}, nil
	}
	r.View.Revision++
	r.View.UpdatedAt = x.Now
	if err := tx.PutUpgrade(ctx, r); err != nil {
		return Outcome{}, err
	}
	if r.View.Status == "completed" {
		if err := refreshDeliveryBaselineQueries(ctx, x, tx); err != nil {
			return Outcome{}, err
		}
	}
	return Outcome{Result: r.View, Events: append(events, upgradeEvent(r))}, nil
}

func advanceRunnerUpgrade(ctx context.Context, x *Exec, tx *store.Tx, r *store.UpgradeRow, attention func(string) error) error {
	now := x.Now.UnixMilli()

	for i := range r.Hosts {
		h := &r.Hosts[i]
		v := &r.View.Hosts[i]
		if v.Status == "completed" {
			if needsSelfUpdate(*r) {
				rt, err := store.GetHostRuntime(ctx, tx.Q(), h.HostID)
				if err != nil {
					return err
				}
				if !rt.Online || now-rt.LastHeartbeatAt >= RunnerOfflineAfter.Milliseconds() || !runnerSelfMatches(*r, *h, rt.Runtime) {
					return nil
				}
				v.Updater = rt.Runtime.Updater
			}
			if err := reconcileCompletedRunner(ctx, x, tx, r, i); err != nil {
				return err
			}
			continue
		}
		if v.Status == "skipped" {
			if hostNeedsSelfUpdate(*r, *h) {
				rt, err := store.GetHostRuntime(ctx, tx.Q(), h.HostID)
				if err != nil {
					return err
				}
				if !rt.Online || now-rt.LastHeartbeatAt >= RunnerOfflineAfter.Milliseconds() || !runnerSelfMatches(*r, *h, rt.Runtime) {
					return nil
				}
				v.Updater = rt.Runtime.Updater
			}
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
		runs, err := store.CountJournalRuns(ctx, tx.Q(), h.HostID, rt.Runtime.JournalID)
		if err != nil {
			return err
		}
		v.RemainingRuns = runs
		if runnerSelfMatches(*r, *h, rt.Runtime) || selfUpdateRejected(*r, h.HostID, rt.Runtime.Updater) {
			v.Updater = rt.Runtime.Updater
		}
		if needsSelfUpdate(*r) && h.PreviousHash == h.TargetHash && h.Configuration == nil && runnerSelfMatches(*r, *h, rt.Runtime) && installedRunnerMatches(rt.Runtime, r.Manifest) && rt.Online && now-rt.LastHeartbeatAt < RunnerOfflineAfter.Milliseconds() && rt.Runtime.AppliedControlRevision == ctl.Revision {
			if (v.Phase == "ready" || v.Phase == "updater_handoff") && rt.Runtime.Mode == protocol.RunnerReady {
				ctl.Revision++
				ctl.Mode = protocol.RunnerNormal
				ctl.Authorization = nil
				// Restore normal admission and clear its completed batch metadata
				// together. The next actual ACK proves both, without another
				// metadata-only heartbeat before the following host may drain.
				ctl.UpgradeID = ""
				ctl.ReleaseID = ""
				if err := tx.PutHostControl(ctx, h.HostID, ctl, now); err != nil {
					return err
				}
				v.Phase = "updater_only_restored"
				return nil
			}
			if v.Phase == "updater_only_restored" && rt.Runtime.Mode == protocol.RunnerNormal {
				v.Status = "skipped"
				v.Phase = "verified"
				v.CurrentVersion = rt.Runtime.Build.Version
				r.Deadline = capUpgradeDeadline(r, now+r.DrainMillis)
				continue
			}
		}
		if ctl.Upgrade != nil && rt.Runtime.AppliedControlRevision == ctl.Revision && rt.Runtime.UpgradeID == r.View.UpgradeID {
			v.DownloadedBytes = rt.Runtime.DownloadedBytes
			if rt.Runtime.UpgradeError != "" {
				v.Reason = rt.Runtime.UpgradeError
				return attention("主机下载或更新器确认失败")
			}
		}

		if v.Status == "queued" {
			if awaitingServerReconnect(*r, *h, ctl, rt.Runtime) {
				// Server replacement can outlast presence freshness. Wait for
				// this target's restored control ACK under the existing deadline;
				// the old maintenance observation cannot authorize a runner stop.
				v.Phase = "reconnecting"
				return nil
			}
			if !rt.Online || now-rt.LastHeartbeatAt >= RunnerOfflineAfter.Milliseconds() {
				return attention("等待升级的主机离线")
			}
			if h.PreviousHash == h.TargetHash && h.Configuration == nil && !needsSelfUpdate(*r) {
				if !installedRunnerMatches(rt.Runtime, r.Manifest) {
					return attention("当前 runner 的协议或 journal 版本与发布清单不一致")
				}
				if h.MaintenanceHandoff && ctl.UpgradeID == r.View.UpgradeID {
					if v.Phase != "handoff_restored" {
						ctl.Revision++
						ctl.Mode, ctl.Upgrade, ctl.Authorization = protocol.RunnerNormal, nil, nil
						ctl.UpgradeID, ctl.ReleaseID = "", ""
						if err := tx.PutHostControl(ctx, h.HostID, ctl, now); err != nil {
							return err
						}
						v.Phase = "handoff_restored"
						return nil
					}
				}
				if h.MaintenanceHandoff && (rt.Runtime.Mode != protocol.RunnerNormal || ctl.Mode != protocol.RunnerNormal || rt.Runtime.AppliedControlRevision != ctl.Revision) {
					return nil
				}
				v.Status = "skipped"
				v.Phase = "skipped"
				r.Deadline = capUpgradeDeadline(r, now+r.DrainMillis)
				continue
			}
			maintained := h.MaintenanceHandoff && ctl.UpgradeID == r.View.UpgradeID && ctl.Mode == protocol.RunnerMaintenance && (rt.Runtime.Mode == protocol.RunnerMaintenance || rt.Runtime.Mode == protocol.RunnerReady) && rt.Runtime.UpgradeID == r.View.UpgradeID
			if rt.Runtime.AppliedControlRevision != ctl.Revision || !maintained && (ctl.Mode != protocol.RunnerNormal || rt.Runtime.Mode != protocol.RunnerNormal) {
				return nil
			}
			ctl.Revision++
			ctl.UpgradeID = r.View.UpgradeID
			ctl.Upgrade = nil
			phase := "draining"
			if h.Configuration == nil && h.PreviousHash != h.TargetHash && needsSelfUpdate(*r) && ctl.Mode == protocol.RunnerNormal {
				phase = "warming"
				ctl.Upgrade = runnerUpgradeTarget(*r, *h, false)
				ctl.Upgrade.Rolling = true
				ctl.Upgrade.Updater.Authorized = true
			} else {
				ctl.Mode = protocol.RunnerMaintenance
				if rt.Runtime.JournalID != "" && h.Configuration == nil && h.PreviousHash != h.TargetHash && needsSelfUpdate(*r) {
					ctl.Upgrade = runnerUpgradeTarget(*r, *h, false)
					ctl.Upgrade.Rolling = true
					ctl.Upgrade.Updater.Authorized = true
				}
			}
			if ctl.CancelledUpgradeID != r.View.UpgradeID {
				ctl.CancelledUpgradeID = ""
			}
			ctl.ReleaseID = r.View.ReleaseID
			if err := tx.PutHostControl(ctx, h.HostID, ctl, now); err != nil {
				return err
			}
			v.Status = "running"
			v.Phase = phase
			r.Deadline = capUpgradeDeadline(r, now+r.DrainMillis)
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
		if v.Phase == "ready" && ctl.Upgrade != nil && ctl.Upgrade.Updater != nil && !ctl.Upgrade.Updater.Authorized && rt.Runtime.AppliedControlRevision == ctl.Revision && rt.Runtime.UpgradeID == r.View.UpgradeID && rt.Runtime.UpgradePhase == "updater_ready" && rt.Runtime.Mode == protocol.RunnerReady && rt.Runtime.PendingClaims == 0 && rt.Runtime.PendingOperations == 0 && len(rt.Runtime.Blockers) == 0 && runs == 0 {
			ctl.Revision++
			ctl.Upgrade.Updater.Authorized = true
			v.Phase = "updater_handoff"
			r.Deadline = capUpgradeDeadline(r, now+r.DrainMillis)
			if err := tx.PutHostControl(ctx, h.HostID, ctl, now); err != nil {
				return err
			}
			return nil
		}
		if v.Phase == "draining" && ctl.Upgrade == nil && rt.Runtime.Mode == protocol.RunnerReady && rt.Runtime.AppliedControlRevision == ctl.Revision && rt.Runtime.PendingClaims == 0 && rt.Runtime.PendingOperations == 0 && len(rt.Runtime.Blockers) == 0 && runs == 0 {
			ctl.Revision++
			ctl.Upgrade = runnerUpgradeTarget(*r, *h, false)
			if err := tx.PutHostControl(ctx, h.HostID, ctl, now); err != nil {
				return err
			}
			v.Phase = "ready"
		}
		return nil
	}
	{
		for _, h := range r.Hosts {
			ctl, err := runnerControl(ctx, tx.Q(), h.HostID, now)
			if err != nil {
				return err
			}
			if ctl.Mode == protocol.RunnerNormal && ctl.UpgradeID == r.View.UpgradeID {
				ctl.Revision++
				ctl.Upgrade = nil
				ctl.UpgradeID, ctl.ReleaseID = "", ""
				if err := tx.PutHostControl(ctx, h.HostID, ctl, now); err != nil {
					return err
				}
			}
		}
	}
	r.View.Status = "completed"
	r.View.Phase = "completed"
	r.Deadline = 0
	message := fmt.Sprintf("升级 `%s` 已完成，所有执行主机恢复服务。", r.View.ReleaseID)
	if r.View.ServerOnly {
		message = fmt.Sprintf("server 升级至 `%s` 已完成，runner 保留原版本。", r.View.ReleaseID)
	}
	for _, h := range r.Hosts {
		if h.PrimaryOffline {
			message = fmt.Sprintf("升级 %s 已完成。被紧急停止的接单实例保持离线。", r.View.ReleaseID)
			break
		}
	}
	return upgradeNotice(ctx, x, tx, *r, "upgrade_completed", "", message)
}

func awaitingServerReconnect(r store.UpgradeRow, h store.UpgradeHost, ctl protocol.RunnerControl, rt protocol.RunnerRuntime) bool {
	if r.ServerCommitted == nil || h.CandidateInstance != "" || h.GrantDeadline != 0 ||
		rt.Build.BinarySHA256 != h.PreviousHash || rt.UpgradeID != r.View.UpgradeID ||
		(rt.Mode != protocol.RunnerMaintenance && rt.Mode != protocol.RunnerReady) ||
		rt.AppliedControlRevision >= ctl.Revision || ctl.Upgrade != nil || ctl.Authorization != nil {
		return false
	}
	return ctl.Mode == protocol.RunnerNormal && ctl.UpgradeID == "" && ctl.ReleaseID == "" ||
		h.MaintenanceHandoff && ctl.Mode == protocol.RunnerMaintenance && ctl.UpgradeID == r.View.UpgradeID && ctl.ReleaseID == r.View.ReleaseID
}

// UpgradePausesMain gates new foreground turns throughout server replacement.
func (a *App) UpgradePausesMain(ctx context.Context) (bool, error) {
	paused := false
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		paused, err = a.mainWorkPaused(ctx, tx.Q())
		return err
	})
	return paused, err
}

// mainWorkPaused shares admission checks with callers already in a transaction.
func (a *App) mainWorkPaused(ctx context.Context, q store.Querier) (bool, error) {
	if fenced, err := a.recoveryStartupFenced(ctx, q); err != nil || fenced {
		return fenced, err
	}
	return upgradePausesMain(ctx, q)
}

func upgradePausesMain(ctx context.Context, q store.Querier) (bool, error) {
	if fenced, err := store.ForwardRecoveryFenced(ctx, q); err != nil || fenced {
		return fenced, err
	}

	if prepared, err := archivePrepared(ctx, q); err != nil || prepared {
		return prepared, err
	}
	r, err := store.LatestUpgrade(ctx, q)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if parked, err := store.ManagerUpgradeParked(ctx, q); err != nil || parked {
		return parked, err
	}
	if r.View.Status == "attention" && (r.ServerProcess == "" || r.ServerRollback != nil) {
		return false, nil
	}
	return (r.View.Status == "running" || r.View.Status == "attention") && r.View.Phase != "runners" && r.View.Phase != "preparing" && r.View.Phase != windowMaintenance, nil
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
		if h.PrimaryOffline || h.CandidateInstance != "" {
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
		if ctl.Upgrade != nil && ctl.Upgrade.Updater != nil && ctl.Upgrade.Updater.Authorized && !runnerSelfMatches(r, h, rt.Runtime) && !selfUpdateRejected(r, h.HostID, rt.Runtime.Updater) {
			// The service is unchanged, but its independent supervisor installer
			// can still be running. Keep its original target until reconciled.
			continue
		}
		if rt.Runtime.Build.BinarySHA256 != h.PreviousHash {
			continue
		}
		ctl.Revision++
		ctl.Mode = protocol.RunnerNormal
		if r.View.ServerOnly && h.MaintenanceHandoff {
			ctl.Mode = protocol.RunnerMaintenance
		}
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
