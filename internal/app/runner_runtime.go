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

func validateRuntime(r *protocol.RunnerRuntime) error {
	negotiated, err := protocol.RuntimeRunnerProtocol(r)
	if err != nil || !protocol.RunnerProtocolSupported(negotiated) {
		return protocol.Errorf(protocol.CodeInvalidRequest, "unsupported runner protocol")
	}
	if r == nil {
		return nil
	}
	if len(r.AgentExecutors) > 0 && !slices.Contains(r.Capabilities, protocol.CapabilityAgentExecution) {
		return protocol.Errorf(protocol.CodeInvalidRequest, "execution observations require capability")
	}

	if negotiated != r.Protocol {
		return protocol.Errorf(protocol.CodeInvalidRequest, "runner protocol does not match negotiation")
	}
	if r.InstanceID == "" || len(r.InstanceID) > 128 || r.Sequence < 1 || r.Generation < 1 || r.Protocol < protocol.PreviousRunnerProtocol || r.Protocol > protocol.CurrentRunnerProtocol || r.ProtocolMin > r.Protocol || r.ProtocolMax < r.Protocol || r.ProtocolMin < 0 || r.PendingClaims < 0 || r.PendingOperations < 0 || !slices.Contains([]string{protocol.RunnerNormal, protocol.RunnerMaintenance, protocol.RunnerReady}, r.Mode) {
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
	retired, e := store.HostInstanceRetired(ctx, q, host, r.InstanceID)
	if e != nil {
		return e
	}
	if retired {
		return protocol.Errorf(protocol.CodeConflictPayload, "runner instance has been superseded")
	}
	if err == nil {
		if old.Runtime.InstanceID == r.InstanceID && old.Runtime.Generation != r.Generation {
			return protocol.Errorf(protocol.CodeConflictPayload, "runner instance generation changed")
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

func runnerControl(ctx context.Context, q store.Querier, host string, now int64) (protocol.RunnerControl, error) {
	c, err := store.GetHostControl(ctx, q, host)
	if errors.Is(err, store.ErrNotFound) {
		return protocol.RunnerControl{Mode: protocol.RunnerNormal}, nil
	}
	if err != nil {
		return c, err
	}
	if c.Authorization != nil {
		c.Authorization.RemainingMillis = max(0, c.Authorization.DeadlineMillis-now)
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
	if !fresh && old.Runtime.InstanceID == r.InstanceID && old.Runtime.Sequence >= r.Sequence {
		return false, &protocol.Error{Code: protocol.CodeConflictPayload, Message: "runner observation sequence is stale", Details: json.RawMessage(`{"reason":"stale_sequence"}`)}
	}
	if !fresh && old.Runtime.InstanceID != r.InstanceID {
		if err := tx.RetireHostInstance(ctx, host, old.Runtime.InstanceID); err != nil {
			return false, err
		}
	}
	changed := fresh || !old.Online || old.Runtime.InstanceID != r.InstanceID || old.Runtime.Mode != r.Mode || old.Runtime.AppliedControlRevision != r.AppliedControlRevision || old.Runtime.AuthorizationID != r.AuthorizationID || old.Runtime.UpgradeID != r.UpgradeID || old.Runtime.UpgradePhase != r.UpgradePhase || old.Runtime.UpgradeError != r.UpgradeError || old.Runtime.DownloadedBytes != r.DownloadedBytes || old.Runtime.Build != r.Build || !slices.Equal(old.Runtime.Repositories, r.Repositories) || !slices.Equal(old.Runtime.Capabilities, r.Capabilities) || !slices.Equal(old.Runtime.AgentExecutors, r.AgentExecutors)
	if err := tx.PutHostRuntime(ctx, host, store.HostRuntimeRow{Runtime: *r, LastHeartbeatAt: now, Online: true}); err != nil {
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

	rec, err := store.GetRequest(ctx, q, x.Principal.DedupDomain(), x.Env.RequestID)
	if err == nil {
		var result claimResult
		if json.Unmarshal([]byte(rec.ResultJSON), &result) == nil && result.Assignment.Execution != nil && !runtimeSupportsAgentExecution(p.Runtime) {
			return protocol.Errorf(protocol.CodeInvalidTransition, "recorded named assignment requires its negotiated adapter")
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	return checkQuestionRunRecovery(ctx, q, x.Principal.Subject, p.Runtime)
}

// SweepRunnerPresence persists online transitions so all clients see the same
// last observation, including clients reconnecting after the transition.
func (a *App) SweepRunnerPresence(ctx context.Context) (int, error) {
	n := 0
	err := a.store.Write(ctx, func(tx *store.Tx) error {
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
	var p struct {
		Mode string `json:"mode"`
	}
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
