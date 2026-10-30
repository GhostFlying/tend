package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type issueStatusNotice struct {
	IssueID     string                   `json:"issue_id"`
	Baseline    protocol.IssueWorkStatus `json:"baseline"`
	Latest      protocol.IssueWorkStatus `json:"status"`
	LastChanged int64                    `json:"changed_at"`
	Due         int64                    `json:"due_at"`
}

func readIssueStatusNotice(ctx context.Context, q store.Querier, id string) (issueStatusNotice, error) {
	out := issueStatusNotice{IssueID: id}
	err := q.QueryRowContext(ctx, `SELECT baseline,latest,last_changed_at,due_at FROM issue_status_notifications WHERE issue_id=?`, id).Scan(&out.Baseline, &out.Latest, &out.LastChanged, &out.Due)
	return out, err
}

func recordIssueStatusChange(ctx context.Context, x *Exec, tx *store.Tx, before, after protocol.Issue) error {
	pending, err := readIssueStatusNotice(ctx, tx.Q(), after.IssueID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	queued, err := cancelQueuedIssueStatusNotice(ctx, x, tx, after.IssueID)
	if err != nil {
		return err
	}
	if x.coordinationExecution != nil || x.Principal.Kind != protocol.PrincipalOwner {
		_, err = tx.Exec(ctx, `DELETE FROM issue_status_notifications WHERE issue_id=?`, after.IssueID)
		return err
	}
	baseline := before.BoardStatus()
	if pending.Due > 0 || queued {
		baseline = pending.Baseline
	}
	if baseline == after.BoardStatus() {
		_, err = tx.Exec(ctx, `DELETE FROM issue_status_notifications WHERE issue_id=?`, after.IssueID)
		return err
	}
	settings, err := store.GetIssueNotificationSettings(ctx, tx.Q())
	if err != nil {
		return err
	}
	now := x.Now.UnixMilli()
	_, err = tx.Exec(ctx, `INSERT INTO issue_status_notifications(issue_id,baseline,latest,last_changed_at,due_at) VALUES(?,?,?,?,?) ON CONFLICT(issue_id) DO UPDATE SET baseline=excluded.baseline,latest=excluded.latest,last_changed_at=excluded.last_changed_at,due_at=excluded.due_at`, after.IssueID, baseline, after.BoardStatus(), now, now+int64(settings.DelayMinutes)*60000)
	if err != nil {
		return err
	}
	if settings.DelayMinutes == 0 {
		return flushIssueStatusNotice(ctx, x, tx, after.IssueID)
	}
	return nil
}

// Only sources in an unfrozen batch may be replaced. The delivery claim freezes
// its batch in this same database serialization boundary; sending/unknown and
// already consumed inputs are never edited or retried here.
func cancelQueuedIssueStatusNotice(ctx context.Context, x *Exec, tx *store.Tx, id string) (bool, error) {
	batches, err := store.FollowupIDs(ctx, tx.Q(), `SELECT DISTINCT b.batch_id FROM background_batches b JOIN background_sources s USING(batch_id) WHERE b.group_key=? AND b.status='pending' AND s.kind='issue.status_changed' AND s.cancelled_at=0`, "issue_status:"+id)
	if err != nil {
		return false, err
	}
	for _, batch := range batches {
		if _, err = tx.Exec(ctx, `UPDATE background_sources SET cancelled_at=? WHERE batch_id=? AND kind='issue.status_changed' AND cancelled_at=0`, x.Now.UnixMilli(), batch); err != nil {
			return false, err
		}
		var remaining int
		var message string
		if err = tx.Q().QueryRowContext(ctx, `SELECT message_id,(SELECT COUNT(*) FROM background_sources WHERE batch_id=? AND cancelled_at=0) FROM background_batches WHERE batch_id=?`, batch, batch).Scan(&message, &remaining); err != nil {
			return false, err
		}
		if remaining == 0 {
			if _, err = tx.Exec(ctx, `UPDATE background_batches SET status='cancelled' WHERE batch_id=?`, batch); err != nil {
				return false, err
			}
			if _, err = tx.Exec(ctx, `UPDATE outbox SET status='cancelled',updated_at=? WHERE status='pending' AND destination LIKE 'background_thread:%' AND json_extract(payload_json,'$.message_id')=?`, x.Now.UnixMilli(), message); err != nil {
				return false, err
			}
			if _, err = tx.InsertEvent(ctx, store.EventRow{EventID: x.IDs.New(protocol.PrefixEvent), Source: "app", ObjectType: "message", ObjectID: message, Kind: "conversation.message_state", BodyJSON: `{"state":"interrupted"}`, RequestID: x.Env.RequestID, CreatedAt: x.Now.UnixMilli()}); err != nil {
				return false, err
			}
		}
	}
	return len(batches) > 0, nil
}

func flushIssueStatusNotice(ctx context.Context, x *Exec, tx *store.Tx, id string) error {
	notice, err := readIssueStatusNotice(ctx, tx.Q(), id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if notice.Due == 0 || notice.Due > x.Now.UnixMilli() {
		return nil
	}
	issue, err := store.GetCanonicalIssue(ctx, tx.Q(), id)
	if err != nil {
		return err
	}
	if issue.BoardStatus() != notice.Latest || issue.Assignee.Kind != protocol.IssueAssigneeAgent {
		_, err = tx.Exec(ctx, `DELETE FROM issue_status_notifications WHERE issue_id=?`, id)
		return err
	}
	if issue.AssignmentState != "active" {
		_, err = tx.Exec(ctx, `UPDATE issue_status_notifications SET due_at=? WHERE issue_id=?`, x.Now.UnixMilli()+30000, id)
		return err
	}
	conversation, err := ensureCanonicalManagerConversationForWake(ctx, x, tx, id)
	if err != nil {
		return err
	}
	message, event := x.IDs.New(protocol.PrefixMessage), x.IDs.New(protocol.PrefixEvent)
	body := fmt.Sprintf("用户调整了 Issue「%s」的工作状态，最终状态为 %s。连续调整已合并。读取当前 Issue 与已有授权后处理；工作状态不代表 worker 正在执行，也不授权额外合入或部署。", issue.Plan.Title, issueStatusLabel(notice.Latest))
	seq, err := tx.InsertMessage(ctx, store.MessageRow{ConversationID: conversation, MessageID: message, Author: "system", Body: body, CreatedAt: x.Now.UnixMilli()})
	if err != nil {
		return err
	}
	if _, err = tx.InsertEvent(ctx, store.EventRow{EventID: event, Source: "app", ObjectType: "message", ObjectID: message, ObjectVersion: seq, Kind: "conversation.message", BodyJSON: `{"author":"system","issue_status":true}`, RequestID: x.Env.RequestID, TraceID: x.TraceID, CreatedAt: x.Now.UnixMilli()}); err != nil {
		return err
	}
	data, _ := json.Marshal(map[string]any{"issue_id": id, "status": notice.Latest, "changed_at": notice.LastChanged})
	// Keep reversible status input separate from ordinary wakes, including the
	// manager's source-less initial assignment batch. Returning to the baseline
	// must never retire that independently required wake.
	if err = appendBackgroundSource(ctx, x, tx, "issue_status:"+id, id, message, event, "issue.status_changed", data); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE issue_status_notifications SET due_at=0 WHERE issue_id=?`, id)
	return err
}

func issueStatusLabel(status protocol.IssueWorkStatus) string {
	return map[protocol.IssueWorkStatus]string{protocol.IssueBacklog: "待安排", protocol.IssueTodo: "待推进", protocol.IssueInProgress: "进行中", protocol.IssueInReview: "待检查", protocol.IssueDone: "已完成", protocol.IssueWorkCancelled: "已取消"}[status]
}

type SetIssueNotificationSettings struct{}

func (SetIssueNotificationSettings) Operation() string { return "issues.set_notification_settings" }
func (SetIssueNotificationSettings) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}
func (SetIssueNotificationSettings) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	if x.Env.Target.Type != "issue_settings" || x.Env.Target.ID != "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "target must be shared Issue settings")
	}
	var p struct {
		DelayMinutes *int `json:"delay_minutes"`
	}
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if p.DelayMinutes == nil || *p.DelayMinutes < 0 || *p.DelayMinutes > 30 {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "delay_minutes must be between 0 and 30")
	}
	expected, err := requireRevision(x.Env)
	if err != nil {
		return Outcome{}, err
	}
	settings, err := store.GetIssueNotificationSettings(ctx, tx.Q())
	if err != nil {
		return Outcome{}, err
	}
	if expected != settings.Revision {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "Issue notification settings have changed")
	}
	if settings.DelayMinutes == *p.DelayMinutes {
		return Outcome{Result: settings}, nil
	}
	settings.DelayMinutes = *p.DelayMinutes
	settings.Revision++
	if _, err = tx.Exec(ctx, `UPDATE issue_notification_settings SET delay_minutes=?,revision=?,updated_at=? WHERE singleton=1`, settings.DelayMinutes, settings.Revision, x.Now.UnixMilli()); err != nil {
		return Outcome{}, err
	}
	ids, err := store.FollowupIDs(ctx, tx.Q(), `SELECT issue_id FROM issue_status_notifications ORDER BY issue_id`)
	if err != nil {
		return Outcome{}, err
	}
	for _, id := range ids {
		notice, err := readIssueStatusNotice(ctx, tx.Q(), id)
		if err != nil {
			return Outcome{}, err
		}
		if notice.Due == 0 {
			queued, err := cancelQueuedIssueStatusNotice(ctx, x, tx, id)
			if err != nil {
				return Outcome{}, err
			}
			if !queued {
				continue
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE issue_status_notifications SET due_at=? WHERE issue_id=?`, notice.LastChanged+int64(settings.DelayMinutes)*60000, id); err != nil {
			return Outcome{}, err
		}
		if err = flushIssueStatusNotice(ctx, x, tx, id); err != nil {
			return Outcome{}, err
		}
	}
	return Outcome{Result: settings, Notify: true, Events: []Event{{ObjectType: "issue_settings", ObjectID: "notifications", ObjectVersion: settings.Revision, Kind: "issue.notification_settings_changed"}}}, nil
}

