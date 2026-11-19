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
	if input.Unused && (input.WindowID == "" || input.AuthorizationID == "") {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "unused stop receipt requires the original window and authorization")
	}
	if x.Env.Target.Type != "host" || x.Env.Target.ID != host || process.LaunchID == "" || process.StartTicks == 0 || process.PID <= 1 {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "exact retirement identity required")
	}
	result := protocol.RunnerRetirementResult{RunnerRetirement: input}
	result.WindowID = "" // The response names a window only for its bounded stop.
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
		if stop != nil && stop.Forced && !confirmed.Valid {
			result.WindowID = window.View.UpgradeID
			result.RemainingMillis = max(0, window.View.OverallDeadlineMillis-x.Now.UnixMilli())
			live := window.View.Status == "running" && result.RemainingMillis > 0
			if input.Unused {
				if live || input.AuthorizationID != auth || input.WindowID != window.View.UpgradeID {
					return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "cannot abandon a live or unrelated stop")
				}
				// The supervisor has inspected the same live process and retained
				// a durable record that no signal was requested.
				if _, err := tx.Exec(ctx, `DELETE FROM runner_retirements WHERE host_id=? AND instance_id=? AND authorization_id=? AND confirmed_at IS NULL`, host, process.LaunchID, auth); err != nil {
					return Outcome{}, err
				}
				stop.State = "not_stopped"
				event, err := persistWindowChange(ctx, x, tx, &window)
				result.Allowed = false
				return Outcome{Result: result, Events: []Event{event}}, err
			}
			if input.AuthorizationID == "" {
				result.Allowed = live && input.WindowID == result.WindowID
			}
		} else if input.Unused {
			return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "no matching unused forced stop")
		}
		if !confirmed.Valid && input.AuthorizationID == "" && (stop == nil || !stop.Primary) {
			drain, err := store.GetDrainingRunner(ctx, tx.Q(), host, process.LaunchID)
			if err != nil {
				return Outcome{}, err
			}
			if retirementReady(drain, x.Now.UnixMilli()) || stop != nil && stop.Forced && result.Allowed {
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
			if stop.Primary {
				for i, h := range window.Hosts {
					if h.HostID == host {
						window.View.Hosts[i].Phase = "offline_maintenance"
						break
					}
				}
				rt, err := store.GetHostRuntime(ctx, tx.Q(), host)
				if err != nil {
					return Outcome{}, err
				}
				if rt.Runtime.InstanceID == process.LaunchID {
					ctl, err := runnerControl(ctx, tx.Q(), host, x.Now.UnixMilli())
					if err != nil {
						return Outcome{}, err
					}
					if ctl.UpgradeID == window.View.UpgradeID {
						ctl.UpgradeID = ""
						ctl.Mode = protocol.RunnerMaintenance
						ctl.Revision++
						if err := tx.PutHostControl(ctx, host, ctl, x.Now.UnixMilli()); err != nil {
							return Outcome{}, err
						}
					}
					rt.Online = false
					if err := tx.PutHostRuntime(ctx, host, rt); err != nil {
						return Outcome{}, err
					}
				}
			}
			runEvents, err := confirmWindowStop(ctx, x, tx, stop, auth)
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
		if input.Unused && stop != nil && stop.State == "not_stopped" && input.WindowID == window.View.UpgradeID {
			return Outcome{NoEffect: true, Result: result}, nil
		}
		return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "stop acknowledgement has no original authorization")
	}
	if stop != nil && stop.Primary {
		if window.View.Status != "running" || upgradeBatchExpired(window, x.Now.UnixMilli()) {
			return Outcome{Result: result, NoEffect: true}, nil
		}
		result.WindowID = window.View.UpgradeID
		if input.WindowID != result.WindowID {
			return Outcome{Result: result, NoEffect: true}, nil
		}
		stop.Process = process
		stop.Forced = true
		result.RemainingMillis = window.View.OverallDeadlineMillis - x.Now.UnixMilli()

		return grantRetirement(ctx, x, tx, result, stop.JournalID, raw, &window, stop)
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
		force := stop != nil && window.View.Status == "running" && !upgradeBatchExpired(window, x.Now.UnixMilli())
		if force {
			result.WindowID = window.View.UpgradeID
			if input.WindowID != result.WindowID {
				break
			}
			stop.Forced = true
			result.RemainingMillis = window.View.OverallDeadlineMillis - x.Now.UnixMilli()
		} else {
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
			if n != 0 || len(pending) != 0 {
				break
			}
		}
		result.ReadySequence = rt.Sequence
		return grantRetirement(ctx, x, tx, result, rt.JournalID, raw, &window, stop)
	}
	return Outcome{NoEffect: true, Result: result}, nil
}

func retirementReady(drain store.DrainingRunner, now int64) bool {
	rt := drain.Runtime
	return rt.Mode == protocol.RunnerReady && rt.AppliedControlRevision == drain.Control.Revision && rt.PendingClaims == 0 && rt.PendingOperations == 0 && len(rt.Blockers) == 0 && now-drain.LastHeartbeatAt < RunnerOfflineAfter.Milliseconds()
}

func grantRetirement(ctx context.Context, x *Exec, tx *store.Tx, result protocol.RunnerRetirementResult, journal string, raw []byte, window *store.UpgradeRow, stop *protocol.UpgradeWindowStop) (Outcome, error) {
	result.AuthorizationID = x.IDs.New(protocol.PrefixRequest)
	result.Allowed = true

	_, err := tx.Exec(ctx, `INSERT INTO runner_retirements(host_id,instance_id,upgrade_id,journal_id,authorization_id,process_json) VALUES(?,?,?,?,?,?)`, x.Principal.Subject, result.Process.LaunchID, result.UpgradeID, journal, result.AuthorizationID, string(raw))
	if err != nil {
		return Outcome{}, err
	}
	events := []Event{{ObjectType: "host", ObjectID: x.Principal.Subject, Kind: "runner.retirement_authorized"}}
	if stop != nil {
		stop.State = "stop_authorized"
		changed, err := persistWindowChange(ctx, x, tx, window)
		if err != nil {
			return Outcome{}, err
		}
		events = append(events, changed)
	}
	return Outcome{Result: result, Events: events}, nil
}
