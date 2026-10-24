// Package app is the use-case layer. Every side-effecting request goes
// through App.Execute, which deduplicates it by request ID, authorizes it and
// applies it in one transaction together with its events and outbox
// messages.
//
// Commands that tend issues on its own behalf under the system principal,
// such as lease expiry sweeps and startup reconciliation, use a fresh
// request ID for every attempt. Their idempotence comes from state
// preconditions checked inside Apply, for example "only if the lease is
// still active and has expired", not from the request record.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// Command is one side-effecting operation.
type Command interface {
	// Operation is the envelope operation the command handles.
	Operation() string
	// Authorize decides whether the principal may perform the request. It
	// runs on every execution, including replays of recorded requests, so
	// revoked access cannot read old results.
	Authorize(ctx context.Context, x *Exec, q store.Querier) error
	// Apply performs the request inside the write transaction.
	Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error)
}

// Exec carries the context of one execution.
type Exec struct {
	Env       *protocol.Envelope
	Principal protocol.Principal
	TraceID   string
	Now       time.Time
	IDs       protocol.IDGen
	// LeaseTTL is the lease duration of runs allocated by this request.
	LeaseTTL time.Duration
	// WaitPerHour is what an hour in the queue adds to an action's score
	// (D60).
	WaitPerHour float64
	// HostLostAfter is how long a host may go unheard before the
	// workspaces on it count as unavailable; 0 never (D62).
	HostLostAfter time.Duration
}

// Outcome is what a successful Apply produced besides its direct writes.
type Outcome struct {
	Result any
	Events []Event
	Outbox []OutboxMessage
	// Accepted marks an external phase that continues after commit.
	Accepted bool
	// NoEffect reports that the command found nothing to do, such as a
	// claim that found no work. The transaction is rolled back and the
	// request is not recorded, so a retry with the same ID runs again. A
	// no-effect outcome must not carry events or outbox messages.
	NoEffect bool
}

// errNoEffect rolls back the transaction of a no-effect outcome.
var errNoEffect = errors.New("app: no effect")

// Event is a domain event about one object.
type Event struct {
	ObjectType    string
	ObjectID      string
	ObjectVersion int64
	Kind          string
	Body          map[string]any
}

// OutboxMessage is delivered by the dispatcher after commit. Its request ID
// is allocated here, before the first send, so redelivery keeps it.
type OutboxMessage struct {
	Destination string
	Operation   string
	Payload     any
}

// App executes commands against the store.
type App struct {
	store    *store.Store
	ids      protocol.IDGen
	now      func() time.Time
	notifier Notifier
	changes  *Broadcast
	logs     *Broadcast
	leaseTTL time.Duration
	waitPer  float64
	hostLost time.Duration
}

// Option configures an App.
type Option func(*App)

// WithNotifier makes the App notify n after committing outbox messages.
func WithNotifier(n Notifier) Option {
	return func(a *App) { a.notifier = n }
}

// New returns an App.
func New(s *store.Store, ids protocol.IDGen, now func() time.Time, opts ...Option) *App {
	a := &App{store: s, ids: ids, now: now, leaseTTL: DefaultLeaseTTL, waitPer: DefaultWaitPerHour, hostLost: DefaultHostLostAfter, changes: NewBroadcast(), logs: NewBroadcast()}
	for _, o := range opts {
		o(a)
	}
	return a
}

// businessCodes are deterministic rejections: the same request would be
// rejected the same way again, so the failure is recorded and replayed.
var businessCodes = map[protocol.Code]bool{
	protocol.CodeInvalidRequest:    true,
	protocol.CodeStaleRevision:     true,
	protocol.CodeNotFound:          true,
	protocol.CodeInvalidTransition: true,
	protocol.CodeDuplicateEffect:   true,
}

const applySavepoint = "apply"

