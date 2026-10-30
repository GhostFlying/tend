package app

import (
	"context"
	"errors"
	"reflect"
	"strings"

	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/hostedconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// AcceptHostedConfiguration is called by server startup with a validated local
// value, never with private material from an owner API request. The envelope
// carries its revision/hash so request replay keeps an exact configuration.
type AcceptHostedConfiguration struct{ Prepared *hostedconfig.Prepared }

func (AcceptHostedConfiguration) Operation() string { return "hosted.configure" }
func (AcceptHostedConfiguration) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalSystem)
}
func (c AcceptHostedConfiguration) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	if c.Prepared == nil || x.Env.Target.Type != "hosted_configuration" || x.Env.Target.ID != "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "hosted configuration needs the server-local prepared value")
	}
	var payload struct {
		Revision int64  `json:"revision"`
		Hash     string `json:"hash"`
	}
	if err := decodePayload(x.Env, &payload); err != nil {
		return Outcome{}, err
	}
	if payload.Revision != c.Prepared.Revision() || payload.Hash != c.Prepared.Hash() {
		return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "prepared configuration differs from the admitted request")
	}
	if x.Env.ExpectedControlRevision == nil || *x.Env.ExpectedControlRevision < 0 {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "expected hosted configuration revision is required")
	}
	current, err := store.GetHostedConfiguration(ctx, tx.Q(), 0)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return Outcome{}, err
	}
	var currentRevision int64
	if err == nil {
		currentRevision = current.Revision
	}
	if *x.Env.ExpectedControlRevision != currentRevision {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "hosted configuration changed")
	}
	if currentRevision > 0 && currentRevision == payload.Revision {
		if current.PrivateHash != payload.Hash {
			return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "hosted configuration revision already has different contents")
		}
		result, err := hostedInventoryProjection(current)
		return Outcome{Result: result}, err
	}
	if payload.Revision <= currentRevision {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "hosted configuration revision was already superseded")
	}
	if currentRevision > 0 {
		previous, err := hostedconfig.Restore(current.PrivateJSON)
		if err != nil {
			return Outcome{}, err
		}
		if err = c.Prepared.ValidateUpdate(previous); err != nil {
			return Outcome{}, err
		}
	}
	for _, definition := range c.Prepared.Public().Agents {
		for _, id := range definition.Categories {
			category, err := store.GetAgentCategory(ctx, tx.Q(), id)
			if err != nil {
				return Outcome{}, errNotFound(err, "hosted definition references an unknown category")
			}
			if category.State != "active" {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "hosted definition category is retired")
			}
		}
	}
	if err := tx.InsertHostedConfiguration(ctx, c.Prepared, x.Env.RequestID, x.Now.UnixMilli()); err != nil {
		return Outcome{}, err
	}
	row, err := store.GetHostedConfiguration(ctx, tx.Q(), payload.Revision)
	if err != nil {
		return Outcome{}, err
	}
	result, err := hostedInventoryProjection(row)
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: result, Events: []Event{{ObjectType: "hosted_configuration", ObjectID: row.Public.ExecutionEnvironmentID, ObjectVersion: row.Revision, Kind: "hosted.configuration_accepted", Body: map[string]any{"inventory": result}}}}, nil
}
func hostedInventoryProjection(row store.HostedConfiguration) (protocol.HostedAgentInventory, error) {
	_, hash, err := protocol.CanonicalAgentInventory(row.Public)
	return protocol.HostedAgentInventory{ConfigurationRevision: row.Revision, PublicSHA256: hash, MainAgentID: row.MainAgentID, AcceptedRequestID: row.RequestID, State: "registered_staged", Projection: row.Public}, err
}
func (a *App) HostedAgentInventory(ctx context.Context) (protocol.HostedAgentInventory, error) {
	var result protocol.HostedAgentInventory
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		row, err := store.GetHostedConfiguration(ctx, tx.Q(), 0)
		if err != nil {
			return err
		}
		result, err = hostedInventoryProjection(row)
		return err
	})
	return result, err
}

