package app

import (
	"context"
	"database/sql"
	"encoding/json"
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
	Agent       protocol.AgentIdentity
	ExecutionID string
	CallID      string
	EffectSeq   int64
	steps       atomic.Int64
}

type ownerExecutionKey struct{}
type ownerPreparationKey struct{}

// RequestDomain follows the original request's namespace, independently of
// both delegated owner authority and the actor performing a recovery.
func (x *Exec) RequestDomain() string {
	if x.requestDomain != "" {
		return x.requestDomain
	}
	return x.ActualPrincipal().DedupDomain()
}

// ActualPrincipal is provenance, whereas Principal may carry delegated authority.
func (x *Exec) ActualPrincipal() protocol.Principal {
	if x.Actor != nil {
		return *x.Actor
	}
	return x.Principal
}

// messageAuthor keeps a delegated owner's original messages visibly Agent-authored.
func (x *Exec) messageAuthor() string {
	if x.ActualPrincipal().Kind == protocol.PrincipalOwner {
		return "owner"
	}
	return "agent"
}

func (x *Exec) recordMessageAuthority(ctx context.Context, tx *store.Tx, message string) error {
	if x.Actor == nil || x.Principal.Kind != protocol.PrincipalOwner {
		return nil
	}
	actor, err := json.Marshal(x.ActualPrincipal())
	if err != nil {
		return err
	}
	call, ok := ctx.Value(ownerExecutionKey{}).(*OwnerExecution)
	if !ok {
		return protocol.Errorf(protocol.CodeForbidden, "delegated message requires its actual execution authority")
	}
	stable, err := store.HostedCoordinationIdentity(ctx, tx.Q(), call.ExecutionID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO main_owner_message_authorities(message_id,execution_id,effect_seq,step,owner_subject,actor_json) SELECT ?,e.execution_id,e.effect_seq,e.step,?,? FROM main_owner_effects e WHERE e.execution_id=? AND e.effect_seq=? AND e.call_id=? AND e.owner_subject=? AND EXISTS(SELECT 1 FROM conversation_messages WHERE message_id=? AND author='agent') ORDER BY e.step DESC LIMIT 1`, message, x.Principal.Subject, string(actor), stable, call.EffectSeq, call.CallID, x.Principal.Subject, message)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO conversation_message_controls(message_id) SELECT ? WHERE EXISTS(SELECT 1 FROM main_owner_message_authorities WHERE message_id=?) ON CONFLICT(message_id) DO NOTHING`, message, message)
	return err
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
	stable, err := store.HostedCoordinationIdentity(ctx, q, executionID)
	if err != nil {
		return err
	}
	for _, identity := range []string{executionID, stable} {
		var bound string
		err = q.QueryRowContext(ctx, `SELECT owner_subject FROM main_owner_authorities WHERE execution_id=?`, identity).Scan(&bound)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if bound != "" && bound != owner {
			return protocol.Errorf(protocol.CodeForbidden, "main execution owner changed")
		}
	}
	return nil
}

// MainOwner authorizes a read through the same owner projection as the UI/CLI.
func (a *App) MainOwner(ctx context.Context, actor protocol.Principal, executionID string, _ ...string) (protocol.Principal, error) {
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		// Reading current business state remains possible for the actual retained
		// execution; it does not admit another mutation or depend on old receipts.
		return mainOwnerAuthority(ctx, tx.Q(), actor, executionID, a.mainOwner, true)
	})
	return protocol.Principal{Kind: protocol.PrincipalOwner, Subject: a.mainOwner, Client: "main-owner"}, err
}

type ownerExecutionCommand struct {
	Command
	call        *OwnerExecution
	step        int64
	owner       string
	preparation bool
	correction  string
}

func (c ownerExecutionCommand) adapted(x *Exec) *Exec {
	next := *x
	next.Principal = protocol.Principal{Kind: protocol.PrincipalOwner, Subject: c.owner, Client: "main-owner"}
	next.Actor = &c.call.Actor
	next.actorAgent = &c.call.Agent
	next.ownerExecutionID = c.call.ExecutionID
	next.executionRequest = c.call.ExecutionID
	return &next
}

func (c ownerExecutionCommand) Authorize(ctx context.Context, x *Exec, q store.Querier) error {
	if err := mainOwnerAuthority(ctx, q, c.call.Actor, c.call.ExecutionID, c.owner, false); err != nil {
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
	if _, err := tx.Exec(ctx, `INSERT INTO main_owner_authorities(execution_id,owner_subject,created_at) VALUES(?,?,?) ON CONFLICT(execution_id) DO NOTHING`, stable, c.owner, x.Now.UnixMilli()); err != nil {
		return err
	}
	var originalEffect int64
	err = tx.Q().QueryRowContext(ctx, `SELECT effect_seq FROM main_owner_effects WHERE execution_id=? AND call_id=? ORDER BY step LIMIT 1`, stable, c.call.CallID).Scan(&originalEffect)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && originalEffect != c.call.EffectSeq {
		return duplicateEffect("native callback already identifies another effect", map[string]any{"coordination_execution_id": stable, "effect_seq": originalEffect})
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
		var original int
		err := tx.Q().QueryRowContext(ctx, `SELECT 1 FROM hosted_coordination_effects WHERE execution_id=? AND effect_seq=?`, stable, c.call.EffectSeq).Scan(&original)
		if err == nil {
			return duplicateEffect("coordination effect was already applied", map[string]any{"coordination_execution_id": stable, "effect_seq": c.call.EffectSeq})
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
		if _, err = tx.Exec(ctx, `INSERT INTO `+table+`(execution_id,effect_seq) VALUES(?,?)`, stable, c.call.EffectSeq); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO main_owner_effects(execution_id,effect_seq,step,call_id,owner_subject) VALUES(?,?,?,?,?) ON CONFLICT(execution_id,effect_seq,step) DO NOTHING`, stable, c.call.EffectSeq, c.step, c.call.CallID, c.owner); err != nil {
		return err
	}
	return nil
}

func (c ownerExecutionCommand) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	if c.correction != "" {
		var logical string
		if err := tx.Q().QueryRowContext(ctx, `SELECT logical_turn_id FROM logical_turn_executions WHERE request_id=?`, c.call.ExecutionID).Scan(&logical); errors.Is(err, sql.ErrNoRows) {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "correction requires a retained logical responsibility")
		} else if err != nil {
			return Outcome{}, err
		}
		if err := validateDelegationCorrection(ctx, x, tx.Q(), logical, c.Operation()); err != nil {
			return Outcome{}, err
		}
		x.validatedDelegationCorrection = c.correction
	}
	return c.Command.Apply(ctx, c.adapted(x), tx)
}

func (a *App) OwnerMessageAuthority(ctx context.Context, id string) (bool, error) {
	var allowed bool
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		allowed, err = store.HasOwnerMessageAuthority(ctx, tx.Q(), id)
		return err
	})
	return allowed, err
}
