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
	"sync"
	"time"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/extensions"
	"github.com/GhostFlying/tend/internal/hostedconfig"
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
	associationDisplayOnly        bool // Keep narration bookkeeping separate from owner authority.
	requestDomain                 string
	managedHostedRuntimes         map[string]bool
	serverDeliveryObservations    bool
	webhookKeys                   WebhookKeys
	interactionOrigin             *communicationOrigin
	coordinationExecution         *store.HostedExecution
	coordinationEffectSeq         int64
	coordinationID                string
	coordinationLogicalID         string
	ownerExecutionID              string
	validatedDelegationCorrection string
	hostedConfiguration           *hostedconfig.Prepared
	// planningSource is the turn's initial delivery, supplied by the host.
	planningSource   string
	executionRequest string
	FollowupsEnabled bool
	FollowupQueued   bool
	Env              *protocol.Envelope
	Principal        protocol.Principal
	// Actor preserves the actual caller when owner authority is delegated.
	Actor      *protocol.Principal
	actorAgent *protocol.AgentIdentity
	TraceID    string
	Now        time.Time
	IDs        protocol.IDGen
	// LeaseTTL is the lease duration of runs allocated by this request.
	LeaseTTL time.Duration
	// WaitPerHour is what an hour in the queue adds to an action's score
	// (D60).
	WaitPerHour float64
	// HostLostAfter is how long a host may go unheard before the
	// workspaces on it count as unavailable; 0 never (D62).
	HostLostAfter        time.Duration
	reviewedContexts     map[string][]string
	affectedIssues       map[string]bool
	globalManagerRefresh bool
	managersRefreshed    bool
}

// Outcome is what a successful Apply produced besides its direct writes.
type Outcome struct {
	// ProjectionOnly excludes output-only facts from scheduling broadcasts.
	ProjectionOnly bool
	// Notify wakes dispatch when a write removes a queue blocker.
	Notify bool
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
	mainOwner                  string
	managedHostedRuntimes      map[string]bool
	extensionValidator         func(context.Context, protocol.ExtensionRelease, extensions.ContentSource, []extensions.ValidationUse) error
	deliveryObserverStatus     *protocol.DeliveryObserverStatus
	serverDeliveryObservations bool
	forwardRecoveryID          string
	committedMaintenanceID     string
	preview                    rolloutPreviewBroker
	localRolloutPreview        func(context.Context, string, []protocol.RolloutPreviewSource, protocol.RolloutPreviewQuery) protocol.RolloutPreview
	webhookKeys                WebhookKeys
	archiveAdmission           sync.Mutex
	archiveOwnerRequests       int64
	hostedConfiguration        *hostedconfig.Prepared
	agentRecoveryPolicy        domain.AgentRecoveryPolicy
	followupsEnabled           bool
	observeDeployment          func(context.Context) (protocol.ObservedDeployment, error)
	store                      *store.Store
	ids                        protocol.IDGen
	now                        func() time.Time
	notifier                   Notifier
	usageWake                  chan struct{}
	changes                    *Broadcast
	scheduling                 *Broadcast
	logs                       *Broadcast
	leaseTTL                   time.Duration
	waitPer                    float64
	hostLost                   time.Duration
}

// Option configures an App.
type Option func(*App)

// WithNotifier makes the App notify n after committing outbox messages.
func WithNotifier(n Notifier) Option {
	return func(a *App) { a.notifier = n }
}

