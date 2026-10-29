package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// BeginOwnerMutation serializes admission with the durable prepare commit. The
// mutex covers a short check/count only; admitted bodies finish without it.
func (a *App) BeginOwnerMutation(ctx context.Context) (func(), error) {
	a.archiveAdmission.Lock()
	defer a.archiveAdmission.Unlock()
	p, err := a.ArchivePreparation(ctx)
	if err != nil {
		return nil, err
	}
	if p.Prepared {
		return nil, protocol.Errorf(protocol.CodeInvalidTransition, "archive preparation rejects new owner writes; cancel preparation to resume")
	}
	a.archiveOwnerRequests++
	return func() { a.archiveAdmission.Lock(); a.archiveOwnerRequests--; a.archiveAdmission.Unlock() }, nil
}

func (a *App) ArchivePreparation(ctx context.Context) (protocol.ArchivePreparation, error) {
	var p protocol.ArchivePreparation
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		p, err = store.ArchivePreparation(ctx, tx.Q())
		return err
	})
	return p, err
}
func archivePrepared(ctx context.Context, q store.Querier) (bool, error) {
	p, err := store.ArchivePreparation(ctx, q)
	return p.Prepared, err
}

// ChangeArchivePreparation leaves host control separate and explicit. Replays
// return the original receipt, even after later cancel/prepare actions.
func (a *App) ChangeArchivePreparation(ctx context.Context, r protocol.ArchiveControlRequest, prepare bool) (protocol.ArchivePreparation, error) {
	a.archiveAdmission.Lock()
	defer a.archiveAdmission.Unlock()
	payload, _ := json.Marshal(map[string]bool{"prepared": prepare})
	env := &protocol.Envelope{RequestID: r.RequestID, Operation: "archive.control", Target: protocol.Target{Type: "archive", ID: "server"}, ExpectedControlRevision: &r.ExpectedControlRevision, Payload: payload, Reason: "Apply explicit local archive preparation control."}
	op, err := a.Execute(ctx, protocol.Principal{Kind: protocol.PrincipalSystem}, env, archiveControl{})
	if err != nil {
		return protocol.ArchivePreparation{}, err
	}
	if op.Error != nil {
		return protocol.ArchivePreparation{}, op.Error
	}
	var p protocol.ArchivePreparation
	err = json.Unmarshal(op.Result, &p)
	return p, err
}

type archiveControl struct{}

func (archiveControl) Operation() string { return "archive.control" }
func (archiveControl) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalSystem)
}
func (archiveControl) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var input struct {
		Prepared bool `json:"prepared"`
	}
	if err := decodePayload(x.Env, &input); err != nil {
		return Outcome{}, err
	}
	p, err := store.ArchivePreparation(ctx, tx.Q())
	if err != nil {
		return Outcome{}, err
	}
	if x.Env.ExpectedControlRevision == nil || *x.Env.ExpectedControlRevision != p.Revision {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "archive control revision is %d", p.Revision)
	}
	if input.Prepared {
		u, e := store.LatestUpgrade(ctx, tx.Q())
		if e != nil && !errors.Is(e, store.ErrNotFound) {
			return Outcome{}, e
		}
		if e == nil && (u.View.Status == "running" || u.View.Status == "attention") {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "finish or explicitly cancel the unfinished upgrade before archive preparation")
		}
	}
	resumeConditions := p.Prepared && !input.Prepared
	p.Revision++
	p.Prepared = input.Prepared
	p.UpdatedAtMillis = x.Now.UnixMilli()
	if input.Prepared {
		p.PreparedAtMillis = p.UpdatedAtMillis
	}
	_, err = tx.Exec(ctx, `UPDATE archive_preparation SET revision=?,prepared=?,prepared_at=?,updated_at=? WHERE singleton=1`, p.Revision, p.Prepared, p.PreparedAtMillis, p.UpdatedAtMillis)
	if err != nil {
		return Outcome{}, err
	}
	if resumeConditions {
		// Events may have committed while occurrence admission was paused.
		// Recheck active conditions through the existing bounded sweep.
		if _, err := tx.Exec(ctx, `UPDATE followup_rules SET next_due=MIN(next_due,?) WHERE status='active' AND json_extract(body_json,'$.kind')='condition'`, x.Now.UnixMilli()); err != nil {
			return Outcome{}, err
		}
	}
	return Outcome{Result: p, Notify: true, Events: []Event{{ObjectType: "archive", ObjectID: "server", Kind: "archive.preparation_changed", Body: map[string]any{"prepared": p.Prepared}}}}, nil
}

