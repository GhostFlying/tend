package views

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestAgentTaskActivityFrozenIdentityDistinctTasksAndUncertainty(t *testing.T) {
	ctx := t.Context()
	clock := testkit.NewClock()
	db, err := viewFixture.Open(ctx, filepath.Join(t.TempDir(), "ledger.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// This fixture isolates projection semantics; App tests drive real claim and
	// started/released transitions rather than claiming these rows launched work.
	if err := db.Write(ctx, func(tx *store.Tx) error {
		for _, task := range []string{"one", "two", "unknown", "interrupted", "history", "failed", "other-host", "other-agent", "legacy"} {
			if _, err := tx.Exec(ctx, `INSERT INTO tasks(task_id,title,lifecycle,control_mode,control_revision,spec_version,implement_generation,priority,block_reason,created_at,updated_at) VALUES(?,'fixture','running','managed',1,1,1,0,'none',1,1)`, task); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO plan_actions(action_id,task_id,kind,effect_key,trigger_json,target_spec_version,authorization_json,generation,status,created_request_id,created_at,updated_at) VALUES(?,?,'implement',?,'{}',1,'{}',1,'done','req_fixture',1,1)`, task, task, task); err != nil {
				return err
			}
		}
		for generation, v := range []struct {
			id, task, env, agent, state string
			revision                    uint64
		}{
			{"start", "one", "host", "cedar", "starting", 1},
			{"duplicate", "one", "host", "cedar", "running", 2},
			{"old-unknown", "one", "host", "cedar", "unknown", 1},
			{"wait", "two", "host", "cedar", "waiting", 2},
			{"unknown", "unknown", "host", "cedar", "unknown", 1},
			{"interrupted", "interrupted", "host", "cedar", "interrupted", 1},
			{"history", "history", "host", "cedar", "finished", 1},
			{"failed", "failed", "host", "cedar", "failed", 1},
			{"other-host", "other-host", "elsewhere", "cedar", "running", 1},
			{"other-agent", "other-agent", "host", "maple", "running", 1},
			{"legacy", "legacy", "host", "", "running", 1},
		} {
			role := protocol.RoleImplement
			if v.id == "wait" {
				role = protocol.RoleIssueManager
			}
			if v.id == "other-agent" {
				role = protocol.RoleAccept
			}
			if _, err := tx.Exec(ctx, `INSERT INTO runs(run_id,task_id,action_id,generation,role,host_id,desired_state,observed_state,created_at,updated_at) VALUES(?,?,?,?,?,?,'run',?,1,1)`, v.id, v.task, v.task, generation+1, role, v.env, v.state); err != nil {
				return err
			}
			if v.agent == "" {
				continue
			}
			pin, _ := json.Marshal(protocol.RunExecutionPin{Identity: protocol.AgentIdentity{ExecutionEnvironmentID: v.env, LocalID: v.agent}, AgentRevision: v.revision})
			if _, err := tx.Exec(ctx, `INSERT INTO run_agent_executions(run_id,pin_json) VALUES(?,?)`, v.id, string(pin)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	read := func(env string) *protocol.AgentTaskActivity {
		t.Helper()
		var out *protocol.AgentTaskActivity
		if err := db.Read(ctx, func(tx *store.ReadTx) error {
			var err error
			out, err = AgentTaskActivity(ctx, tx.Q(), env)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return out
	}
	got := read("host")
	if got.RunningTasks["cedar"] != 2 || got.UncertainTasks["cedar"] != 2 || got.RunningTasks["maple"] != 1 || len(got.RunningTasks) != 2 {
		t.Fatalf("activity=%+v", got)
	}
	if got := read("elsewhere"); got.RunningTasks["cedar"] != 1 || got.UncertainTasks["cedar"] != 0 {
		t.Fatalf("other environment=%+v", got)
	}
	if got := read("empty"); got.RunningTasks == nil || got.UncertainTasks == nil || len(got.RunningTasks) != 0 {
		t.Fatalf("zero observation=%+v", got)
	}
	if err := db.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE runs SET observed_state='finished' WHERE observed_state IN ('starting','running','waiting')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	got = read("host")
	if got.RunningTasks["cedar"] != 0 || got.UncertainTasks["cedar"] != 3 {
		t.Fatalf("ended activity=%+v", got)
	}
}

func TestAgentTaskActivityIndexesUnsettledHostRunsBeforeHistoricalPins(t *testing.T) {
	ctx := t.Context()
	// The minimal schema isolates this read/query-plan contract, not migrations,
	// scheduling or provider execution. Keep the production index definitions.
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		`CREATE TABLE runs(run_id TEXT PRIMARY KEY,task_id TEXT,host_id TEXT,observed_state TEXT)`,
		`CREATE INDEX runs_host ON runs(host_id,observed_state)`,
		`CREATE INDEX runs_task ON runs(task_id,observed_state)`,
		`CREATE TABLE run_agent_executions(run_id TEXT PRIMARY KEY,pin_json TEXT NOT NULL)`,
		`WITH RECURSIVE history(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM history WHERE n<10000)
   INSERT INTO runs SELECT 'history-'||n,'task-'||n,'host-'||(n%20),'finished' FROM history`,
		`INSERT INTO run_agent_executions SELECT run_id,json_object('identity',json_object('execution_environment_id',host_id,'local_id','cedar')) FROM runs`,
		`INSERT INTO runs VALUES('active','current','host-0','running'),('unknown','uncertain','host-0','unknown'),('mismatch','mismatch','host-0','running'),('other-host','other-host','host-1','running')`,
		`INSERT INTO run_agent_executions VALUES
   ('active','{"identity":{"execution_environment_id":"host-0","local_id":"cedar"}}'),
   ('unknown','{"identity":{"execution_environment_id":"host-0","local_id":"maple"}}'),
   ('mismatch','{"identity":{"execution_environment_id":"host-1","local_id":"cedar"}}'),
   ('other-host','{"identity":{"execution_environment_id":"host-1","local_id":"cedar"}}')`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+agentTaskActivitySQL, "host-0", "host-0")
	if err != nil {
		t.Fatal(err)
	}
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(plan, "\n")
	// This is a performance boundary: JSON must not be decoded for all history
	// whenever a live event refreshes a directory. Do not assert unrelated ordering.
	if !strings.Contains(joined, "SEARCH r USING INDEX runs_host (host_id=? AND observed_state=?)") || strings.Contains(joined, "SCAN e") {
		t.Fatalf("unbounded activity plan:\n%s", joined)
	}
	t.Logf("activity plan:\n%s", joined)
	activity, err := AgentTaskActivity(ctx, db, "host-0")
	if err != nil {
		t.Fatal(err)
	}
	if activity.RunningTasks["cedar"] != 1 || activity.UncertainTasks["maple"] != 1 || activity.RunningTasks["maple"] != 0 {
		t.Fatalf("activity=%+v", activity)
	}
}
