package views

import (
	"database/sql"
	"testing"
)

func TestIssueActivityAuthor(t *testing.T) {
	// These read-projection fixtures isolate frozen identity lookup from writes.
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		`CREATE TABLE hosted_executions(execution_id TEXT,conversation_id TEXT,private_snapshot TEXT,created_at INTEGER)`,
		`CREATE TABLE main_turns(request_id TEXT,thread_id TEXT)`,
		`CREATE TABLE agent_conversations(conversation_id TEXT,work_item_id TEXT)`,
		`CREATE TABLE run_agent_executions(run_id TEXT,pin_json TEXT)`,
		`CREATE TABLE agent_inventory_versions(host_id TEXT,server_revision INTEGER,projection_json TEXT)`,
		`INSERT INTO agent_conversations VALUES('other-issue','other')`,
		`INSERT INTO hosted_executions VALUES('old','other-issue','{"definition":{"display_name":"体验负责人"}}',10),('new','other-issue','{"definition":{"display_name":"新负责人"}}',30)`,
		`INSERT INTO main_turns VALUES('old','original-thread'),('new','new-thread')`,
		`INSERT INTO run_agent_executions VALUES('run','{"identity":{"execution_environment_id":"host","local_id":"review"},"inventory_revision":1}')`,
		`INSERT INTO agent_inventory_versions VALUES('host',1,'{"agents":[{"identity":{"local_id":"review"},"display_name":"独立验收员"}]}'),('host',2,'{"agents":[{"identity":{"local_id":"review"},"display_name":"后续改名"}]}')`,
	} {
		if _, err := db.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ name, actor, want string }{
		{"missing", "", "tend"},
		{"system", `{"kind":"system"}`, "tend"},
		{"owner", `{"kind":"owner"}`, "你"},
		{"main", `{"kind":"main_agent"}`, "主 Agent"},
		{"other Issue manager", `{"kind":"background_agent","conversation_id":"other-issue","thread":"original-thread"}`, "体验负责人"},
		{"unknown manager", `{"kind":"background_agent","conversation_id":"missing"}`, "负责人 Agent"},
		{"named run", `{"kind":"run","subject":"run"}`, "独立验收员"},
		{"legacy run", `{"kind":"run","subject":"missing"}`, "执行 Agent"},
		{"runner", `{"kind":"runner","subject":"host"}`, "执行主机"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := issueActivityAuthor(t.Context(), db, tc.actor, 20)
			if err != nil || got != tc.want {
				t.Fatalf("author=%q, err=%v, want %q", got, err, tc.want)
			}
		})
	}
}