// ArchiveOnlineStatus reads existing observations without renewing presence or
// changing unfinished business. Callers append in-process hosted drain facts.
func (a *App) ArchiveOnlineStatus(ctx context.Context) (protocol.ArchiveOnlineStatus, error) {
	a.archiveAdmission.Lock()
	defer a.archiveAdmission.Unlock()
	v := protocol.ArchiveOnlineStatus{ActiveOwnerRequests: a.archiveOwnerRequests, Hosts: []protocol.ArchiveHostStatus{}, Blockers: []string{}}
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		q := tx.Q()
		var err error
		v.Preparation, err = store.ArchivePreparation(ctx, q)
		if err != nil {
			return err
		}
		hosts, err := store.ListActiveHosts(ctx, q)
		if err != nil {
			return err
		}
		for _, h := range hosts {
			hs := protocol.ArchiveHostStatus{HostID: h.HostID, Blockers: []string{}}
			hs.Control, err = runnerControl(ctx, q, h.HostID, a.now().UnixMilli())
			if err != nil {
				return err
			}
			hs.OpenRuns, err = store.CountOpenRuns(ctx, q, h.HostID)
			if err != nil {
				return err
			}
			rt, e := store.GetHostRuntime(ctx, q, h.HostID)
			if e != nil && !errors.Is(e, store.ErrNotFound) {
				return e
			}
			if e != nil {
				hs.Blockers = append(hs.Blockers, "runner observation missing")
			} else {
				hs.Runtime = &rt.Runtime
				if !slices.Contains(rt.Runtime.Capabilities, protocol.CapabilityRunnerMaintenance) {
					hs.Blockers = append(hs.Blockers, "runner has not reported maintenance support")
				}
				hs.Fresh = rt.Online && a.now().UnixMilli()-rt.LastHeartbeatAt < RunnerOfflineAfter.Milliseconds()
				if !hs.Fresh {
					hs.Blockers = append(hs.Blockers, "runner observation offline or stale")
				}
				if rt.Runtime.Mode != protocol.RunnerReady || rt.Runtime.AppliedControlRevision != hs.Control.Revision {
					hs.Blockers = append(hs.Blockers, "runner has not acknowledged current maintenance control")
				}
				if rt.Runtime.PendingClaims != 0 || rt.Runtime.PendingOperations != 0 {
					hs.Blockers = append(hs.Blockers, "runner journal operations remain")
				}
				hs.Blockers = append(hs.Blockers, rt.Runtime.Blockers...)
			}
			if hs.Control.Mode != protocol.RunnerMaintenance {
				hs.Blockers = append(hs.Blockers, "explicit host maintenance is required")
			}
			if hs.OpenRuns != 0 {
				hs.Blockers = append(hs.Blockers, "allocated runs remain")
			}
			v.Hosts = append(v.Hosts, hs)
		}
		retired, err := store.CountOpenRunsOnRetiredHosts(ctx, q)
		if err != nil {
			return err
		}
		if retired != 0 {
			v.Blockers = append(v.Blockers, "retired hosts retain unfinished runs")
		}
		// These are actual uncertain/started effects, not ordinary queued work.
		checks := []struct{ name, query string }{
			{"hosted native turns", `SELECT COUNT(*) FROM main_turns WHERE status IN ('starting','started','unknown')`},
			{"native receipt reads", `SELECT COUNT(*) FROM native_turn_receipts_pending`},
			{"native compactions", `SELECT COUNT(*) FROM agent_compactions WHERE status IN ('starting','running','unknown')`},
			{"native steers", `SELECT COUNT(*) FROM main_steers WHERE status IN ('sending','accepted','unknown')`},
			{"summary operations", `SELECT COUNT(*) FROM conversation_summary_attempts WHERE status='started'`},
			{"worker supplements", `SELECT COUNT(*) FROM run_messages WHERE status IN ('queued','dispatching','unknown')`},
			{"native handoff", `SELECT COUNT(*) FROM main_thread_handoffs WHERE phase IN ('creating','created','injecting','injected','unknown')`},
			{"admitted native controls", `SELECT COUNT(*) FROM outbox WHERE destination='main_thread_control' AND status='pending'`},
			{"retired runner journal blockers", `SELECT COUNT(*) FROM host_runtime r JOIN hosts h ON h.host_id=r.host_id WHERE h.status='retired' AND (json_extract(r.runtime_json,'$.pending_claims')>0 OR json_extract(r.runtime_json,'$.pending_operations')>0 OR json_array_length(json_extract(r.runtime_json,'$.blockers'))>0)`},
			{"uncertain deliveries", `SELECT COUNT(*) FROM outbox WHERE status IN ('sending','unknown')`},
		}
		for _, c := range checks {
			var n int64
			if err := q.QueryRowContext(ctx, c.query).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				v.Blockers = append(v.Blockers, fmt.Sprintf("%s: %d", c.name, n))
			}
		}
		return nil
	})
	return v, err
}

// ArchiveNeedsReceiptRead identifies actual admitted native work, independently
// of pending messages and the currently selected model's availability.
func (a *App) ArchiveNeedsReceiptRead(ctx context.Context, conversation string) (bool, error) {
	var needed bool
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM main_turns t JOIN conversation_messages m ON m.message_id=t.message_id WHERE m.conversation_id=? AND (t.status IN ('starting','started','unknown') OR EXISTS(SELECT 1 FROM native_turn_receipts_pending p WHERE p.request_id=t.request_id))) OR EXISTS(SELECT 1 FROM outbox WHERE destination=? AND status='sending')`, store.ConversationID(conversation), func() string {
			if store.ConversationID(conversation) == "main" {
				return MainThreadDestination
			}
			return "background_thread:" + conversation
		}()).Scan(&needed)
	})
	return needed, err
}
