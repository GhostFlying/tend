package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

const RunnerOfflineAfter = 60 * time.Second

// A new runner sends its lowest usable encoding on its first hello. Released
// servers that only know that generation can already decode the request.
func helloProtocolOffer(r *protocol.RunnerRuntime) (int64, bool) {
	if r == nil || r.Protocol != r.ProtocolMin || !slices.Contains(r.Capabilities, protocol.CapabilityProtocolNegotiation) {
		return 0, false
	}
	selected, err := protocol.RuntimeRunnerProtocol(r)
	return selected, err == nil && selected > r.Protocol
}

func validateRuntime(r *protocol.RunnerRuntime) error {
	negotiated, err := protocol.RuntimeRunnerProtocol(r)
	if err != nil || !protocol.RunnerProtocolSupported(negotiated) {
		return protocol.Errorf(protocol.CodeInvalidRequest, "unsupported runner protocol")
	}
	if r == nil {
		return nil
	}
	if r.Updater != nil && !slices.Contains(r.Capabilities, protocol.CapabilityUpdaterSelfUpdate) {
		return protocol.Errorf(protocol.CodeInvalidRequest, "updater observations require negotiated capability")
	}
	if r.ContinuationBackend != "" && (!slices.Contains(r.Capabilities, protocol.CapabilityAgentContinuation) || r.ContinuationBackend != "git" && r.ContinuationBackend != "orca") {
		return protocol.Errorf(protocol.CodeInvalidRequest, "invalid continuation backend")
	}
	if len(r.AgentContinuations) > 0 && !slices.Contains(r.Capabilities, protocol.CapabilityAgentContinuation) {
		return protocol.Errorf(protocol.CodeInvalidRequest, "continuation observations require capability")
	}
	if len(r.AgentExecutors) > 0 && !slices.Contains(r.Capabilities, protocol.CapabilityAgentExecution) {
		return protocol.Errorf(protocol.CodeInvalidRequest, "execution observations require capability")
	}

	if coverage := r.MaintenanceCoverage; coverage != nil {
		if coverage.RunDeadlines == nil {
			return protocol.Errorf(protocol.CodeInvalidRequest, "runner maintenance coverage requires a Run deadline map")
		}
		for id, deadline := range coverage.RunDeadlines {
			if !protocol.ValidID(protocol.PrefixRun, id) || deadline <= 0 {
				return protocol.Errorf(protocol.CodeInvalidRequest, "invalid runner maintenance coverage")
			}
		}
	}
	if negotiated != r.Protocol {
		return protocol.Errorf(protocol.CodeInvalidRequest, "runner protocol does not match negotiation")
	}
	if len(r.JournalID) > 128 || r.JournalID != "" && r.Protocol < 6 || r.InstanceID == "" || len(r.InstanceID) > 128 || r.Sequence < 1 || r.Generation < 1 || r.Protocol < protocol.MinimumRunnerProtocol || r.Protocol > protocol.CurrentRunnerProtocol || r.ProtocolMin > r.Protocol || r.ProtocolMax < r.Protocol || r.ProtocolMin < 0 || r.PendingClaims < 0 || r.PendingOperations < 0 || !slices.Contains([]string{protocol.RunnerNormal, protocol.RunnerMaintenance, protocol.RunnerReady}, r.Mode) {
		return protocol.Errorf(protocol.CodeInvalidRequest, "invalid or unsupported runner runtime")
	}
	return nil
}

