package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type IssueOwnerSteerDispatch struct {
	MessageID string
	Text      string
}

func (a *App) ClaimIssueOwnerSteer(ctx context.Context, execution, run, thread, turn string, supported bool) (IssueOwnerSteerDispatch, error) {
	var dispatch IssueOwnerSteerDispatch
	var message string
	next := func(q store.Querier) error {
		return q.QueryRowContext(ctx, `SELECT s.message_id FROM issue_owner_steers s
 JOIN conversation_messages m USING(message_id)
 WHERE s.execution_id=? AND s.run_id=? AND s.thread_id=? AND s.turn_id=? AND s.status='queued'
 ORDER BY s.revokes_source_message_id IS NULL,m.seq LIMIT 1`,
			execution, run, thread, turn).Scan(&message)
	}
	// The active delivery loop already ticks. Idle checks stay off the writer.
	err := a.store.Read(ctx, func(tx *store.ReadTx) error { return next(tx.Q()) })
	if errors.Is(err, sql.ErrNoRows) {
		return dispatch, nil
	}
	if err != nil {
		return dispatch, err
	}
	var changed bool
	err = a.write(ctx, func(tx *store.Tx) error {
		// A newly committed revocation can preempt a queued notice before
		// native dispatch begins. Select again in the write transaction.
		err := next(tx.Q())
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		s, err := store.GetIssueOwnerSteer(ctx, tx.Q(), message)
		if err != nil {
			return err
		}
		now := a.now().UnixMilli()
		reason, err := issueSteerClaimBlocked(ctx, tx.Q(), s, now)
		if err != nil {
			return err
		}
		status := "sending"
		if reason != "" {
			status = "rejected"
		} else if !supported {
			status, reason = "unsupported", "executor_unsupported"
		}
		if err := writeIssueSteerState(ctx, tx, a.ids, s.MessageID, status, reason, now); err != nil {
			return err
		}
		if status == "sending" {
			notice, err := store.GetMessage(ctx, tx.Q(), s.MessageID)
			if err != nil {
				return err
			}
			dispatch = IssueOwnerSteerDispatch{MessageID: s.MessageID, Text: s.InputHeader + notice.Body}
		}
		changed = true
		return nil
	})
	if err == nil && changed {
		a.notifyChange()
	}
	return dispatch, err
}

