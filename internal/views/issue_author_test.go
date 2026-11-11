package views

import (
	"database/sql"
	"testing"
)

func TestIssueConversationAuthor(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		`CREATE TABLE hosted_executions(execution_id TEXT,private_snapshot TEXT,created_at INTEGER)`,
		`CREATE TABLE main_turns(request_id TEXT,message_id TEXT)`,
		`CREATE TABLE run_agent_executions(run_id TEXT,pin_json TEXT)`,
		`CREATE TABLE runs(run_id TEXT,task_id TEXT,role TEXT)`,
		`CREATE TABLE issue_manager_tasks(task_id TEXT,pin_json TEXT)`,
		`CREATE TABLE agent_inventory_versions(host_id TEXT,local_revision INTEGER,projection_json TEXT)`,
		`INSERT INTO agent_inventory_versions VALUES('host',7,'{"agents":[{"identity":{"local_id":"implementer"},"display_name":"Cedar"}]}'),('host',8,'{"agents":[{"identity":{"local_id":"implementer"},"display_name":"Renamed Agent"}]}')`,
		`INSERT INTO run_agent_executions VALUES('original-run','{"identity":{"execution_environment_id":"host","local_id":"implementer"},"inventory_revision":7}'),('new-run','{"identity":{"execution_environment_id":"host","local_id":"implementer"},"inventory_revision":8}'),('missing-inventory','{"identity":{"execution_environment_id":"host","local_id":"implementer"},"inventory_revision":6}')`,
		`INSERT INTO runs VALUES('original-run','manager-task','issue_manager'),('legacy-run','legacy-task','issue_manager'),('missing-inventory','manager-task','issue_manager')`,
		`INSERT INTO issue_manager_tasks VALUES('manager-task','{"identity":{"execution_environment_id":"host","local_id":"implementer"},"inventory_revision":8}'),('legacy-task','{"identity":{"execution_environment_id":"host","local_id":"implementer"},"inventory_revision":7}')`,
	} {
		if _, err := db.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ name, snapshot, want string }{
		{"runner frozen name", `{"runner_run_id":"original-run","identity":{"execution_environment_id":"host","local_id":"implementer"},"definition":{"display_name":""}}`, "Cedar"},
		{"new execution renamed", `{"runner_run_id":"new-run","identity":{"execution_environment_id":"host","local_id":"implementer"}}`, "Renamed Agent"},
		{"legacy manager pin", `{"runner_run_id":"legacy-run","identity":{"execution_environment_id":"host","local_id":"implementer"}}`, "Cedar"},
		{"missing original inventory does not use current manager", `{"runner_run_id":"missing-inventory","identity":{"execution_environment_id":"host","local_id":"implementer"}}`, "host/implementer"},
		{"unknown run", `{"runner_run_id":"unknown","identity":{"execution_environment_id":"retired-host","local_id":"retired-agent"}}`, "retired-host/retired-agent"},
		{"hosted frozen name", `{"definition":{"display_name":"Hosted Agent"},"runner_run_id":"new-run"}`, "Hosted Agent"},
		{"legacy hosted reference", `{"identity":{"execution_environment_id":"hosted","local_id":"legacy"}}`, "hosted/legacy"},
		{"empty legacy snapshot", `{}`, "负责人"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := db.ExecContext(t.Context(), `INSERT INTO hosted_executions VALUES(?,?,1)`, tc.name, tc.snapshot); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(t.Context(), `INSERT INTO main_turns VALUES(?,?)`, tc.name, tc.name); err != nil {
				t.Fatal(err)
			}
			// A later handoff on the same trigger must not relabel its original author.
			if _, err := db.ExecContext(t.Context(), `INSERT INTO hosted_executions VALUES(?, '{"definition":{"display_name":"Successor"}}',2)`, tc.name+"-successor"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(t.Context(), `INSERT INTO main_turns VALUES(?,?)`, tc.name+"-successor", tc.name); err != nil {
				t.Fatal(err)
			}
			got, err := issueConversationAuthor(t.Context(), db, tc.name)
			if err != nil || got != tc.want {
				t.Fatalf("author=%q, err=%v, want %q", got, err, tc.want)
			}
		})
	}
	if got, err := issueConversationAuthor(t.Context(), db, "missing"); err != nil || got != "负责人" {
		t.Fatalf("unbound author=%q, err=%v", got, err)
	}
}