// checkRuntime protects both new requests and recorded replies from a retired
// process. Sequence only orders telemetry: a claim replay can be much older.
func checkRuntime(ctx context.Context, q store.Querier, host string, r *protocol.RunnerRuntime, hello bool) error {
	if err := validateRuntime(r); err != nil {
		return err
	}
	old, err := store.GetHostRuntime(ctx, q, host)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if r == nil {
		if err == nil {
			return protocol.Errorf(protocol.CodeConflictPayload, "runner runtime is required after versioned registration")
		}
		return nil
	}
	// Preflight and registration serialize on the same writer. Cover both
	// orderings when an older runner connects while a newer window is admitted.
	if hello && r.Protocol < protocol.CurrentRunnerProtocol {
		var blocked bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM upgrades
			WHERE status IN ('running','attention') AND json_extract(state_json,'$.manifest.protocol.min')>?
			AND NOT EXISTS(SELECT 1 FROM json_each(state_json,'$.view.window_stops') s WHERE phase='window_maintenance'
				AND json_extract(s.value,'$.host_id')=? AND json_extract(s.value,'$.process.launch_id')=?
				AND json_extract(s.value,'$.protocol')=? AND json_extract(s.value,'$.generation')=?
				AND json_extract(s.value,'$.journal_id')=? AND json_extract(s.value,'$.process.pid')=?
				AND json_extract(s.value,'$.process.sha256')=? AND json_extract(s.value,'$.state')!='stopped'))`, r.Protocol, host, r.InstanceID, r.Protocol, r.Generation, r.JournalID, r.Build.PID, r.Build.BinarySHA256).Scan(&blocked); err != nil {
			return err
		}
		if blocked {
			return protocol.Errorf(protocol.CodeInvalidTransition, "runner %s protocol %d is below the admitted release window", r.InstanceID, r.Protocol)
		}
	}
	retired, e := store.HostInstanceRetired(ctx, q, host, r.InstanceID)
	if e != nil {
		return e
	}
	if retired {
		return protocol.Errorf(protocol.CodeConflictPayload, "runner instance has been superseded")
	}
	if err == nil {
		if old.Runtime.InstanceID != r.InstanceID {
			draining, e := store.GetDrainingRunner(ctx, q, host, r.InstanceID)
			switch {
			case e == nil:
				if draining.Runtime.Generation != r.Generation || draining.Runtime.JournalID != r.JournalID || draining.Runtime.Protocol != r.Protocol {
					return protocol.Errorf(protocol.CodeConflictPayload, "draining runner identity changed")
				}
				return nil
			case !errors.Is(e, store.ErrNotFound):
				return e
			}
		}
		if old.Runtime.JournalID != r.JournalID {
			batch, _, err := warmCandidate(ctx, q, host, r)
			if err != nil || batch != nil {
				return err
			}
			if !hello {
				return protocol.Errorf(protocol.CodeConflictPayload, "runner journal is not registered")
			}
			_, err = rollingAdmission(ctx, q, host, old.Runtime, r)
			return err
		}
		if old.Runtime.InstanceID == r.InstanceID && old.Runtime.Generation != r.Generation {
			return protocol.Errorf(protocol.CodeConflictPayload, "runner instance generation changed")
		}
		if hello && old.Runtime.InstanceID != r.InstanceID {
			var pinned bool
			err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM upgrades u JOIN json_each(u.state_json,'$.view.window_stops') s WHERE u.status IN ('running','attention') AND json_extract(s.value,'$.primary')=1 AND json_extract(s.value,'$.host_id')=? AND json_extract(s.value,'$.process.launch_id')=?)`, host, old.Runtime.InstanceID).Scan(&pinned)
			if err != nil {
				return err
			}
			if pinned {
				return protocol.Errorf(protocol.CodeInvalidTransition, "accepting runner is under emergency maintenance")
			}
		}
		if old.Runtime.InstanceID != r.InstanceID && r.Generation <= old.Runtime.Generation {
			return protocol.Errorf(protocol.CodeConflictPayload, "runner process generation is stale")
		}
	}
	if !hello && (err != nil || old.Runtime.InstanceID != r.InstanceID) {
		return protocol.Errorf(protocol.CodeConflictPayload, "runner must reconcile through hello")
	}
	return nil
}

// Only the candidate bound by the supervisor's authorized upgrade may open
// an independent journal. A higher generation alone proves no such handoff.
func rollingAdmission(ctx context.Context, q store.Querier, host string, old protocol.RunnerRuntime, next *protocol.RunnerRuntime) (int64, error) {
	if next.InstanceID != old.InstanceID && next.JournalID == next.InstanceID && next.UpgradeID != "" {
		batch, err := store.GetUpgrade(ctx, q, next.UpgradeID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return 0, err
		}
		if err == nil && batch.View.Status == "running" && batch.View.Phase == "runners" {
			for _, target := range batch.Hosts {
				if target.HostID == host && target.WarmRevision == 0 && target.RollingFrom == old.InstanceID && target.CandidateInstance == next.InstanceID && target.TargetHash == next.Build.BinarySHA256 && target.GrantDeadline > 0 {
					return target.GrantDeadline, nil
				}
			}
		}
	}
	return 0, protocol.Errorf(protocol.CodeConflictPayload, "independent runner journal requires an authorized handoff")
}

