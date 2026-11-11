package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestRefreshNotificationsCoalesceAndPreserveBusinessHistory(t *testing.T) {
	s, path := openTest(t)
	var first, latest int64
	if err := s.Write(t.Context(), func(tx *Tx) error {
		var err error
		first, err = tx.InsertEvent(t.Context(), EventRow{EventID: "real-before", Source: "app", ObjectType: "task", ObjectID: "task", Kind: "task.completed", BodyJSON: `{"result":"original"}`, CreatedAt: 1})
		if err != nil {
			return err
		}
		for range 100 {
			latest, err = tx.InsertEvent(t.Context(), EventRow{ObjectType: "token_usage", ObjectID: "all", Kind: "token_usage.changed", CreatedAt: 2})
			if err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(t.Context(), path, testkit.NewClock().Now)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Read(t.Context(), func(tx *ReadTx) error {
		var rows, revision, pending, permanent int64
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*),MAX(revision),MAX(pending) FROM refresh_notifications`).Scan(&rows, &revision, &pending); err != nil {
			return err
		}
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM events`).Scan(&permanent); err != nil {
			return err
		}
		if rows != 1 || revision != 100 || pending != 1 || permanent != 1 {
			t.Fatal("refreshes became history or displaced real facts", rows, revision, pending, permanent)
		}
		expired, err := RefreshCursorExpired(t.Context(), tx.Q(), first)
		if err != nil || !expired {
			t.Fatal("removed refresh positions silently served as complete deltas", expired, err)
		}
		current, err := LatestEventSeq(t.Context(), tx.Q())
		if err != nil || current != latest {
			t.Fatal("restart lost notification watermark", current, latest, err)
		}
		business, err := ListEventsAfter(t.Context(), tx.Q(), 0, latest, []string{"task.completed"})
		if err != nil || len(business) != 1 || business[0].BodyJSON != `{"result":"original"}` {
			t.Fatal("resync lost immutable business history", business, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(t.Context(), func(tx *Tx) error {
		if err := tx.AcknowledgeRefreshes(t.Context(), latest-1); err != nil {
			return err
		}
		var pending int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT pending FROM refresh_notifications`).Scan(&pending); err != nil || pending != 1 {
			t.Fatal("stale ACK cleared a newer revision", pending, err)
		}
		if err := tx.AcknowledgeRefreshes(t.Context(), latest); err != nil {
			return err
		}
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT pending FROM refresh_notifications`).Scan(&pending); err != nil || pending != 0 {
			t.Fatal("transport ACK did not clear pending state", pending, err)
		}
		// Direct event insertion also remains beyond compacted refresh cursors.
		_, err := tx.Exec(t.Context(), `INSERT INTO events(event_id,source,object_type,object_id,kind,body_json,created_at) VALUES('real-after','app','issue','issue','issue.closed','{}',3)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Read(t.Context(), func(tx *ReadTx) error {
		rows, err := ListEvents(t.Context(), tx.Q(), latest, 5)
		if err != nil || len(rows) != 1 || rows[0].Seq != latest+1 || rows[0].Kind != "issue.closed" {
			t.Fatal("coalescing reused a business cursor", rows, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRefreshMigrationRetainsExactReasonsAndUnmatchedInboxFacts(t *testing.T) {
	ctx := t.Context()
	db, err := sql.Open("sqlite", dsn(filepath.Join(t.TempDir(), "historical.db"), true))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	s := &Store{write: db, read: db, now: testkit.NewClock().Now}
	if err := s.migrate(ctx, 118); err != nil {
		t.Fatal(err)
	}
	issue := canonicalIssueFixture("wi_00000000000000000000000001")
	unique := canonicalIssueFixture("wi_00000000000000000000000002")
	if err := s.Write(ctx, func(tx *Tx) error {
		if err := tx.SaveCanonicalIssue(ctx, issue, 0, issueProvenance()); err != nil {
			return err
		}
		if err := tx.SaveCanonicalIssue(ctx, unique, 0, issueProvenance()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE work_item_revisions SET reason_json='"unique historical reason"' WHERE work_item_id=?`, unique.IssueID); err != nil {
			return err
		}
		if err := tx.InsertTask(ctx, TaskRow{TaskID: "task_original", Title: "Original", Project: "default", Lifecycle: protocol.TaskCompleted, ControlMode: protocol.ControlManaged, ControlRevision: 1, SpecVersion: 1}); err != nil {
			return err
		}
		if err := tx.InsertAction(ctx, ActionRow{ActionID: "action_original", TaskID: "task_original", Kind: protocol.ActionImplement, EffectKey: "original", TriggerJSON: `{}`, TargetSpecVersion: 1, AuthorizationJSON: `{}`, Generation: 1, Status: protocol.ActionDone, CreatedRequestID: "request_original"}); err != nil {
			return err
		}
		if err := tx.InsertRun(ctx, RunRow{RunID: "run_original", TaskID: "task_original", ActionID: "action_original", Generation: 1, Role: protocol.RoleImplement, DesiredState: protocol.DesiredStopped, ObservedState: protocol.RunFinished}); err != nil {
			return err
		}
		if err := tx.InsertRunLog(ctx, "run_original", protocol.RunEvent{Seq: 7, Kind: "item.completed", Body: []byte(`{"original":true}`)}, 1); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO inbox VALUES('run:run_original','7',1),('run:run_original','8',1),('root-attention:run_original','managed:500',1)`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO events(event_id,source,object_type,object_id,kind,body_json,created_at) VALUES('attention-original','runner','run','run_original','run.attention_required','{"managed_turn_at":500,"turn_at":4,"background_pending":true}',1)`); err != nil {
			return err
		}
		for i, row := range []struct{ kind, body string }{{"task.completed", `{"kept":"exact"}`}, {"token_usage.changed", `{}`}, {"host.runtime_changed", `{}`}, {"token_usage.changed", `{}`}} {
			if _, err := tx.Exec(ctx, `INSERT INTO events(event_id,source,object_type,object_id,kind,body_json,created_at) VALUES(?,'app','test','scope',?,?,1)`, fmt.Sprint("original-", i), row.kind, row.body); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO inbox VALUES('run:missing','7',1)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(ctx, 121); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(ctx, 121); err != nil {
		t.Fatal("migration not restartable", err)
	}
	if err := s.Read(ctx, func(tx *ReadTx) error {
		reason, err := IssueRevisionReason(ctx, tx.Q(), issue.IssueID, 1)
		if err != nil || reason != `"owner request"` {
			t.Fatal("reason reference lost original bytes", reason, err)
		}
		legacy, err := IssueRevisionReason(ctx, tx.Q(), unique.IssueID, 1)
		if err != nil || legacy != `"unique historical reason"` {
			t.Fatal("unequal legacy reason was discarded", legacy, err)
		}
		var inline string
		var ref, notices, facts, inbox int
		if err := tx.Q().QueryRowContext(ctx, `SELECT reason_json,reason_control_revision FROM work_item_revisions WHERE work_item_id=?`, issue.IssueID).Scan(&inline, &ref); err != nil {
			return err
		}
		if err := tx.Q().QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM refresh_notifications),(SELECT COUNT(*) FROM events),(SELECT COUNT(*) FROM inbox)`).Scan(&notices, &facts, &inbox); err != nil {
			return err
		}
		if inline != "" || ref != 1 || notices != 2 || facts != 2 || inbox != 2 {
			t.Fatal("migration retained duplicate reason or discarded unique facts", inline, ref, notices, facts, inbox)
		}
		var attentionKey string
		if err := tx.Q().QueryRowContext(ctx, `SELECT run_attention_key FROM events WHERE event_id='attention-original'`).Scan(&attentionKey); err != nil || attentionKey != "managed:500" {
			t.Fatal("historical attention lost its actual root-turn key", attentionKey, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	issue.Revision, issue.UpdatedAt = 2, 2
	saveCanonicalFixture(t, s, issue, 1)
	if err := s.Read(ctx, func(tx *ReadTx) error {
		var size int
		if err := tx.Q().QueryRowContext(ctx, `SELECT length(reason_json) FROM work_item_revisions WHERE work_item_id=? AND revision=2`, issue.IssueID).Scan(&size); err != nil {
			return err
		}
		if size != 0 {
			t.Fatal("new canonical versions still duplicate reason", size)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRefreshMigrationPreservesUniqueRetiredFactsWithoutUpgradeGate(t *testing.T) {
	for _, table := range []string{"agent_sessions", "background_assessments"} {
		t.Run(table, func(t *testing.T) {
			db, err := sql.Open("sqlite", dsn(filepath.Join(t.TempDir(), "historical.db"), true))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			db.SetMaxOpenConns(1)
			s := &Store{write: db, read: db, now: testkit.NewClock().Now}
			if err := s.migrate(t.Context(), 120); err != nil {
				t.Fatal(err)
			}
			statement := `INSERT INTO agent_sessions(agent,session_id,client_id,first_seen_at,last_seen_at) VALUES('agent','session','client',1,2)`
			if table == "background_assessments" {
				statement = `INSERT INTO background_assessments(assessment_id,conversation_id,delivery_id,context_revision,disposition,note,evidence_json,created_at) VALUES('original','main','delivery',1,'reviewed','unique original','{}',1)`
			}
			if _, err := db.ExecContext(t.Context(), statement); err != nil {
				t.Fatal(err)
			}
			if err := s.migrate(t.Context(), 121); err != nil {
				t.Fatal("unique retired history became an upgrade prerequisite", err)
			}
			var count int
			if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil || count != 1 {
				t.Fatal("migration lost the historical original", count, err)
			}
		})
	}
}