// Execute runs a side-effecting request exactly once per (dedup domain,
// request ID).
//
// The returned error covers rejections that are not recorded: malformed
// envelopes, payload conflicts, authorization failures and internal errors.
// Recorded business failures come back as an Operation with status failed,
// and replaying the same request returns the same Operation.
func (a *App) Execute(ctx context.Context, p protocol.Principal, env *protocol.Envelope, cmd Command) (protocol.Operation, error) {
	if err := p.Validate(); err != nil {
		return protocol.Operation{}, fmt.Errorf("app: invalid principal: %w", err)
	}
	if err := env.Validate(); err != nil {
		return protocol.Operation{}, err
	}
	if env.Operation != cmd.Operation() {
		return protocol.Operation{}, protocol.Errorf(protocol.CodeInvalidRequest, "operation %q is not handled here", env.Operation)
	}
	hash, err := protocol.PayloadHash(env)
	if err != nil {
		return protocol.Operation{}, protocol.Errorf(protocol.CodeInvalidRequest, "payload cannot be hashed")
	}
	principalJSON, err := json.Marshal(p)
	if err != nil {
		return protocol.Operation{}, fmt.Errorf("app: encode principal: %w", err)
	}
	domain := p.DedupDomain()
	x := &Exec{Env: env, Principal: p, TraceID: env.TraceID, Now: a.now(), IDs: a.ids, LeaseTTL: a.leaseTTL, WaitPerHour: a.waitPer, HostLostAfter: a.hostLost}
	if x.TraceID == "" {
		x.TraceID = a.ids.New(protocol.PrefixTrace)
	}

	var (
		op      protocol.Operation
		queued  bool
		applied bool
	)
	err = a.store.Write(ctx, func(tx *store.Tx) error {
		rec, err := store.GetRequest(ctx, tx.Q(), domain, env.RequestID)
		switch {
		case err == nil:
			if rec.PayloadHash != hash {
				return protocol.Errorf(protocol.CodeConflictPayload, "request_id was already used for a different request")
			}
			if err := cmd.Authorize(ctx, x, tx.Q()); err != nil {
				return err
			}
			op = operationOf(rec)
			return nil
		case !errors.Is(err, store.ErrNotFound):
			return err
		}

		if err := cmd.Authorize(ctx, x, tx.Q()); err != nil {
			return err
		}
		rec = store.RequestRecord{
			Domain: domain, RequestID: env.RequestID, Operation: env.Operation, PayloadHash: hash,
			PrincipalJSON: string(principalJSON), ParentRequestID: env.ParentRequestID, TraceID: x.TraceID,
			Reason: env.Reason, CreatedAt: x.Now.UnixMilli(),
		}
		if err := tx.Savepoint(ctx, applySavepoint); err != nil {
			return err
		}
		out, applyErr := cmd.Apply(ctx, x, tx)
		if applyErr == nil && out.NoEffect {
			if len(out.Events) > 0 || len(out.Outbox) > 0 {
				return fmt.Errorf("app: %s returned a no-effect outcome with events or outbox messages", env.Operation)
			}
			result, err := json.Marshal(out.Result)
			if err != nil {
				return fmt.Errorf("app: encode result: %w", err)
			}
			op = protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: result}
			return errNoEffect
		}
		if applyErr == nil {
			applyErr = a.persistOutcome(ctx, x, tx, out, &rec)
		}
		if applyErr != nil {
			pe, ok := errors.AsType[*protocol.Error](applyErr)
			if !ok || !businessCodes[pe.Code] {
				return applyErr
			}
			// Undo the partial apply but keep the transaction, so the
			// failure is recorded atomically with nothing else.
			if err := tx.RollbackTo(ctx, applySavepoint); err != nil {
				return err
			}
			rec.Status, rec.ErrorCode, rec.ErrorMessage, rec.ErrorDetails = protocol.OpFailed, string(pe.Code), pe.Message, string(pe.Details)
			rec.ResultJSON = ""
		} else {
			if err := tx.Release(ctx, applySavepoint); err != nil {
				return err
			}
			queued = len(out.Outbox) > 0
			for _, event := range out.Events {
				queued = queued || wakesMain(x.Principal, event.Kind)
			}
			applied = true
		}
		if err := tx.InsertRequest(ctx, rec); err != nil {
			return err
		}
		op = operationOf(rec)
		return nil
	})
	// Compare exactly: a rollback failure joined with the sentinel must
	// surface, since it usually means the connection is broken.
	if err != nil && err != errNoEffect {
		return protocol.Operation{}, err
	}
	if queued && a.notifier != nil {
		a.notifier.Notify()
	}
	if applied {
		a.notifyChange()
	}
	return op, nil
}