func instanceControl(ctx context.Context, q store.Querier, host string, runtime *protocol.RunnerRuntime, now int64) (protocol.RunnerControl, error) {
	if runtime != nil {
		drain, err := store.GetDrainingRunner(ctx, q, host, runtime.InstanceID)
		if err == nil {
			control := drain.Control
			if control.Authorization != nil {
				control.Authorization.RemainingMillis = max(0, control.Authorization.DeadlineMillis-now)
			}
			control.UsageCollectionCommands, err = usageCollectionCommands(ctx, q, host, &drain.Runtime)
			return control, err
		}
		if !errors.Is(err, store.ErrNotFound) {
			return protocol.RunnerControl{}, err
		}
	}
	return runnerControl(ctx, q, host, now)
}

func runnerControl(ctx context.Context, q store.Querier, host string, now int64) (protocol.RunnerControl, error) {
	c, err := store.GetHostControl(ctx, q, host)
	if errors.Is(err, store.ErrNotFound) {
		c = protocol.RunnerControl{Mode: protocol.RunnerNormal}
		err = nil
	}
	if err != nil {
		return c, err
	}
	if c.Authorization != nil {
		c.Authorization.RemainingMillis = max(0, c.Authorization.DeadlineMillis-now)
	}
	primary, err := store.GetHostRuntime(ctx, q, host)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return c, err
	}
	c.UsageCollectionCommands, err = usageCollectionCommands(ctx, q, host, &primary.Runtime)
	if err != nil {
		return c, err
	}
	c.Extensions, err = extensionTarget(ctx, q, host)
	if err != nil {
		return c, err
	}
	return c, nil
}

// observeRuntime is called by hello and heartbeat, never by a retransmitted
// claim. Receiving an old sequence cannot keep a lost process online.
func observeRuntime(ctx context.Context, tx *store.Tx, host string, r *protocol.RunnerRuntime, now int64, hello bool) (bool, error) {
	if err := checkRuntime(ctx, tx.Q(), host, r, hello); err != nil {
		return false, err
	}
	if r == nil {
		return false, nil
	}
	old, err := store.GetHostRuntime(ctx, tx.Q(), host)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return false, err
	}
	fresh := errors.Is(err, store.ErrNotFound)
	if !fresh && old.Runtime.InstanceID != r.InstanceID {
		drain, err := store.GetDrainingRunner(ctx, tx.Q(), host, r.InstanceID)
		if err == nil {
			if drain.Runtime.Sequence >= r.Sequence {
				return false, &protocol.Error{Code: protocol.CodeConflictPayload, Message: "runner observation sequence is stale", Details: json.RawMessage(`{"reason":"stale_sequence"}`)}
			}
			return false, tx.ObserveDrainingRunner(ctx, host, *r, now)
		}
		if !errors.Is(err, store.ErrNotFound) {
			return false, err
		}
	}
	if !fresh && !hello && old.Runtime.Protocol != r.Protocol {
		return false, protocol.Errorf(protocol.CodeConflictPayload, "runner must negotiate a protocol change through hello")
	}
	if !fresh && old.Runtime.InstanceID == r.InstanceID && old.Runtime.Sequence >= r.Sequence {
		return false, &protocol.Error{Code: protocol.CodeConflictPayload, Message: "runner observation sequence is stale", Details: json.RawMessage(`{"reason":"stale_sequence"}`)}
	}
	if !fresh && old.Runtime.InstanceID != r.InstanceID {
		if old.Runtime.JournalID != r.JournalID {
			deadline, err := rollingAdmission(ctx, tx.Q(), host, old.Runtime, r)
			if err != nil {
				return false, err
			}
			if now >= deadline {
				return false, protocol.Errorf(protocol.CodeInvalidTransition, "runner handoff grant expired")
			}
			control, err := store.GetHostControl(ctx, tx.Q(), host)
			if err != nil {
				return false, err
			}
			drain := protocol.RunnerControl{Mode: protocol.RunnerMaintenance, Revision: control.Revision + 1}
			if err := tx.PreserveDrainingRunner(ctx, host, r.UpgradeID, old, drain); err != nil {
				return false, err
			}
		} else if err := tx.RetireHostInstance(ctx, host, old.Runtime.InstanceID); err != nil {
			return false, err
		}
	}
	changed := fresh || !old.Online || old.Runtime.InstanceID != r.InstanceID || old.Runtime.Mode != r.Mode || old.Runtime.AppliedControlRevision != r.AppliedControlRevision || old.Runtime.AuthorizationID != r.AuthorizationID || old.Runtime.UpgradeID != r.UpgradeID || old.Runtime.UpgradePhase != r.UpgradePhase || old.Runtime.UpgradeError != r.UpgradeError || old.Runtime.ConfirmedCancelledUpgradeID != r.ConfirmedCancelledUpgradeID || old.Runtime.DownloadedBytes != r.DownloadedBytes || old.Runtime.Build != r.Build || !slices.Equal(old.Runtime.Repositories, r.Repositories) || !slices.Equal(old.Runtime.Capabilities, r.Capabilities) || !slices.Equal(old.Runtime.AgentExecutors, r.AgentExecutors) || !slices.Equal(old.Runtime.AgentContinuations, r.AgentContinuations) || old.Runtime.ContinuationBackend != r.ContinuationBackend
	if err := tx.PutHostRuntime(ctx, host, store.HostRuntimeRow{Runtime: *r, LastHeartbeatAt: now, Online: true}); err != nil {
		return false, err
	}
	if err := recordRunnerCancellationConfirmation(ctx, tx, host, *r, now); err != nil {
		return false, err
	}
	return changed, nil
}

