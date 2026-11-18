package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// currentIssueWriter uses execution provenance already checked by the command.
// The effect identity can span Runs and must not be used as an execution key.
func currentIssueWriter(x *Exec, issue protocol.Issue) bool {
	e := x.coordinationExecution
	return e != nil && e.IssueID == issue.IssueID && e.AssignmentEpoch == issue.AssignmentEpoch
}

// recordIssueChangeEvent observes committed business drafts in the same writer
// transaction. It adds no dispatch gate and never consumes a pending external
// change merely because the owner also wrote progress in its current turn.
func recordIssueChangeEvent(ctx context.Context, x *Exec, tx *store.Tx, event Event, eventID string) error {
	if event.ObjectType != "issue" && event.ObjectType != "work_item" {
		return nil
	}
	if event.Kind == "work_item.delivery_checked" {
		// An unchanged provider snapshot advances observation time only. It is
		// not a new Issue fact and must not keep resetting the owner's window.
		return nil
	}
	var canonical bool
	if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issue_controls WHERE issue_id=?)`, event.ObjectID).Scan(&canonical); err != nil || !canonical {
		return err
	}
	issue, err := store.GetCanonicalIssue(ctx, tx.Q(), event.ObjectID)
	if err != nil {
		return err
	}
	if issue.Assignee.Kind != protocol.IssueAssigneeAgent {
		return nil
	}
	self := currentIssueWriter(x, issue)
	kind := event.Kind
	if self {
		return nil
	}
	// Status writes already have their reversible coalescing window. Keep that
	// protocol and avoid creating a second wake for the same status event.
	if kind == "issue.status_changed" || kind == "issue.created" {
		return nil
	}
	settings, err := store.GetIssueNotificationSettings(ctx, tx.Q())
	if err != nil {
		return err
	}
	var sequence int64
	if err := tx.Q().QueryRowContext(ctx, `SELECT event_seq FROM events WHERE event_id=?`, eventID).Scan(&sequence); err != nil {
		return err
	}
	now := x.Now.UnixMilli()
	if _, err := tx.Exec(ctx, `INSERT INTO issue_change_notifications(issue_id,event_id,first_event_seq,event_seq,kind,issue_revision,assignment_epoch,last_changed_at,due_at) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(issue_id) DO UPDATE SET event_id=excluded.event_id,first_event_seq=CASE WHEN issue_change_notifications.due_at=0 THEN excluded.first_event_seq ELSE issue_change_notifications.first_event_seq END,event_seq=excluded.event_seq,kind=excluded.kind,issue_revision=excluded.issue_revision,assignment_epoch=excluded.assignment_epoch,last_changed_at=excluded.last_changed_at,due_at=excluded.due_at`, issue.IssueID, eventID, sequence, sequence, kind, issue.Revision, issue.AssignmentEpoch, now, now+int64(settings.DelayMinutes)*60000); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE issue_status_notifications SET due_at=? WHERE issue_id=? AND due_at>0`, now+int64(settings.DelayMinutes)*60000, issue.IssueID); err != nil {
		return err
	}
	if settings.DelayMinutes == 0 {
		return flushIssueChangeNotice(ctx, x, tx, issue.IssueID)
	}
	return nil
}

func flushIssueChangeNotice(ctx context.Context, x *Exec, tx *store.Tx, id string) error {
	var event, kind string
	var first, last, revision, epoch, changed, due int64
	err := tx.Q().QueryRowContext(ctx, `SELECT event_id,first_event_seq,event_seq,kind,issue_revision,assignment_epoch,last_changed_at,due_at FROM issue_change_notifications WHERE issue_id=?`, id).Scan(&event, &first, &last, &kind, &revision, &epoch, &changed, &due)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (due == 0 || due > x.Now.UnixMilli())) {
		return nil
	}
	if err != nil {
		return err
	}
	issue, err := store.GetCanonicalIssue(ctx, tx.Q(), id)
	if err != nil {
		return err
	}
	if issue.Assignee.Kind != protocol.IssueAssigneeAgent {
		_, err = tx.Exec(ctx, `DELETE FROM issue_change_notifications WHERE issue_id=?`, id)
		return err
	}
	if issue.AssignmentState != "active" {
		_, err = tx.Exec(ctx, `UPDATE issue_change_notifications SET due_at=? WHERE issue_id=?`, x.Now.UnixMilli()+30000, id)
		return err
	}
	conversation, err := ensureCanonicalManagerConversationForWake(ctx, x, tx, id)
	if err != nil || conversation == "" {
		return err
	}
	message := x.IDs.New(protocol.PrefixMessage)
	body := fmt.Sprintf("Issue「%s」已有合并后的变更通知（事件 %d–%d，最新 %s，记录版本 %d / 责任代次 %d）。读取当前 Issue、来源和任务，判断这是状态/进度变化还是实际任务约定变化。现有任务默认沿原 spec 继续，不需要逐 Task 重新确认适用性；负责人判断确需改变任务时正式修订或明确暂停，保留实际理由。通知不授予新权限，不修改已有 spec，也不授权重发 unknown 操作。", issue.Plan.Title, first, last, kind, revision, epoch)
	seq, err := tx.InsertMessage(ctx, store.MessageRow{ConversationID: conversation, MessageID: message, Author: "system", Body: body, CreatedAt: x.Now.UnixMilli()})
	if err != nil {
		return err
	}
	wakeEvent := x.IDs.New(protocol.PrefixEvent)
	if _, err := tx.InsertEvent(ctx, store.EventRow{EventID: wakeEvent, Source: "app", ObjectType: "message", ObjectID: message, ObjectVersion: seq, Kind: "conversation.message", BodyJSON: `{"author":"system","issue_change":true}`, RequestID: x.Env.RequestID, TraceID: x.TraceID, CreatedAt: x.Now.UnixMilli()}); err != nil {
		return err
	}
	if err := tx.BindMessage(ctx, message, "event", event, "issue_change"); err != nil {
		return err
	}
	data, err := json.Marshal(map[string]any{"issue_id": id, "first_event_seq": first, "event_seq": last, "issue_revision": revision, "assignment_epoch": epoch, "changed_at": changed, "kind": kind})
	if err != nil {
		return err
	}
	// Share the existing reversible-change batch, while keeping initial
	// assignment and ordinary business wakes independently required.
	if err := appendBackgroundSource(ctx, x, tx, "issue_status:"+id, id, message, wakeEvent, "issue.changed", data); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE issue_change_notifications SET due_at=0 WHERE issue_id=?`, id)
	return err
}

func sweepIssueChangeNotices(ctx context.Context, x *Exec, tx *store.Tx) (int, error) {
	ids, err := store.FollowupIDs(ctx, tx.Q(), `SELECT issue_id FROM issue_change_notifications WHERE due_at>0 AND due_at<=? ORDER BY due_at,issue_id LIMIT 100`, x.Now.UnixMilli())
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		if err := flushIssueChangeNotice(ctx, x, tx, id); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}