func issueSteerClaimBlocked(ctx context.Context, q store.Querier, s store.IssueOwnerSteer, now int64) (string, error) {
	e, err := store.GetHostedExecution(ctx, q, s.ExecutionID)
	if err != nil {
		return "", err
	}
	if err := authorizeRunnerCoordination(ctx, q, issueSteerRunPrincipal(e), e, true, now); err != nil {
		var denied *protocol.Error
		if errors.As(err, &denied) && denied.Code == protocol.CodeForbidden {
			return "owner_fenced", nil
		}
		return "", err
	}
	t, err := store.GetMainTurn(ctx, q, s.ExecutionID)
	if err != nil {
		return "", err
	}
	if t.Status != store.TurnStarted || t.ThreadID != s.ThreadID || t.TurnID != s.TurnID {
		return "turn_ended", nil
	}
	if ok, err := issueSteerTurnAdmitted(ctx, q, s.ExecutionID); err != nil {
		return "", err
	} else if !ok {
		return "turn_fenced", nil
	}
	source, revision, withdrawn, err := issueSteerSource(ctx, q, s.IssueID, s.SourceMessageID)
	if err != nil {
		var denied *protocol.Error
		if errors.As(err, &denied) && denied.Code == protocol.CodeForbidden {
			return "source_changed", nil
		}
		return "", err
	}
	if revision != s.SourceRevision || withdrawn || issueSteerInputDigest(s.InputHeader+source.Body) != s.InputSHA256 {
		return "source_changed", nil
	}
	var revoked bool
	err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issue_owner_steers
 WHERE issue_id=? AND revokes_source_message_id=?)`, s.IssueID, s.SourceMessageID).Scan(&revoked)
	if revoked {
		return "source_revoked", err
	}
	return "", err
}

// RecordIssueOwnerSteerAnswer retains the original attempt even when a native
// user item has already arrived. A late RPC cannot erase known consumption.
func (a *App) RecordIssueOwnerSteerAnswer(ctx context.Context, message, status, reason string) error {
	err := a.write(ctx, func(tx *store.Tx) error {
		s, err := store.GetIssueOwnerSteer(ctx, tx.Q(), message)
		if err != nil {
			return err
		}
		return recordIssueOwnerSteerAnswer(ctx, tx, a.ids, s, status, reason, a.now().UnixMilli())
	})
	if err == nil {
		a.notifyChange()
	}
	return err
}

func bindIssueOwnerSteerRequest(ctx context.Context, tx *store.Tx, run string, request protocol.ManagerNativeRequest) error {
	var payload struct {
		ThreadID string `json:"thread_id"`
		TurnID   string `json:"turn_id"`
		Input    struct {
			Text, UserMessageID string
		} `json:"input"`
	}
	if err := json.Unmarshal(request.Payload, &payload); err != nil {
		return err
	}
	s, err := store.GetIssueOwnerSteer(ctx, tx.Q(), payload.Input.UserMessageID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if s.Status != "sending" || s.NativeRequestID != "" || s.RunID != run || s.ThreadID != payload.ThreadID ||
		s.TurnID != payload.TurnID || issueSteerInputDigest(payload.Input.Text) != s.InputSHA256 {
		return protocol.Errorf(protocol.CodeInvalidTransition, "urgent input is already bound or its native target changed")
	}
	_, err = tx.Exec(ctx, `UPDATE issue_owner_steers SET native_request_id=? WHERE message_id=?`, request.ID, s.MessageID)
	return err
}

func recordIssueOwnerSteerRPC(ctx context.Context, tx *store.Tx, ids protocol.IDGen, run, request, status, reason string, now int64) error {
	var message string
	err := tx.Q().QueryRowContext(ctx, `SELECT message_id FROM issue_owner_steers
 WHERE native_request_id=? AND run_id=?`, request, run).Scan(&message)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	s, err := store.GetIssueOwnerSteer(ctx, tx.Q(), message)
	if err != nil {
		return err
	}
	return recordIssueOwnerSteerAnswer(ctx, tx, ids, s, status, reason, now)
}

func recordIssueOwnerSteerAnswer(ctx context.Context, tx *store.Tx, ids protocol.IDGen, s store.IssueOwnerSteer, status, reason string, now int64) error {
	if status == "accepted" && s.AcceptedAt == 0 {
		if _, err := tx.Exec(ctx, `UPDATE issue_owner_steers SET accepted_at=? WHERE message_id=?`, now, s.MessageID); err != nil {
			return err
		}
	}
	if s.Status == "sending" || s.Status == "unknown" && status != "accepted" && (s.Status != status || s.Reason != reason) {
		return writeIssueSteerState(ctx, tx, ids, s.MessageID, status, reason, now)
	}
	return nil
}

func (a *App) ConsumeIssueOwnerSteer(ctx context.Context, conversation, thread, turn, text, item string) error {
	if item == "" || text == "" || turn == "" {
		return nil
	}
	var message string
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(ctx, `SELECT s.message_id FROM issue_owner_steers s
 JOIN conversation_messages m USING(message_id) WHERE m.conversation_id=? AND s.thread_id=?
 AND s.turn_id=? AND s.input_sha256=? AND s.status IN ('sending','accepted','unknown')`,
			conversation, thread, turn, issueSteerInputDigest(text)).Scan(&message)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	err = a.write(ctx, func(tx *store.Tx) error {
		res, err := tx.Exec(ctx, `UPDATE issue_owner_steers SET native_item_id=? WHERE message_id=?
 AND status IN ('sending','accepted','unknown')`, item, message)
		if err != nil {
			return err
		}
		updated, err := res.RowsAffected()
		if err != nil || updated == 0 {
			return err
		}
		return writeIssueSteerState(ctx, tx, a.ids, message, "consumed", "", a.now().UnixMilli())
	})
	if err == nil {
		a.notifyChange()
	}
	return err
}

func settleIssueOwnerSteers(ctx context.Context, tx *store.Tx, ids protocol.IDGen, execution, run string, now int64) error {
	messages, err := store.FollowupIDs(ctx, tx.Q(), `SELECT message_id FROM issue_owner_steers
 WHERE (execution_id=? OR run_id=?) AND status IN ('queued','sending','accepted')`, execution, run)
	if err != nil {
		return err
	}
	for _, id := range messages {
		s, err := store.GetIssueOwnerSteer(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		status, reason := "unknown", "no_consumption_receipt"
		if s.Status == "queued" {
			status, reason = "rejected", "turn_ended"
		}
		if err := writeIssueSteerState(ctx, tx, ids, id, status, reason, now); err != nil {
			return err
		}
	}
	return nil
}

func writeIssueSteerState(ctx context.Context, tx *store.Tx, ids protocol.IDGen, id, status, reason string, now int64) error {
	if _, err := tx.Exec(ctx, `UPDATE issue_owner_steers SET status=?,reason=? WHERE message_id=?`, status, reason, id); err != nil {
		return err
	}
	// Refresh the public item using the normal message event path. The steer
	// projection carries its own status independently of ordinary processing.
	return recordMessageState(ctx, tx, ids, id, steerState(status), now)
}