type SweepIssueStatusNotifications struct{}

func (SweepIssueStatusNotifications) Operation() string { return "issues.sweep_status_notifications" }
func (SweepIssueStatusNotifications) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalSystem)
}
func (SweepIssueStatusNotifications) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	ids, err := store.FollowupIDs(ctx, tx.Q(), `SELECT issue_id FROM issue_status_notifications WHERE due_at>0 AND due_at<=? ORDER BY due_at,issue_id LIMIT 100`, x.Now.UnixMilli())
	if err != nil {
		return Outcome{}, err
	}
	for _, id := range ids {
		if err = flushIssueStatusNotice(ctx, x, tx, id); err != nil {
			return Outcome{}, err
		}
	}
	return Outcome{NoEffect: len(ids) == 0, Notify: len(ids) > 0}, nil
}

// RunIssueStatusNotifications discovers due persisted windows without keeping
// request identity or timers per Issue in memory. It never invokes a model.
func (a *App) RunIssueStatusNotifications(ctx context.Context, log *slog.Logger) {
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for ctx.Err() == nil {
		var due bool
		err := a.store.Read(ctx, func(tx *store.ReadTx) error {
			return tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issue_status_notifications WHERE due_at>0 AND due_at<=?)`, a.now().UnixMilli()).Scan(&due)
		})
		if err == nil && due {
			command := SweepIssueStatusNotifications{}
			env := &protocol.Envelope{RequestID: a.ids.New(protocol.PrefixRequest), Operation: command.Operation(), Target: protocol.Target{Type: "issue_settings"}, Payload: json.RawMessage(`{}`)}
			var op protocol.Operation
			op, err = a.Execute(ctx, protocol.Principal{Kind: protocol.PrincipalSystem}, env, command)
			if err == nil && op.Error != nil {
				err = op.Error
			}
		}
		if err != nil && ctx.Err() == nil {
			log.WarnContext(ctx, "could not reconcile Issue status notifications", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}