// HostedConfiguration reads private state only for a local runtime consumer.
// The owner API must expose HostedAgentInventory instead.
func (a *App) HostedConfiguration(ctx context.Context, revision int64) (*hostedconfig.Prepared, error) {
	var result *hostedconfig.Prepared
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		row, err := store.GetHostedConfiguration(ctx, tx.Q(), revision)
		if err != nil {
			return err
		}
		result, err = hostedconfig.Restore(row.PrivateJSON)
		return err
	})
	return result, err
}

// WithHostedConfiguration selects named mode from the one server-file authority.
// It is immutable for this process; changing the file takes effect on restart.
func WithHostedConfiguration(prepared *hostedconfig.Prepared) Option {
	return func(a *App) { a.hostedConfiguration = prepared }
}
func namedHostedSelection(ctx context.Context, q store.Querier, prepared *hostedconfig.Prepared, conversation string) (hostedconfig.Selection, bool, error) {
	if store.ConversationID(conversation) == "main" {
		if prepared == nil {
			var registered bool
			if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hosted_configuration_versions)`).Scan(&registered); err != nil {
				return hostedconfig.Selection{}, false, err
			}
			if registered {
				return hostedconfig.Selection{}, false, protocol.Errorf(protocol.CodeInvalidTransition, "the accepted named main configuration is unavailable")
			}
			return hostedconfig.Selection{}, false, nil
		}
		selection, err := prepared.Select(prepared.MainAgentID(), agentconfig.Main)
		return selection, true, err
	}

	current, err := store.AgentConversation(ctx, q, conversation)
	if err != nil {
		return hostedconfig.Selection{}, false, err
	}
	if current.Kind == "consultation" {
		if prepared == nil {
			return hostedconfig.Selection{}, false, protocol.Errorf(protocol.CodeInvalidTransition, "consultation requires the server-local named configuration")
		}
		selected, err := store.ConsultationSelection(ctx, q, current.ID)
		return selected, true, err
	}
	issue, err := store.GetCanonicalIssue(ctx, q, current.WorkItemID)
	if errors.Is(err, store.ErrNotFound) {
		return hostedconfig.Selection{}, false, nil
	}
	if err != nil {
		return hostedconfig.Selection{}, false, err
	}
	if selected, named, err := runnerReceiptSelection(ctx, q, issue.IssueID); err != nil || named {
		return selected, named, err
	}
	if m, e := store.CurrentIssueManagerTask(ctx, q, issue.IssueID); e == nil {
		return runnerManagerSelection(ctx, q, m)
	} else if !errors.Is(e, store.ErrNotFound) {
		return hostedconfig.Selection{}, false, e
	}
	var migrated bool
	if err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issue_manager_tasks WHERE issue_id=?)`, issue.IssueID).Scan(&migrated); err != nil {
		return hostedconfig.Selection{}, false, err
	}
	if migrated {
		return hostedconfig.Selection{}, false, protocol.Errorf(protocol.CodeInvalidTransition, "runner manager responsibility is not currently runnable")
	}
	var retired bool
	if err = q.QueryRowContext(ctx, `SELECT finalized FROM manager_runner_cutover WHERE singleton=1`).Scan(&retired); err != nil {
		return hostedconfig.Selection{}, false, err
	}
	if retired {
		return hostedconfig.Selection{}, false, protocol.Errorf(protocol.CodeInvalidTransition, "hosted manager startup is retired")
	}
	if prepared == nil {
		return hostedconfig.Selection{}, false, protocol.Errorf(protocol.CodeInvalidTransition, "canonical Issue coordination requires its named configuration")
	}
	if issue.Assignee.Kind != protocol.IssueAssigneeAgent {
		return hostedconfig.Selection{}, false, protocol.Errorf(protocol.CodeInvalidTransition, "the canonical Issue has no active hosted assignee")
	}

	prefix := prepared.Public().ExecutionEnvironmentID + "/"
	if !strings.HasPrefix(issue.Assignee.Ref, prefix) {
		return hostedconfig.Selection{}, false, protocol.Errorf(protocol.CodeInvalidTransition, "assigned hosted execution environment is unavailable")
	}
	selection, err := prepared.Select(strings.TrimPrefix(issue.Assignee.Ref, prefix), agentconfig.IssueManager)
	selection.IssueID, selection.AssignmentEpoch = issue.IssueID, issue.AssignmentEpoch
	return selection, true, err
}
func (a *App) NamedHostedSelection(ctx context.Context, conversation string) (hostedconfig.Selection, bool, error) {
	var selection hostedconfig.Selection
	var named bool
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		selection, named, err = namedHostedSelection(ctx, tx.Q(), a.hostedConfiguration, conversation)
		return err
	})
	return selection, named, err
}
func (a *App) HostedExecution(ctx context.Context, request string) (store.HostedExecution, bool, error) {
	var row store.HostedExecution
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		row, err = store.GetHostedExecution(ctx, tx.Q(), request)
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		return row, false, nil
	}
	return row, err == nil, err
}

