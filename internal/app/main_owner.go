package app

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// WithMainOwner binds the hosted main Agent to its configured owner. This is
// server configuration, never a tool argument or an inferred recent caller.
func WithMainOwner(subject string) Option {
	return func(a *App) { a.mainOwner = subject }
}

// OwnerExecution is the authenticated native call behind an owner interface.
// Its actor and original request domain survive delegation.
type OwnerExecution struct {
	Actor       protocol.Principal
	ExecutionID string
	CallID      string
	EffectSeq   int64
	// ReplayPrincipal is resolved by the owner adapter from the original record.
	ReplayPrincipal *protocol.Principal
	steps           atomic.Int64
}

type ownerExecutionKey struct{}
type ownerPreparationKey struct{}

func ownerRequestPrincipal(ctx context.Context, p protocol.Principal) protocol.Principal {
	if call, ok := ctx.Value(ownerExecutionKey{}).(*OwnerExecution); ok && p.Kind == protocol.PrincipalOwner {
		if call.ReplayPrincipal != nil {
			return *call.ReplayPrincipal
		}
		return call.Actor
	}
	return p
}

// ActualPrincipal is provenance, whereas Principal may carry delegated authority.
func (x *Exec) ActualPrincipal() protocol.Principal {
	if x.Actor != nil {
		return *x.Actor
	}
	return x.Principal
}

// WithOwnerExecution is used by the server-owned adapter after native identity
// validation. App.Execute independently validates every mutation again.
func WithOwnerExecution(ctx context.Context, call *OwnerExecution) context.Context {
	return context.WithValue(ctx, ownerExecutionKey{}, call)
}

func mainOwnerAuthority(ctx context.Context, q store.Querier, actor protocol.Principal, executionID, owner string, replay bool) error {
	if actor.Kind != protocol.PrincipalMainAgent || owner == "" {
		return protocol.Errorf(protocol.CodeForbidden, "main owner authority is not configured")
	}
	e, err := store.GetHostedExecution(ctx, q, executionID)
	if err != nil {
		return errNotFound(err, "main execution does not exist")
	}
	turn, err := store.GetMainTurn(ctx, q, executionID)
	if err != nil {
		return err
	}
	if e.Selection.Purpose != agentconfig.Main || e.IssueID != "" || turn.ThreadID != actor.Thread {
		return protocol.Errorf(protocol.CodeForbidden, "caller does not own a main execution")
	}
	if !replay && turn.Status != store.TurnStarted && turn.Status != store.TurnStarting {
		return protocol.Errorf(protocol.CodeForbidden, "main execution is no longer active")
	}
	var bound string
	err = q.QueryRowContext(ctx, `SELECT owner_subject FROM main_owner_authorities WHERE execution_id=?`, executionID).Scan(&bound)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if bound != "" && bound != owner {
		return protocol.Errorf(protocol.CodeForbidden, "main execution owner changed")
	}
	return nil
}

// MainOwner authorizes a read through the same owner projection as the UI/CLI.
func (a *App) MainOwner(ctx context.Context, actor protocol.Principal, executionID string, requestID ...string) (protocol.Principal, error) {
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		replay := false
		if len(requestID) > 0 && requestID[0] != "" {
			var found bool
			if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM main_owner_effects e JOIN request_records r ON r.request_id=e.request_id WHERE e.request_id=? AND e.owner_subject=? AND r.domain=?)`, requestID[0], a.mainOwner, actor.DedupDomain()).Scan(&found); err != nil {
				return err
			}
			replay = found
		}
		return mainOwnerAuthority(ctx, tx.Q(), actor, executionID, a.mainOwner, replay)
	})
	return protocol.Principal{Kind: protocol.PrincipalOwner, Subject: a.mainOwner, Client: "main-owner"}, err
}

type ownerExecutionCommand struct {
	Command
	call        *OwnerExecution
	step        int64
	owner       string
	preparation bool
}

func (c ownerExecutionCommand) adapted(x *Exec) *Exec {
	next := *x
	next.Principal = protocol.Principal{Kind: protocol.PrincipalOwner, Subject: c.owner, Client: "main-owner"}
	next.Actor = &c.call.Actor
	return &next
}

func (c ownerExecutionCommand) Authorize(ctx context.Context, x *Exec, q store.Querier) error {
	_, err := store.GetRequest(ctx, q, x.Principal.DedupDomain(), x.Env.RequestID)
	replay := err == nil
	if err := mainOwnerAuthority(ctx, q, c.call.Actor, c.call.ExecutionID, c.owner, replay); err != nil {
		return err
	}
	return c.Command.Authorize(ctx, c.adapted(x), q)
}

func (c ownerExecutionCommand) prepare(ctx context.Context, x *Exec, tx *store.Tx) error {
	if c.call.EffectSeq < 1 || c.call.CallID == "" {
		return protocol.Errorf(protocol.CodeInvalidRequest, "owner tool needs a call identity and positive effect_seq")
	}
	if _, err := tx.Exec(ctx, `INSERT INTO main_owner_authorities(execution_id,owner_subject,created_at) VALUES(?,?,?) ON CONFLICT(execution_id) DO NOTHING`, c.call.ExecutionID, c.owner, x.Now.UnixMilli()); err != nil {
		return err
	}
	stable, err := store.HostedCoordinationIdentity(ctx, tx.Q(), c.call.ExecutionID)
	if err != nil {
		return err
	}
	var priorCall string
	err = tx.Q().QueryRowContext(ctx, `SELECT call_id FROM main_owner_effects WHERE execution_id=? AND effect_seq=? LIMIT 1`, stable, c.call.EffectSeq).Scan(&priorCall)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if priorCall != "" && priorCall != c.call.CallID {
		return duplicateEffect("coordination effect was already applied", map[string]any{"coordination_execution_id": stable, "effect_seq": c.call.EffectSeq})
	}
	if priorCall == "" {
		var original string
		err := tx.Q().QueryRowContext(ctx, `SELECT request_id FROM hosted_coordination_effects WHERE execution_id=? AND effect_seq=?`, stable, c.call.EffectSeq).Scan(&original)
		if err == nil {
			return duplicateEffect("coordination effect was already applied", map[string]any{"coordination_execution_id": stable, "effect_seq": c.call.EffectSeq, "original_request_id": original})
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		e, err := store.GetHostedExecution(ctx, tx.Q(), c.call.ExecutionID)
		if err != nil {
			return err
		}
		table := "local_hosted_coordination_effects"
		if e.Selection.ConfigurationRevision == 0 {
			table = "runner_coordination_effects"
		}
		if _, err = tx.Exec(ctx, `INSERT INTO `+table+`(execution_id,effect_seq,request_id) VALUES(?,?,?)`, stable, c.call.EffectSeq, x.Env.RequestID); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO main_owner_effects(execution_id,effect_seq,step,call_id,request_id,owner_subject) VALUES(?,?,?,?,?,?)`, stable, c.call.EffectSeq, c.step, c.call.CallID, x.Env.RequestID, c.owner); err != nil {
		return err
	}
	return nil
}

func (c ownerExecutionCommand) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	if c.call.ReplayPrincipal != nil {
		return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "original request is required for replay")
	}
	out, err := c.Command.Apply(ctx, c.adapted(x), tx)
	// Retain an exact native-call receipt even for a business no-op.
	if !c.preparation {
		out.NoEffect = false
	}
	return out, err
}