// New returns an App.
func New(s *store.Store, ids protocol.IDGen, now func() time.Time, opts ...Option) *App {
	a := &App{agentRecoveryPolicy: domain.DefaultAgentRecoveryPolicy(), store: s, ids: ids, now: now, leaseTTL: DefaultLeaseTTL, waitPer: DefaultWaitPerHour, hostLost: DefaultHostLostAfter, usageWake: make(chan struct{}, 1), changes: NewBroadcast(), scheduling: NewBroadcast(), logs: NewBroadcast()}
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
	if call, ok := ctx.Value(ownerExecutionKey{}).(*OwnerExecution); ok && p.Kind == protocol.PrincipalOwner {
		if p.Subject != a.mainOwner {
			return protocol.Operation{}, protocol.Errorf(protocol.CodeForbidden, "owner delegation does not match server binding")
		}
		p = call.Actor
		if call.ReplayPrincipal != nil {
			p = *call.ReplayPrincipal
		}
		preparation, _ := ctx.Value(ownerPreparationKey{}).(bool)
		step := int64(0)
		if !preparation {
			step = call.steps.Add(1)
		}
		cmd = ownerExecutionCommand{Command: cmd, call: call, step: step, owner: a.mainOwner, preparation: preparation, correction: delegationCorrection(env.Payload)}
	}
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
	if delegated, ok := cmd.(ownerExecutionCommand); ok && delegated.call.RequestDomain != "" {
		call := delegated.call
		if call.RequestDomain != "owner" && call.RequestDomain != p.DedupDomain() {
			return protocol.Operation{}, protocol.Errorf(protocol.CodeForbidden, "original request namespace is not owned by this caller")
		}
		domain = call.RequestDomain
	}
	x := &Exec{managedHostedRuntimes: a.managedHostedRuntimes, serverDeliveryObservations: a.serverDeliveryObservations, webhookKeys: a.webhookKeys, hostedConfiguration: a.hostedConfiguration, FollowupsEnabled: a.followupsEnabled, Env: env, Principal: p, TraceID: env.TraceID, Now: a.now(), IDs: a.ids, LeaseTTL: a.leaseTTL, WaitPerHour: a.waitPer, HostLostAfter: a.hostLost}
	x.requestDomain = domain
	if delegated, ok := cmd.(ownerExecutionCommand); ok {
		x = delegated.adapted(x)
	}
	if scoped, ok := cmd.(interface{ executionRequestID() string }); ok {
		if p.Kind != protocol.PrincipalMainAgent {
			return protocol.Operation{}, protocol.Errorf(protocol.CodeForbidden, "execution scope requires a hosted main agent")
		}
		x.executionRequest = scoped.executionRequestID()
	}
	if x.TraceID == "" {
		x.TraceID = a.ids.New(protocol.PrefixTrace)
	}
	ctx = store.WithWriteSource(ctx, store.WriteSource{Operation: env.Operation, RequestID: env.RequestID, TraceID: x.TraceID})

	var (
		op             protocol.Operation
		queued         bool
		applied        bool
		projectionOnly bool
	)
	err = a.write(ctx, func(tx *store.Tx) error {

		rec, err := store.GetRequest(ctx, tx.Q(), domain, env.RequestID)
		if p.Kind == protocol.PrincipalOwner && errors.Is(err, store.ErrNotFound) {
			rec, err = store.GetOwnerRequest(ctx, tx.Q(), env.RequestID)
			if err == nil {
				domain = rec.Domain
				x.requestDomain = domain
			}
		}
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
		// Committed receipts above remain replayable during recovery. The fence
		// applies to new actions, with a narrow exception for retained native facts.
		if p.Kind == protocol.PrincipalOwner || p.Kind == protocol.PrincipalMainAgent || p.Kind == protocol.PrincipalBackgroundAgent || env.Operation == protocol.OpRunnerClaim || p.Kind == protocol.PrincipalSystem && recoveryOwnerControl(env.Operation) {
			fenced, err := a.recoveryStartupFenced(ctx, tx.Q())
			if err != nil {
				return err
			}
			if fenced {
				reconciled, err := recoveryReceiptWrite(ctx, x, tx.Q(), cmd)
				if err != nil {
					return err
				}
				if !reconciled {
					return protocol.Errorf(protocol.CodeInvalidTransition, "forward recovery admission is fenced until exact durable receipt linkage")
				}
			}
		}
		rec = store.RequestRecord{
			Domain: domain, RequestID: env.RequestID, Operation: env.Operation, PayloadHash: hash,
			PrincipalJSON: string(principalJSON), ParentRequestID: env.ParentRequestID, TraceID: x.TraceID,
			Reason: env.Reason, CreatedAt: x.Now.UnixMilli(),
		}
		if delegated, ok := cmd.(ownerExecutionCommand); ok && delegated.call.ReplayPrincipal == nil && !delegated.preparation {
			if err := delegated.prepare(ctx, x, tx); err != nil {
				return err
			}
		}
		if err := tx.Savepoint(ctx, applySavepoint); err != nil {
			return err
		}
		// Context revisions include queue and lifecycle counters. Carry only
		// reviews that were exact before the write, independent of its actor or
		// command alias; persistOutcome also requires valid reviewed sources.
		endReviewed := tx.ObservePhase(store.WriteReviewedContext)
		err = snapshotReviewedContexts(ctx, x, tx)
		endReviewed()
		if err != nil {
			return err
		}

		endApply := tx.ObservePhase(store.WriteApply)
		out, applyErr := cmd.Apply(ctx, x, tx)
		endApply()
		if applyErr == nil && out.NoEffect {
			if len(out.Events) > 0 || len(out.Outbox) > 0 {
				return fmt.Errorf("app: %s returned a no-effect outcome with events or outbox messages", env.Operation)
			}
			if p.Kind == protocol.PrincipalMainAgent && coordinationWrite(env.Operation) {
				// Preserve no-effect business semantics, but durably retain the
				// exact receipt so retries cannot observe a later revision.
				if err := tx.RollbackTo(ctx, applySavepoint); err != nil {
					return err
				}
				if err := coordinationReceipt(ctx, x, tx, &out); err != nil {
					return err
				}
				result, err := json.Marshal(out.Result)
				if err != nil {
					return err
				}
				rec.Status, rec.ResultJSON = protocol.OpSucceeded, string(result)
				endInsert := tx.ObservePhase(store.WriteRequestInsert)
				insertErr := tx.InsertRequest(ctx, rec)
				endInsert()
				if insertErr != nil {
					return insertErr
				}
				op = operationOf(rec)
				return nil
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
			if applyErr == nil {
				endRefresh := tx.ObservePhase(store.WriteManagerRefresh)
				applyErr = refreshAffectedManagers(ctx, x, tx)
				endRefresh()
			}
			if applyErr == nil {
				endMaintenance := tx.ObservePhase(store.WriteMaintenance)
				applyErr = settleMaintenanceStopsOnOwnerControl(ctx, x, tx)
				endMaintenance()
			}
		}
		if applyErr != nil {
			pe, ok := errors.AsType[*protocol.Error](applyErr)
			delegation := false
			if c, tracked := delegationCommand(cmd); tracked && ok && pe.Code == protocol.CodeForbidden {
				execution, err := store.GetHostedExecution(ctx, tx.Q(), c.ExecutionID)
				if err != nil {
					return err
				}
				delegation = execution.Selection.Purpose == agentconfig.Main
			}
			// An authenticated coordinator's rejected source is a known failed
			// delegation, not an authorization grant or an unknown side effect.
			if !ok || !businessCodes[pe.Code] && !(delegation && pe.Code == protocol.CodeForbidden) {
				return applyErr
			}
			// Undo the partial apply but keep the transaction, so the
			// failure is recorded atomically with nothing else.
			if err := tx.RollbackTo(ctx, applySavepoint); err != nil {
				return err
			}
			rec.Status, rec.ErrorCode, rec.ErrorMessage, rec.ErrorDetails = protocol.OpFailed, string(pe.Code), pe.Message, string(pe.Details)
			rec.ResultJSON = ""
			recorded, err := recordDelegationAttempt(ctx, x, tx, cmd, pe)
			if err != nil {
				return err
			}
			applied = recorded
		} else {
			if _, err := recordDelegationAttempt(ctx, x, tx, cmd, nil); err != nil {
				return err
			}
			if err := tx.Release(ctx, applySavepoint); err != nil {
				return err
			}
			projectionOnly = out.ProjectionOnly || projectionOnlyOperation(cmd.Operation())
			queued = out.Notify || x.FollowupQueued || len(out.Outbox) > 0
			for _, event := range out.Events {
				queued = queued || wakesMain(x.ActualPrincipal(), event.Kind) || questionEvent(event) || event.ObjectType == "attention"
			}
			applied = true
		}
		endInsert := tx.ObservePhase(store.WriteRequestInsert)
		insertErr := tx.InsertRequest(ctx, rec)
		endInsert()
		if insertErr != nil {
			return insertErr
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
		if env.Operation == protocol.OpRetireUsageCollection || env.Operation == protocol.OpResumeUsageCollection {
			a.wakeLocalUsage()
		}
		if projectionOnly && !queued {
			a.changes.Notify()
		} else {
			a.notifyChange()
		}
	}
	return op, nil
}

func (a *App) persistOutcome(ctx context.Context, x *Exec, tx *store.Tx, out Outcome, rec *store.RequestRecord) error {
	defer tx.ObservePhase(store.WritePersistOutcome)()
	if err := reconcileDecisionDependencies(ctx, x, tx, &out); err != nil {
		return err
	}
	if err := cancelObsoleteQuestions(ctx, x, tx, &out); err != nil {
		return err
	}
	if err := settleObsoleteRunMessages(ctx, x, tx, &out); err != nil {
		return err
	}
	now := x.Now.UnixMilli()
	principal := x.Principal
	if x.Actor != nil {
		principal = *x.Actor
	}
	actor, err := json.Marshal(principal)
	if err != nil {
		return fmt.Errorf("app: encode principal: %w", err)
	}
	taskIssues := map[string][]protocol.WorkItem{}
	eventAffected := map[string]bool{}
	for _, e := range out.Events {
		items, err := eventIssues(ctx, tx.Q(), e, taskIssues)
		if err != nil {
			return err
		}
		for _, item := range items {
			eventAffected[item.WorkItemID] = true
			x.affectedIssues[item.WorkItemID] = true
		}
		if x.Env.ParentRequestID != "" || x.executionRequest != "" {
			request, err := x.reviewExecution(ctx, tx.Q())
			if err != nil {
				return err
			}
			for _, item := range items {
				if _, err := tx.Exec(ctx, `INSERT INTO logical_turn_contexts(logical_turn_id,work_item_id) SELECT logical_turn_id,? FROM logical_turn_executions WHERE request_id=? ON CONFLICT(logical_turn_id,work_item_id) DO NOTHING`, item.WorkItemID, request); err != nil {
					return err
				}
			}
		}
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
		if err := recordIssueChangeEvent(ctx, x, tx, e, eventID); err != nil {
			return err
		}
		if e.Kind == "conversation.processing_stopped" {
			if err := a.settleStoppedMainSources(ctx, tx, e.ObjectID, now); err != nil {
				return err
			}
			if result, ok := out.Result.(map[string]any); ok {
				item, err := store.GetConversationItem(ctx, tx.Q(), e.ObjectID)
				if err != nil {
					return err
				}
				if item.State != "" {
					result["state"] = protocol.MessageState(item.State)
				}
			}
		}
		if e.Kind == "workspace.released" {
			if task, ok := e.Body["task_id"].(string); ok {
				primary, err := store.GetTaskIssueOwner(ctx, tx.Q(), task)
				if err != nil && !errors.Is(err, store.ErrNotFound) {
					return err
				}
				if err == nil {
					if _, err := store.CurrentIssueManagerTask(ctx, tx.Q(), primary); err == nil {
						if err = appendBackgroundSource(ctx, x, tx, "work_item:"+primary, primary, "", eventID, e.Kind, nil); err != nil {
							return err
						}
					} else if !errors.Is(err, store.ErrNotFound) {
						return err
					}
				}
			}
		}
		if e.Kind == "issue.delivery_candidate_expired" {
			if err := recordDeliveryCandidateExpiry(ctx, x, tx, e.ObjectID, eventID); err != nil {
				return err
			}
		}
		if e.Kind == "task.completed" {
			if err := recordCompletionNotice(ctx, x, tx, e.ObjectID, eventID); err != nil {
				return err
			}
		}
		if e.Kind == "issue.closed" || e.Kind == "issue.cancelled" {
			if err := recordCanonicalParentResult(ctx, x, tx, e, eventID); err != nil {
				return err
			}
		}
		if e.Kind == "issue.cancelling" && x.coordinationExecution == nil {
			if err := recordManagerCancellationIntent(ctx, x, tx, e.ObjectID, eventID); err != nil {
				return err
			}
		}
		if issueNotice(e) {
			if err := recordIssueNotice(ctx, x, tx, e, eventID); err != nil {
				return err
			}
		}
		questionIssue := ""
		if questionEvent(e) {
			var err error
			questionIssue, err = syncAttentionDecision(ctx, x, tx, e)
			if err != nil {
				return err
			}
		}
		if e.Kind == "attention.changed" {
			if err := recordAttentionNotice(ctx, x, tx, e, eventID); err != nil {
				return err
			}
		}
		if e.Kind != "task.completed" && (e.Kind == "decision.opened" || wakesMain(x.ActualPrincipal(), e.Kind) || questionIssue != "" && (e.Kind == "decision.answered" || e.Kind == "work_item_choice.answered")) {
			if e.Kind == "task.context_changed" || e.Kind == "task.preparation_blocked" {
				if err := recordTaskContextWake(ctx, x, tx, e, eventID); err != nil {
					return err
				}
			} else if e.Kind == "work_item_choice.answered" {
				if err := recordWorkItemChoiceWake(ctx, x, tx, e, eventID); err != nil {
					return err
				}
			} else if err := recordDecisionWake(ctx, x, tx, e, eventID); err != nil {
				return err
			}
		}
	}
	endAttention := tx.ObservePhase(store.WriteAttention)
	attentionErr := reconcileAttentionResponsibilities(ctx, x, tx)
	endAttention()
	if attentionErr != nil {
		return attentionErr
	}
	if err := syncMessagingInteractions(ctx, x, tx, out.Events); err != nil {
		return err
	}
	if err := observationIssueEvents(ctx, x, tx, out.Events); err != nil {
		return err
	}
	if err := webhookIssueEvents(ctx, x, tx, out.Events); err != nil {
		return err
	}
	endFollowup := tx.ObservePhase(store.WriteFollowup)
	matched, err := followupEvents(ctx, x, tx, out.Events)
	endFollowup()
	if err != nil {
		return err
	}
	x.FollowupQueued = matched
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
	for id, logicals := range x.reviewedContexts {
		if !eventAffected[id] {
			continue
		}
		checked, err := store.WorkItemContextReviewed(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		if checked {
			for _, logical := range logicals {
				if _, err := tx.Exec(ctx, `UPDATE logical_turn_context_reviews SET revision=(SELECT revision FROM work_item_contexts WHERE work_item_id=?) WHERE work_item_id=? AND logical_turn_id=?`, id, id, logical); err != nil {
					return err
				}
			}
		}
	}
	if err := coordinationReceipt(ctx, x, tx, &out); err != nil {
		return err
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

// projectionOnlyOperation identifies high-frequency writes that carry no new
// scheduling facts. An actual followup overrides this classification.
func projectionOnlyOperation(operation string) bool {
	switch operation {
	case "conversation.record_agent_message", protocol.OpRunnerUsageRegister, protocol.OpRunnerUsageUpload, protocol.OpRunnerUsageCollection, protocol.OpRetireUsageCollection, protocol.OpResumeUsageCollection:
		return true
	default:
		return false
	}
}