// HostedSelectionForAdmission restores an actual continuation's original pin.
// A new execution alone may resolve the current accepted definition.
func (a *App) HostedSelectionForAdmission(ctx context.Context, request, conversation string) (hostedconfig.Selection, bool, error) {
	var selection hostedconfig.Selection
	var named bool
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		selection, named, err = a.hostedSelectionForAdmission(ctx, tx.Q(), request, conversation)
		return err
	})
	return selection, named, err
}
func (a *App) hostedSelectionForAdmission(ctx context.Context, q store.Querier, request, conversation string) (hostedconfig.Selection, bool, error) {
	if selected, frozen, err := a.hostedFrozenSelection(ctx, q, request); err != nil || frozen {
		if err == nil && selected.RunnerManaged {
			err = rebindRunnerRuntime(ctx, q, &selected)
		}
		return selected, frozen, err
	}
	return namedHostedSelection(ctx, q, a.hostedConfiguration, conversation)
}
func (a *App) hostedFrozenSelection(ctx context.Context, q store.Querier, request string) (hostedconfig.Selection, bool, error) {
	if row, err := store.GetHostedExecution(ctx, q, request); err == nil {
		if err := checkHostedContinuationEpoch(ctx, q, row); err != nil {
			return hostedconfig.Selection{}, false, err
		}
		return row.Selection, true, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return hostedconfig.Selection{}, false, err
	}
	// Repair deliveries are registered against the existing logical responsibility
	// before native admission, independently of rate-limit recovery records.
	roots, err := store.FollowupIDs(ctx, q, `SELECT original.request_id FROM logical_turn_executions repair JOIN logical_turn_executions original ON original.logical_turn_id=repair.logical_turn_id AND original.repair=0 JOIN hosted_executions h ON h.execution_id=original.request_id WHERE repair.request_id=? AND repair.repair=1 ORDER BY original.request_id`, request)
	if err != nil {
		return hostedconfig.Selection{}, false, err
	}
	if len(roots) > 0 {
		original, err := store.GetHostedExecution(ctx, q, roots[0])
		if err != nil {
			return hostedconfig.Selection{}, false, err
		}
		if err := checkHostedContinuationEpoch(ctx, q, original); err != nil {
			return hostedconfig.Selection{}, false, err
		}
		return original.Selection, true, nil
	}
	recovery, err := store.RecoveryForRequest(ctx, q, request)
	if err == nil && recovery.RootRequestID != request {
		original, err := store.GetHostedExecution(ctx, q, recovery.RootRequestID)
		if errors.Is(err, store.ErrNotFound) {
			if a.hostedConfiguration == nil {
				return hostedconfig.Selection{}, false, nil
			}
			return hostedconfig.Selection{}, false, protocol.Errorf(protocol.CodeInvalidTransition, "legacy continuation has no named snapshot; preserve its original executor")
		}
		if err != nil {
			return hostedconfig.Selection{}, false, err
		}
		if err := checkHostedContinuationEpoch(ctx, q, original); err != nil {
			return hostedconfig.Selection{}, false, err
		}
		return original.Selection, true, nil
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return hostedconfig.Selection{}, false, err
	}
	return hostedconfig.Selection{}, false, nil
}
func (a *App) freezeHostedExecution(ctx context.Context, tx *store.Tx, m store.MainTurn, now int64) error {
	if _, err := store.GetHostedExecution(ctx, tx.Q(), m.RequestID); err == nil {
		return nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	conversation, err := store.MessageConversation(ctx, tx.Q(), m.MessageID)
	if err != nil {
		return err
	}
	selected, named, err := a.hostedSelectionForAdmission(ctx, tx.Q(), m.RequestID, conversation)
	if err != nil {
		return err
	}
	opened := *m.Hosted
	// The runtime resolves a provider model at session startup. That observation
	// is recorded below from MainTurn; compare the frozen definition identity.
	expected := selected
	expected.AppliedModel, expected.AppliedProvider, expected.AppliedEffort = "", "", ""
	opened.AppliedModel, opened.AppliedProvider, opened.AppliedEffort = "", "", ""
	if !named || !reflect.DeepEqual(expected, opened) {
		return protocol.Errorf(protocol.CodeStaleRevision, "named selection changed before native admission")
	}
	if selected.RunnerRunID == "" {
		config, err := store.GetHostedConfiguration(ctx, tx.Q(), selected.ConfigurationRevision)
		if err != nil {
			return err
		}
		if config.PrivateHash != selected.ConfigurationHash {
			return protocol.Errorf(protocol.CodeConflictPayload, "named selection does not match its accepted local snapshot")
		}
	}
	if selected.Purpose == agentconfig.Main {
		uncertain, err := hostedMainReceiptPending(ctx, tx.Q())
		if err != nil {
			return err
		}
		if uncertain {
			return protocol.Errorf(protocol.CodeInvalidTransition, "main coordination requires native receipt reconciliation")
		}
	}
	row := store.HostedExecution{ID: m.RequestID, ConversationID: conversation, Selection: selected, CreatedAt: now}
	row.Selection.AppliedModel, row.Selection.AppliedProvider, row.Selection.AppliedEffort = m.Model, m.ModelProvider, m.Effort
	if selected.Purpose == agentconfig.IssueManager {
		current, err := store.AgentConversation(ctx, tx.Q(), conversation)
		if err != nil {
			return err
		}
		issue, err := store.GetCanonicalIssue(ctx, tx.Q(), current.WorkItemID)
		if err != nil {
			return err
		}
		if issue.AssignmentState != "active" || issue.AssignmentEpoch != m.Hosted.AssignmentEpoch || issue.IssueID != m.Hosted.IssueID {
			return protocol.Errorf(protocol.CodeInvalidTransition, "assignment handoff fenced new coordination")
		}
		_, ready, err := a.canonicalManagerAdmission(ctx, tx.Q(), conversation, issue.IssueID, m.RequestID)
		if err != nil {
			return err
		}
		if !ready {
			return protocol.Errorf(protocol.CodeInvalidTransition, "Issue coordination requires native receipt reconciliation")
		}
		row.IssueID, row.AssignmentEpoch = issue.IssueID, issue.AssignmentEpoch
	}
	return tx.InsertHostedExecution(ctx, row)
}

// HostedRuntimeSelection restores the executor needed to reconcile outstanding
// native history before opening a session with the current file configuration.
func (a *App) HostedRuntimeSelection(ctx context.Context, conversation string) (hostedconfig.Selection, bool, error) {
	var selected hostedconfig.Selection
	var named bool
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		ids, err := store.FollowupIDs(ctx, tx.Q(), `SELECT DISTINCT h.execution_id FROM hosted_executions h JOIN main_turns t ON t.request_id=h.execution_id WHERE h.conversation_id=? AND (t.status IN ('starting','started','unknown') OR EXISTS(SELECT 1 FROM native_turn_receipts_pending p WHERE p.request_id=t.request_id) OR EXISTS(SELECT 1 FROM agent_recoveries r WHERE r.root_request_id=h.execution_id AND r.status IN ('starting','running','unknown')) OR EXISTS(SELECT 1 FROM outbox o WHERE o.request_id=h.execution_id AND o.status='sending' AND t.status IN ('completed','rejected'))) ORDER BY h.execution_id`, store.ConversationID(conversation))
		if err != nil {
			return err
		}
		if len(ids) > 0 {
			row, err := store.GetHostedExecution(ctx, tx.Q(), ids[0])
			selected, named = row.Selection, true
			if err == nil && !selected.RunnerManaged && row.IssueID != "" {
				issue, readErr := store.GetCanonicalIssue(ctx, tx.Q(), row.IssueID)
				if readErr != nil {
					return readErr
				}
				selected.ReceiptOnly = issue.AssignmentEpoch != row.AssignmentEpoch
			}
			return err
		}
		destination := MainThreadDestination
		if store.ConversationID(conversation) != "main" {
			destination = "background_thread:" + conversation
		}
		head, err := a.hostedServiceableHead(ctx, tx.Q(), destination)
		if err == nil {
			selected, named, err = a.hostedSelectionForAdmission(ctx, tx.Q(), head.RequestID, conversation)
			return err
		}
		if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		selected, named, err = namedHostedSelection(ctx, tx.Q(), a.hostedConfiguration, conversation)
		return err
	})
	if err == nil && selected.RunnerManaged {
		err = a.store.Read(ctx, func(tx *store.ReadTx) error { return rebindRunnerRuntime(ctx, tx.Q(), &selected) })
	}
	return selected, named, err
}

