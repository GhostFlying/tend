package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestSharedDeliveryMigrationPreservesOriginalApprovalAndExecution(t *testing.T) {
	ctx := t.Context()
	clock := testkit.NewClock()
	path := filepath.Join(t.TempDir(), "schema97.db")
	db, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	old := &Store{write: db, read: db, now: clock.Now}
	// Build the real deployed schema, rather than lowering a current database's
	// version and leaving the replacement tables or triggers in place.
	if err := old.migrate(ctx, 97); err != nil {
		t.Fatal(err)
	}
	issue := canonicalIssueFixture("wi_00000000000000000000000001")
	issue.Plan.Deliveries = []protocol.IssueDelivery{{DeliveryID: "pr", Provider: "github", Repository: "example/repo", Number: 17, URL: "https://example.invalid/repo/pull/17"}}
	candidate := protocol.IssueDeliveryCandidate{DeliveryID: "pr", URL: issue.Plan.Deliveries[0].URL, HeadSHA: strings.Repeat("a", 40), BaseSHA: strings.Repeat("b", 40), TargetSHA: strings.Repeat("b", 40), ProviderState: "open", Validation: "Original accepted head and repository checks", DeploymentScope: "Merge only; no deployment"}
	frozen, err := json.MarshalIndent(struct {
		protocol.IssueDeliveryCandidate
		AssignmentEpoch int64 `json:"assignment_epoch"`
	}{candidate, issue.AssignmentEpoch}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	duty := protocol.IssueDeliveryDuty{TaskID: "task_original", EventID: "evt_original", AcceptanceID: "acc_original", Revision: 3, Status: "executing", AssignmentEpoch: issue.AssignmentEpoch, Candidate: &candidate, AttentionID: "attention_original", ExecutionTaskID: "task_delivery", Reason: "Execute the original owner's approved candidate"}
	if err := old.Write(ctx, func(tx *Tx) error {
		if err := tx.SaveCanonicalIssue(ctx, issue, 0, issueProvenance()); err != nil {
			return err
		}
		for _, task := range []TaskRow{
			{TaskID: duty.TaskID, Title: "Original implementation", Project: "default", Lifecycle: protocol.TaskCompleted, ControlMode: protocol.ControlManaged, ControlRevision: 7, SpecVersion: 2, CreatedAt: 1, UpdatedAt: 2},
			{TaskID: duty.ExecutionTaskID, Title: "Approved delivery", Project: "default", Lifecycle: protocol.TaskWaiting, ControlMode: protocol.ControlManaged, ControlRevision: 1, SpecVersion: 1, CreatedAt: 3, UpdatedAt: 3},
		} {
			if err := tx.InsertTask(ctx, task); err != nil {
				return err
			}
			if task.SpecVersion > 1 {
				if err := tx.InsertSpec(ctx, task.TaskID, 1, `{"goal":"Original first agreement","delivery":"commit"}`, "", 1); err != nil {
					return err
				}
			}
			if err := tx.InsertSpec(ctx, task.TaskID, task.SpecVersion, `{ "goal": "Preserve the original frozen scope", "delivery": "commit" }`, "", 1); err != nil {
				return err
			}
			if err := tx.InsertTaskIssueOwner(ctx, task.TaskID, issue.IssueID); err != nil {
				return err
			}
			actionStatus := protocol.ActionDone
			if task.TaskID == duty.ExecutionTaskID {
				actionStatus = protocol.ActionDispatched
			}
			if err := tx.InsertAction(ctx, ActionRow{ActionID: "act_" + task.TaskID, TaskID: task.TaskID, Kind: protocol.ActionImplement, EffectKey: task.TaskID, TriggerJSON: `{}`, TargetSpecVersion: task.SpecVersion, AuthorizationJSON: `{}`, Generation: 1, Status: actionStatus, CreatedRequestID: "req_original", CreatedAt: 1}); err != nil {
				return err
			}
			state := protocol.RunFinished
			if task.TaskID == duty.ExecutionTaskID {
				state = protocol.RunUnknown
			}
			if err := tx.InsertRun(ctx, RunRow{RunID: "run_" + task.TaskID, TaskID: task.TaskID, ActionID: "act_" + task.TaskID, Generation: 1, Role: protocol.RoleImplement, DesiredState: protocol.DesiredStopped, ObservedState: state, CreatedAt: 1}); err != nil {
				return err
			}
		}
		if err := tx.InsertAction(ctx, ActionRow{ActionID: "act_accept_original", TaskID: duty.TaskID, Kind: protocol.ActionAccept, EffectKey: "accept_original", TriggerJSON: `{}`, TargetSpecVersion: 2, AuthorizationJSON: `{}`, Generation: 1, Status: protocol.ActionDone, CreatedRequestID: "req_original", CreatedAt: 2}); err != nil {
			return err
		}
		if err := tx.InsertRun(ctx, RunRow{RunID: "run_accept_original", TaskID: duty.TaskID, ActionID: "act_accept_original", Generation: 1, Role: protocol.RoleAccept, DesiredState: protocol.DesiredStopped, ObservedState: protocol.RunFinished, CreatedAt: 2}); err != nil {
			return err
		}
		if err := tx.InsertArtifact(ctx, "art_original", duty.TaskID, "run_"+duty.TaskID, "commit", `{ "mounts": [{ "commit_sha": "`+candidate.HeadSHA+`" }] }`, "original-artifact-digest", 1); err != nil {
			return err
		}
		if err := tx.InsertAcceptance(ctx, duty.AcceptanceID, duty.TaskID, "run_accept_original", 2, "art_original", protocol.VerdictPassed, `{ "review": "Original independent acceptance" }`, `[]`, 2); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE tasks SET current_artifact_id='art_original' WHERE task_id=?`, duty.TaskID); err != nil {
			return err
		}
		if _, err := tx.InsertEvent(ctx, EventRow{EventID: duty.EventID, Source: "app", ObjectType: "task", ObjectID: duty.TaskID, Kind: "task.completed", BodyJSON: `{}`, RequestID: "req_original", CreatedAt: 2}); err != nil {
			return err
		}
		if err := tx.InsertDecision(ctx, "dec_original", duty.TaskID, protocol.DeliveryCandidateQuestion(candidate), `["approve","defer","end_delivery"]`, `{}`, nil, 2); err != nil {
			return err
		}
		if _, err := tx.InsertMessage(ctx, MessageRow{MessageID: "msg_approval", ConversationID: "main", Author: "owner", Body: "Approve this exact candidate", RequestID: "req_approval", CreatedAt: 3}); err != nil {
			return err
		}
		if err := tx.InsertRequest(ctx, RequestRecord{Domain: "owner", RequestID: "req_approval", Operation: "attentions.handle", PayloadHash: "original-payload", PrincipalJSON: `{"kind":"owner","subject":"fixture-owner"}`, TraceID: "trace_original", Status: protocol.OpSucceeded, ResultJSON: `{ "decision_id": "dec_original", "option": "approve" }`, CreatedAt: 3}); err != nil {
			return err
		}
		if err := tx.AnswerDecision(ctx, "dec_original", 1, `{ "option": "approve" }`, "msg_approval", 3); err != nil {
			return err
		}
		if err := tx.SaveAttention(ctx, protocol.Attention{AttentionID: duty.AttentionID, IssueID: issue.IssueID, TaskID: duty.TaskID, DecisionID: "dec_original", Kind: "decision", Status: "resolved", Revision: 2, Title: protocol.DeliveryCandidateQuestion(candidate), Resolution: "approve", ResolutionSourceID: "msg_approval", CreatedAt: 2, UpdatedAt: 3}, 0, "req_approval"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO issue_delivery_answers(decision_id,message_id,content_revision) VALUES('dec_original','msg_approval',1)`); err != nil {
			return err
		}
		raw, err := json.MarshalIndent(duty, "", "  ")
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO issue_delivery_duties(issue_id,task_id,event_id,acceptance_id,revision,status,body_json) VALUES(?,?,?,?,?,?,?)`, issue.IssueID, duty.TaskID, duty.EventID, duty.AcceptanceID, duty.Revision, duty.Status, string(raw)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO issue_delivery_executions(task_id,issue_id,source_task_id,duty_revision,candidate_json) VALUES(?,?,?,?,?)`, duty.ExecutionTaskID, issue.IssueID, duty.TaskID, duty.Revision, string(frozen)); err != nil {
			return err
		}
		if err := tx.UpsertHost(ctx, HostRow{HostID: "host_original", OS: "linux", Arch: "amd64", Slots: 1}, 1); err != nil {
			return err
		}
		return tx.PutHostRuntime(ctx, "host_original", HostRuntimeRow{Runtime: protocol.RunnerRuntime{InstanceID: "instance_original", Generation: 4, Sequence: 23, Protocol: 9, ProtocolMin: 8, ProtocolMax: 9, JournalVersion: 12, PendingOperations: 1}, LastHeartbeatAt: 3, Online: true})
	}); err != nil {
		t.Fatal(err)
	}

	queries := []string{
		`SELECT * FROM issue_delivery_duties ORDER BY issue_id,task_id`,
		`SELECT * FROM issue_delivery_executions ORDER BY task_id`,
		`SELECT * FROM issue_delivery_answers ORDER BY decision_id`,
		`SELECT * FROM attentions ORDER BY attention_id`,
		`SELECT * FROM attention_revisions ORDER BY attention_id,revision`,
		`SELECT * FROM decisions ORDER BY decision_id`,
		`SELECT * FROM request_records ORDER BY domain,request_id`,
		`SELECT * FROM conversation_messages ORDER BY seq`,
		`SELECT * FROM task_specs ORDER BY task_id,spec_version`,
		`SELECT * FROM tasks ORDER BY task_id`,
		`SELECT * FROM runs ORDER BY run_id`,
		`SELECT * FROM acceptances ORDER BY acceptance_id`,
		`SELECT * FROM artifacts ORDER BY artifact_id`,
		`SELECT * FROM host_runtime ORDER BY host_id`,
		`SELECT * FROM schema_migrations WHERE version<=97 ORDER BY version`,
	}
	before := sharedDeliveryMigrationSnapshot(t, ctx, db, queries)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(ctx, path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	if after := sharedDeliveryMigrationSnapshot(t, ctx, upgraded.read, queries); !reflect.DeepEqual(before, after) {
		t.Fatal("migration changed an original approval, frozen execution or native runtime", before, after)
	}
	if err := upgraded.Read(ctx, func(tx *ReadTx) error {
		var additions int
		if err := tx.Q().QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE version BETWEEN 98 AND 101`).Scan(&additions); err != nil {
			return err
		}
		if additions != 4 {
			t.Fatalf("schema 97 did not append all four maintenance migrations: %d", additions)
		}
		var foreignKeys int
		if err := tx.Q().QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_check`).Scan(&foreignKeys); err != nil {
			return err
		}
		var integrity string
		if err := tx.Q().QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil {
			return err
		}
		if foreignKeys != 0 || integrity != "ok" {
			t.Fatalf("migrated ledger integrity: foreign keys=%d, integrity=%q", foreignKeys, integrity)
		}
		saved, err := GetIssueDeliveryDuty(ctx, tx.Q(), issue.IssueID, duty.TaskID)
		if err != nil {
			return err
		}
		option, err := DeliveryOwnerOption(ctx, tx.Q(), saved)
		if err != nil {
			return err
		}
		if option != "approve" || saved.Authorization != nil || !reflect.DeepEqual(saved.Candidate, &candidate) {
			t.Fatal("original approval was lost or replaced with synthetic policy authority", option, saved)
		}
		if err := DeliveryGrantValid(ctx, tx.Q(), issue, saved); err != nil {
			return err
		}
		for _, table := range []string{"shared_delivery_reviews", "issue_pr_policy_questions", "task_agreement_assessments"} {
			var count int
			if err := tx.Q().QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				t.Fatalf("migration fabricated a new assessment or authority in %s", table)
			}
		}
		runtime, err := GetHostRuntime(ctx, tx.Q(), "host_original")
		if err != nil {
			return err
		}
		if runtime.Runtime.JournalVersion != 12 || runtime.Runtime.Protocol != 9 || runtime.Runtime.Sequence != 23 || runtime.Runtime.PendingOperations != 1 {
			t.Fatal("server schema migration changed the native journal or communication identity", runtime)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func sharedDeliveryMigrationSnapshot(t *testing.T, ctx context.Context, db *sql.DB, queries []string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, query := range queries {
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(values)
			if err != nil {
				t.Fatal(err)
			}
			out[query] = append(out[query], string(raw))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	return out
}
