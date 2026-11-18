package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type SteerIssueOwner struct{}

func (SteerIssueOwner) Operation() string { return protocol.OpIssueOwnerSteer }
func (SteerIssueOwner) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}

func (SteerIssueOwner) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.IssueOwnerSteerPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	issueID := x.Env.Target.ID
	if x.Env.Target.Type != "issue" || !protocol.ValidID(protocol.PrefixMessage, p.MessageID) ||
		!protocol.ValidID(protocol.PrefixMessage, p.SourceMessageID) || p.RunID == "" ||
		p.ExpectedAssignmentEpoch < 1 || p.ExpectedSourceRevision < 1 {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "issue, message, run, assignment epoch and source revision are required")
	}
	revision, err := requireRevision(x.Env)
	if err != nil {
		return Outcome{}, err
	}
	sender := x.ActualPrincipal()
	if prior, err := store.GetIssueOwnerSteer(ctx, tx.Q(), p.MessageID); err == nil {
		if prior.IssueID != issueID || prior.IssueRevision != revision || prior.RunID != p.RunID ||
			prior.AssignmentEpoch != p.ExpectedAssignmentEpoch || prior.SourceMessageID != p.SourceMessageID ||
			prior.SourceRevision != p.ExpectedSourceRevision || prior.RevokesSourceMessageID != p.RevokesSourceMessageID ||
			prior.Sender.Kind != sender.Kind || prior.Sender.Subject != sender.Subject {
			return Outcome{}, protocol.Errorf(protocol.CodeConflictPayload, "urgent notice identity has another immutable input")
		}
		return Outcome{NoEffect: true, Result: prior.IssueOwnerSteerView}, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return Outcome{}, err
	}
	source, sourceRevision, withdrawn, err := issueSteerSource(ctx, tx.Q(), issueID, p.SourceMessageID)
	if err != nil {
		return Outcome{}, err
	}
	if sourceRevision != p.ExpectedSourceRevision || withdrawn {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "urgent source changed or was withdrawn")
	}
	textOnly, err := isIssueSteerTextSource(ctx, tx.Q(), source)
	if err != nil {
		return Outcome{}, err
	}
	if !textOnly {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "urgent delivery requires a nonempty text-only original without images or attachments")
	}
	if sender.Kind == protocol.PrincipalMainAgent {
		admitted, err := mainExecutionSourceAdmitted(ctx, tx.Q(), x.ownerExecutionID, source.MessageID)
		if err != nil {
			return Outcome{}, err
		}
		if !admitted {
			return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "Main must route an owner original admitted to its execution")
		}
	}
	var priorID string
	err = tx.Q().QueryRowContext(ctx, `SELECT message_id FROM issue_owner_steers WHERE issue_id=? AND source_message_id=?`, issueID, source.MessageID).Scan(&priorID)
	if err == nil {
		return Outcome{}, commandDiagnostic(protocol.CodeConflictPayload, "source already has an urgent notice in this Issue", map[string]any{"original_message_id": priorID})
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Outcome{}, err
	}
	if p.RevokesSourceMessageID != "" {
		old, _, _, err := issueSteerSource(ctx, tx.Q(), issueID, p.RevokesSourceMessageID)
		if err != nil {
			return Outcome{}, err
		}
		if old.Seq >= source.Seq {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "revocation must cite an earlier owner source in this Issue")
		}
	}
	issue, err := store.GetCanonicalIssue(ctx, tx.Q(), issueID)
	if err != nil {
		return Outcome{}, errNotFound(err, "target Issue does not exist")
	}
	if issue.Revision != revision || issue.AssignmentEpoch != p.ExpectedAssignmentEpoch {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "Issue or its owner changed")
	}
	manager, err := store.CurrentIssueManagerTask(ctx, tx.Q(), issueID)
	if err != nil {
		return Outcome{}, errNotFound(err, "Issue has no current owner task")
	}
	run, err := store.GetRun(ctx, tx.Q(), p.RunID)
	if err != nil {
		return Outcome{}, errNotFound(err, "target owner Run does not exist")
	}
	if run.TaskID != manager.TaskID || run.Role != protocol.RoleIssueManager || manager.AssignmentEpoch != issue.AssignmentEpoch {
		return Outcome{}, protocol.Errorf(protocol.CodeForbidden, "Run does not belong to the current Issue owner")
	}
	var execution string
	err = tx.Q().QueryRowContext(ctx, `SELECT e.execution_id FROM hosted_executions e JOIN main_turns t ON t.request_id=e.execution_id
 WHERE e.issue_id=? AND e.assignment_epoch=? AND e.conversation_id=? AND t.status='started' AND t.turn_id<>''
 AND json_extract(e.private_snapshot,'$.runner_run_id')=? ORDER BY t.updated_at DESC LIMIT 1`,
		issueID, manager.AssignmentEpoch, manager.ConversationID, run.RunID).Scan(&execution)
	if errors.Is(err, sql.ErrNoRows) {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "the target Run has no confirmed active native turn; urgent input was not queued")
	}
	if err != nil {
		return Outcome{}, err
	}
	record, err := store.GetHostedExecution(ctx, tx.Q(), execution)
	if err != nil {
		return Outcome{}, err
	}
	turn, err := store.GetMainTurn(ctx, tx.Q(), execution)
	if err != nil {
		return Outcome{}, err
	}
	if err := authorizeRunnerCoordination(ctx, tx.Q(), issueSteerRunPrincipal(record), record, true, x.Now.UnixMilli()); err != nil {
		return Outcome{}, err
	}
	if usable, err := issueSteerTurnAdmitted(ctx, tx.Q(), execution); err != nil {
		return Outcome{}, err
	} else if !usable {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "the target turn is reconciling or draining; urgent input was not queued")
	}

	// The routed text stays attributable to its original. This notice does not
	// create an owner-authority row or grant a business-control operation.
	header := fmt.Sprintf("[Tend urgent Issue owner notice %s. Issue %s, assignment %d. Original owner message %s revision %d, sequence %d.",
		p.MessageID, issueID, manager.AssignmentEpoch, source.MessageID, sourceRevision, source.Seq)
	if p.RevokesSourceMessageID != "" {
		header += fmt.Sprintf(" This is a revocation of source %s for this Issue. Recheck in-flight authority and reconcile unknown effects using their original identities.", p.RevokesSourceMessageID)
	}
	header += "]\n\n"
	steer := store.IssueOwnerSteer{
		IssueOwnerSteerView: protocol.IssueOwnerSteerView{
			MessageID: p.MessageID, IssueID: issueID, AssignmentEpoch: manager.AssignmentEpoch,
			TaskID: manager.TaskID, RunID: run.RunID, ThreadID: turn.ThreadID, TurnID: turn.TurnID,
			SourceMessageID: source.MessageID, SourceRevision: sourceRevision, RevokesSourceMessageID: p.RevokesSourceMessageID,
			Sender: sender, Status: "queued",
		},
		IssueRevision: revision, ExecutionID: execution, InputHeader: header, InputSHA256: issueSteerInputDigest(header + source.Body),
	}
	author := "external"
	if sender.Kind == protocol.PrincipalMainAgent {
		author = "agent"
	}
	seq, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: p.MessageID, ConversationID: manager.ConversationID,
		Body: source.Body, Author: author, RequestID: x.Env.RequestID, CreatedAt: x.Now.UnixMilli()})
	if err != nil {
		return Outcome{}, err
	}
	if err := tx.InsertIssueOwnerSteer(ctx, steer); err != nil {
		return Outcome{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO conversation_blocks(message_id) VALUES(?)`, p.MessageID); err != nil {
		return Outcome{}, err
	}
	if p.RevokesSourceMessageID != "" {
		var revokedNotice string
		err := tx.Q().QueryRowContext(ctx, `SELECT message_id FROM issue_owner_steers
 WHERE issue_id=? AND source_message_id=? AND (status='queued' OR (status='sending' AND native_request_id IS NULL))`,
			issueID, p.RevokesSourceMessageID).Scan(&revokedNotice)
		switch {
		case err == nil:
			if err := writeIssueSteerState(ctx, tx, x.IDs, revokedNotice, "rejected", "source_revoked", x.Now.UnixMilli()); err != nil {
				return Outcome{}, err
			}
		case !errors.Is(err, sql.ErrNoRows):
			return Outcome{}, err
		}
	}
	return Outcome{Accepted: true, Result: steer.IssueOwnerSteerView, Events: []Event{{
		ObjectType: "message", ObjectID: p.MessageID, ObjectVersion: seq,
		Kind: "conversation.message", Body: map[string]any{"author": author, "seq": seq},
	}}}, nil
}

// Only originals linked to the exact Issue qualify. A delegated message, an
// external evidence injection, or text that merely quotes the owner cannot
// serve as the original source.
func issueSteerSource(ctx context.Context, q store.Querier, issue, id string) (store.MessageView, int64, bool, error) {
	var revision int64
	var withdrawn bool
	err := q.QueryRowContext(ctx, `SELECT COALESCE(d.content_revision,1),COALESCE(c.withdrawn_at,0)<>0
 FROM conversation_messages m LEFT JOIN message_dispositions d USING(message_id)
 LEFT JOIN conversation_message_controls c USING(message_id)
 WHERE m.message_id=? AND m.author='owner' AND m.kind='message'
 AND EXISTS(SELECT 1 FROM message_work_items w WHERE w.message_id=m.message_id AND w.work_item_id=? AND w.active=1)`,
		id, issue).Scan(&revision, &withdrawn)
	if errors.Is(err, sql.ErrNoRows) {
		return store.MessageView{}, 0, false, protocol.Errorf(protocol.CodeForbidden, "source must be an original owner message linked to the target Issue")
	}
	if err != nil {
		return store.MessageView{}, 0, false, err
	}
	msg, err := store.GetMessage(ctx, q, id)
	return msg, revision, withdrawn, err
}

func isIssueSteerTextSource(ctx context.Context, q store.Querier, source store.MessageView) (bool, error) {
	if strings.TrimSpace(source.Body) == "" {
		return false, nil
	}
	var media bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_images WHERE message_id=?)
 OR EXISTS(SELECT 1 FROM conversation_message_attachments WHERE message_id=?)`, source.MessageID, source.MessageID).Scan(&media)
	return !media, err
}

func issueSteerRunPrincipal(e store.HostedExecution) protocol.Principal {
	return protocol.Principal{Kind: protocol.PrincipalRun, Subject: e.Selection.RunnerRunID,
		Role: protocol.RoleIssueManager, ConversationID: e.ConversationID}
}

func issueSteerTurnAdmitted(ctx context.Context, q store.Querier, execution string) (bool, error) {
	paused, err := upgradePausesMain(ctx, q)
	if err != nil || paused {
		return false, err
	}
	var admitted bool
	err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM logical_turn_executions e
 JOIN logical_turns l USING(logical_turn_id) WHERE e.request_id=? AND e.repair=0 AND l.status='processing')
 AND NOT EXISTS(SELECT 1 FROM native_turn_receipts_pending WHERE request_id=?)`, execution, execution).Scan(&admitted)
	return admitted, err
}

func issueSteerInputDigest(text string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
}