// canonicalManagerAdmission replaces the legacy baseline gate only for an
// actually registered, active Issue assignee. It is not worker dispatch readiness.
func (a *App) canonicalManagerAdmission(ctx context.Context, q store.Querier, conversation, issueID, request string) (bool, bool, error) {
	var canonical bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issue_controls WHERE issue_id=?)`, issueID).Scan(&canonical); err != nil || !canonical {
		return canonical, false, err
	}
	issue, err := store.GetCanonicalIssue(ctx, q, issueID)
	if err != nil {
		return true, false, err
	}
	if issue.AssignmentState != "active" || issue.Assignee.Kind != protocol.IssueAssigneeAgent {
		return true, false, nil
	}
	frozen := false
	if request != "" {
		_, pinned, err := a.hostedFrozenSelection(ctx, q, request)
		if err != nil {
			if protocol.CodeOf(err) == protocol.CodeForbidden {
				return true, false, nil
			}
			return true, false, err
		}
		frozen = pinned

	}
	var uncertain bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM native_turn_receipts_pending p JOIN hosted_executions h ON h.execution_id=p.request_id WHERE h.issue_id=?)`, issueID).Scan(&uncertain); err != nil {
		return true, false, err
	}
	if uncertain {
		return true, false, nil
	}
	if frozen {
		return true, true, nil
	}
	_, named, err := namedHostedSelection(ctx, q, a.hostedConfiguration, conversation)
	if protocol.CodeOf(err) == protocol.CodeInvalidTransition {
		// Keep the original assignee's pending work while its definition or
		// execution environment is unavailable. Do not select a replacement.
		return true, false, nil
	}
	return true, named, err
}