func checkClaimRuntime(ctx context.Context, x *Exec, q store.Querier) error {
	if err := checkWorker(ctx, x, q); err != nil {
		return err
	}
	var p protocol.ClaimPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return err
	}
	if err := checkRuntime(ctx, q, x.Principal.Subject, p.Runtime, false); err != nil {
		return err
	}

	return checkQuestionRunRecovery(ctx, q, x.Principal.Subject, p.Runtime)
}

// SweepRunnerPresence persists online transitions so all clients see the same
// last observation, including clients reconnecting after the transition.
func (a *App) SweepRunnerPresence(ctx context.Context) (int, error) {
	n := 0
	err := a.write(ctx, func(tx *store.Tx) error {
		rows, err := tx.Q().QueryContext(ctx, `SELECT host_id FROM host_runtime WHERE online=1 AND last_heartbeat_at<=?`, a.now().Add(-RunnerOfflineAfter).UnixMilli())
		if err != nil {
			return fmt.Errorf("app: find silent runners: %w", err)
		}
		var hosts []string
		for rows.Next() {
			var host string
			if err := rows.Scan(&host); err != nil {
				rows.Close()
				return err
			}
			hosts = append(hosts, host)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, host := range hosts {
			if _, err := tx.Exec(ctx, `UPDATE host_runtime SET online=0 WHERE host_id=?`, host); err != nil {
				return err
			}
			if err := a.runtimeEvent(ctx, tx, host, "host.offline"); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	if err == nil && n > 0 {
		a.notifyChange()
	}
	return n, err
}

func (a *App) runtimeEvent(ctx context.Context, tx *store.Tx, host, kind string) error {
	_, err := tx.InsertEvent(ctx, store.EventRow{EventID: a.ids.New(protocol.PrefixEvent), Source: "runner", ObjectType: "host", ObjectID: host, Kind: kind, BodyJSON: "{}", CreatedAt: a.now().UnixMilli()})
	return err
}

// HostMaintenance is a revisioned owner operation. Active work keeps its
// identity and workspace; this gate prevents only new allocations.
type HostMaintenance struct{}

func (HostMaintenance) Operation() string { return "hosts.maintenance" }
func (HostMaintenance) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner, protocol.PrincipalSystem)
}
func (HostMaintenance) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.HostMaintenancePayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	host, err := store.GetHost(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, errNotFound(err, "host does not exist")
	}
	c, err := runnerControl(ctx, tx.Q(), host.HostID, x.Now.UnixMilli())
	if err != nil {
		return Outcome{}, err
	}
	if x.Env.ExpectedControlRevision == nil || *x.Env.ExpectedControlRevision != c.Revision {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "host control revision is %d", c.Revision)
	}
	if !slices.Contains([]string{protocol.RunnerNormal, protocol.RunnerMaintenance}, p.Mode) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "mode must be normal or maintenance")
	}
	if c.UpgradeID != "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "host is controlled by an upgrade")
	}
	rt, err := store.GetHostRuntime(ctx, tx.Q(), host.HostID)
	if err != nil || !slices.Contains(rt.Runtime.Capabilities, protocol.CapabilityRunnerMaintenance) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "runner has not reported maintenance support")
	}
	c.Revision++
	c.Mode = p.Mode
	if err := tx.PutHostControl(ctx, host.HostID, c, x.Now.UnixMilli()); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: c, Events: []Event{{ObjectType: "host", ObjectID: host.HostID, Kind: "host.maintenance_changed", Body: map[string]any{}}}}, nil
}
