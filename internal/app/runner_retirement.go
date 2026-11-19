package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// RetireRollingRunner grants a specific stop and separately records its
// physical acknowledgement. Silence never becomes a retirement receipt.
type RetireRollingRunner struct{}

func (RetireRollingRunner) Operation() string { return "runner.rolling_retire" }
func (RetireRollingRunner) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalRunner)
}
func (RetireRollingRunner) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var input protocol.RunnerRetirement
	if err := decodePayload(x.Env, &input); err != nil {
		return Outcome{}, err
	}
	host := x.Principal.Subject
	process := input.Process
	if x.Env.Target.Type != "host" || x.Env.Target.ID != host || process.LaunchID == "" || process.StartTicks == 0 || process.PID <= 1 {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "exact retirement identity required")
	}
	result := protocol.RunnerRetirementResult{RunnerRetirement: input}
	raw, err := json.Marshal(process)
	if err != nil {
		return Outcome{}, err
	}
	window, stop, err := windowForRetirement(ctx, tx.Q(), host, input)
	if err != nil {
		return Outcome{}, err
	}
	var upgrade, auth, prior string
	var confirmed sql.NullInt64
	err = tx.Q().QueryRowContext(ctx, `SELECT upgrade_id,authorization_id,process_json,confirmed_at FROM runner_retirements WHERE host_id=? AND instance_id=?`, host, process.LaunchID).Scan(&upgrade, &auth, &prior, &confirmed)
	if err == nil {
		if upgrade != input.UpgradeID || prior != string(raw) || input.AuthorizationID != "" && input.AuthorizationID != auth {
			return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "retirement identity changed")
		}
		result.AuthorizationID, result.Allowed, result.Confirmed = auth, true, confirmed.Valid
		if !confirmed.Valid && input.AuthorizationID == "" {
			drain, err := store.GetDrainingRunner(ctx, tx.Q(), host, process.LaunchID)
			if err != nil {
				return Outcome{}, err
			}
			if retirementReady(drain, x.Now.UnixMilli()) || stop != nil && stop.State == "stop_authorized" && window.View.Status == "running" && !upgradeBatchExpired(window, x.Now.UnixMilli()) && x.Now.UnixMilli() >= stop.DrainUntil {
				result.ReadySequence = drain.Runtime.Sequence
			}
		}
		if confirmed.Valid || input.AuthorizationID == "" {
			return Outcome{Result: result, NoEffect: true}, nil
		}
		if err := tx.RetireHostInstance(ctx, host, process.LaunchID); err != nil {
			return Outcome{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE runner_retirements SET confirmed_at=? WHERE host_id=? AND instance_id=?`, x.Now.UnixMilli(), host, process.LaunchID); err != nil {
			return Outcome{}, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM draining_runners WHERE host_id=? AND instance_id=? AND upgrade_id=?`, host, process.LaunchID, upgrade); err != nil {
			return Outcome{}, err
		}
		result.Confirmed = true
		events := []Event{{ObjectType: "host", ObjectID: host, Kind: "runner.retired"}}
		if stop != nil {
			runEvents, err := confirmWindowStop(ctx, x, tx, stop)
			if err != nil {
				return Outcome{}, err
			}
			changed, err := persistWindowChange(ctx, x, tx, &window)
			if err != nil {
				return Outcome{}, err
			}
			events = append(append(events, runEvents...), changed)
		}
		return Outcome{Result: result, Events: events}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Outcome{}, err
	}
	if input.AuthorizationID != "" {
		return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "stop acknowledgement has no original authorization")
	}
	drain, err := store.GetDrainingRunner(ctx, tx.Q(), host, process.LaunchID)
	if err != nil {
		return Outcome{}, err
	}
	if drain.UpgradeID != input.UpgradeID {
		return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "predecessor belongs to another upgrade")
	}
	batch, err := store.GetUpgrade(ctx, tx.Q(), drain.UpgradeID)
	if err != nil {
		return Outcome{}, err
	}
	for _, h := range batch.Hosts {
		if h.HostID != host || h.RollingProcess != process || h.Committed == nil {
			continue
		}
		rt := drain.Runtime
		force := stop != nil && window.View.Status == "running" && !upgradeBatchExpired(window, x.Now.UnixMilli()) && x.Now.UnixMilli() >= stop.DrainUntil
		if !force {
			if !retirementReady(drain, x.Now.UnixMilli()) {
				break
			}
			n, err := store.CountJournalRuns(ctx, tx.Q(), host, rt.JournalID)
			if err != nil {
				return Outcome{}, err
			}
			pending, err := usageCollectionCommands(ctx, tx.Q(), host, &rt)
			if err != nil {
				return Outcome{}, err
			}
			if n != 0 || len(pending) > 0 {
				break
			}
		}
		result.AuthorizationID = x.IDs.New(protocol.PrefixRequest)
		result.Allowed = true
		result.ReadySequence = rt.Sequence
		_, err = tx.Exec(ctx, `INSERT INTO runner_retirements(host_id,instance_id,upgrade_id,journal_id,authorization_id,process_json) VALUES(?,?,?,?,?,?)`, host, process.LaunchID, input.UpgradeID, rt.JournalID, result.AuthorizationID, string(raw))
		if err != nil {
			return Outcome{}, err
		}
		events := []Event{{ObjectType: "host", ObjectID: host, Kind: "runner.retirement_authorized"}}
		if stop != nil {
			stop.State = "stop_authorized"
			changed, err := persistWindowChange(ctx, x, tx, &window)
			if err != nil {
				return Outcome{}, err
			}
			events = append(events, changed)
		}
		return Outcome{Result: result, Events: events}, nil
	}
	return Outcome{NoEffect: true, Result: result}, nil
}

func retirementReady(drain store.DrainingRunner, now int64) bool {
	rt := drain.Runtime
	return rt.Mode == protocol.RunnerReady && rt.AppliedControlRevision == drain.Control.Revision && rt.PendingClaims == 0 && rt.PendingOperations == 0 && len(rt.Blockers) == 0 && now-drain.LastHeartbeatAt < RunnerOfflineAfter.Milliseconds()
}