func checkHostedContinuationEpoch(ctx context.Context, q store.Querier, original store.HostedExecution) error {
	if original.IssueID == "" {
		return nil
	}
	issue, err := store.GetCanonicalIssue(ctx, q, original.IssueID)
	if err != nil {
		return err
	}
	if issue.AssignmentState != "active" || issue.AssignmentEpoch != original.AssignmentEpoch || issue.Assignee.Ref != hostedconfig.IdentityRef(original.Selection.Identity) {
		return protocol.Errorf(protocol.CodeForbidden, "old assignment cannot admit another coordination execution")
	}
	return nil
}

// hostedServiceableHead preserves the existing queue priority, skipping only
// canonical requests whose actual admission is blocked. Frozen continuations
// remain serviceable when a new request's current definition is unavailable.
func (a *App) hostedServiceableHead(ctx context.Context, q store.Querier, destination string) (store.OutboxMessage, error) {
	conversation := "main"
	issueID := ""
	if destination == MainThreadDestination {
		var registered bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hosted_configuration_versions)`).Scan(&registered); err != nil {
			return store.OutboxMessage{}, err
		}
		if a.hostedConfiguration == nil && !registered {
			return store.HeadOutboxPrioritized(ctx, q, destination, OpHandleEvent)
		}
	} else if strings.HasPrefix(destination, "background_thread:") {
		conversation = strings.TrimPrefix(destination, "background_thread:")
		c, err := store.AgentConversation(ctx, q, conversation)
		if err != nil {
			return store.OutboxMessage{}, err
		}
		issueID = c.WorkItemID
		var canonical bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issue_controls WHERE issue_id=?)`, issueID).Scan(&canonical); err != nil {
			return store.OutboxMessage{}, err
		}
		if !canonical {
			return store.HeadOutboxPrioritized(ctx, q, destination, OpHandleEvent)
		}
	} else {
		return store.HeadOutboxPrioritized(ctx, q, destination, "")
	}
	candidates, err := store.ListOutboxPrioritized(ctx, q, destination, OpHandleEvent)
	if err != nil {
		return store.OutboxMessage{}, err
	}
	if len(candidates) == 0 {
		return store.OutboxMessage{}, store.ErrNotFound
	}
	readyFor := func(request string) (bool, error) {
		if conversation != "main" {
			_, ready, err := a.canonicalManagerAdmission(ctx, q, conversation, issueID, request)
			return ready, err
		}
		uncertain, err := hostedMainReceiptPending(ctx, q)
		if err != nil || uncertain {
			return false, err
		}
		_, named, err := a.hostedSelectionForAdmission(ctx, q, request, conversation)
		if protocol.CodeOf(err) == protocol.CodeInvalidTransition || protocol.CodeOf(err) == protocol.CodeForbidden {
			return false, nil
		}
		return named, err
	}
	head := candidates[0]
	if head.Status == store.OutboxSending {
		return head, nil
	}
	ready, err := readyFor(head.RequestID)
	if err != nil {
		return store.OutboxMessage{}, err
	}
	if ready && (head.NextAttemptAt <= a.now().UnixMilli() || conversation == "main") {
		return head, nil
	}
	// Only an already registered, due continuation can pass an unavailable or
	// known-unsent deferred head. Later ordinary input retains its queue order.
	for _, continuation := range candidates {
		if continuation.Status != store.OutboxPending || continuation.NextAttemptAt > a.now().UnixMilli() || (continuation.Operation != "conversation.repair_turn" && continuation.Operation != OpRecoverAgent) {
			continue
		}
		_, frozen, err := a.hostedFrozenSelection(ctx, q, continuation.RequestID)
		if protocol.CodeOf(err) == protocol.CodeForbidden {
			continue
		}
		if err != nil {
			return store.OutboxMessage{}, err
		}
		if !frozen {
			continue
		}
		continuationReady, err := readyFor(continuation.RequestID)
		if err != nil {
			return store.OutboxMessage{}, err
		}
		if continuationReady {
			return continuation, nil
		}
	}
	if ready {
		return head, nil
	}
	return store.OutboxMessage{}, store.ErrNotFound
}

func hostedMainReceiptPending(ctx context.Context, q store.Querier) (bool, error) {
	var pending bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM native_turn_receipts_pending p JOIN hosted_executions h ON h.execution_id=p.request_id WHERE h.conversation_id='main')`).Scan(&pending)
	return pending, err
}