func (a *App) persistOutcome(ctx context.Context, x *Exec, tx *store.Tx, out Outcome, rec *store.RequestRecord) error {
	if err := cancelObsoleteQuestions(ctx, x, tx, &out); err != nil {
		return err
	}
	now := x.Now.UnixMilli()
	actor, err := json.Marshal(x.Principal)
	if err != nil {
		return fmt.Errorf("app: encode principal: %w", err)
	}
	for _, e := range out.Events {
		body, err := json.Marshal(e.Body)
		if err != nil {
			return fmt.Errorf("app: encode event: %w", err)
		}
		eventID := x.IDs.New(protocol.PrefixEvent)
		if _, err := tx.InsertEvent(ctx, store.EventRow{
			EventID: eventID, Source: "app", ObjectType: e.ObjectType, ObjectID: e.ObjectID,
			ObjectVersion: e.ObjectVersion, Kind: e.Kind, BodyJSON: string(body), RequestID: x.Env.RequestID,
			TraceID: x.TraceID, ActorJSON: string(actor), CreatedAt: now,
		}); err != nil {
			return err
		}
		if e.Kind == "task.completed" {
			if err := recordCompletionNotice(ctx, x, tx, e.ObjectID, eventID); err != nil {
				return err
			}
		}
		if e.Kind != "task.completed" && wakesMain(x.Principal, e.Kind) {
			if err := recordDecisionWake(ctx, x, tx, e, eventID); err != nil {
				return err
			}
		}
	}
	for _, m := range out.Outbox {
		payload, err := json.Marshal(m.Payload)
		if err != nil {
			return fmt.Errorf("app: encode outbox payload: %w", err)
		}
		if err := tx.InsertOutbox(ctx, store.OutboxRow{
			OutboxID: x.IDs.New(protocol.PrefixOutbox), RequestID: x.IDs.New(protocol.PrefixRequest),
			ParentRequestID: x.Env.RequestID, Destination: m.Destination, Operation: m.Operation,
			PayloadJSON: string(payload), CreatedAt: now,
		}); err != nil {
			return err
		}
	}
	result, err := json.Marshal(out.Result)
	if err != nil {
		return fmt.Errorf("app: encode result: %w", err)
	}
	rec.ResultJSON = string(result)
	rec.Status = protocol.OpSucceeded
	if out.Accepted {
		rec.Status = protocol.OpAccepted
	}
	return nil
}

func operationOf(r store.RequestRecord) protocol.Operation {
	op := protocol.Operation{RequestID: r.RequestID, Status: r.Status}
	if r.ResultJSON != "" {
		op.Result = json.RawMessage(r.ResultJSON)
	}
	if r.ErrorCode != "" {
		op.Error = &protocol.Error{Code: protocol.Code(r.ErrorCode), Message: r.ErrorMessage}
		if r.ErrorDetails != "" {
			op.Error.Details = json.RawMessage(r.ErrorDetails)
		}
	}
	return op
}

// errNotFound maps a store miss to the protocol code and leaves other
// errors untouched.
func errNotFound(err error, format string, args ...any) error {
	if errors.Is(err, store.ErrNotFound) {
		return protocol.Errorf(protocol.CodeNotFound, format, args...)
	}
	return err
}

// duplicateEffect builds the error for an effect that was already applied,
// naming the original so the caller can look it up instead of retrying.
func duplicateEffect(message string, details map[string]any) error {
	pe := protocol.Errorf(protocol.CodeDuplicateEffect, "%s", message)
	if b, err := json.Marshal(details); err == nil {
		pe.Details = b
	}
	return pe
}
