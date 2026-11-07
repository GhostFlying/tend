package views

import (
	"database/sql"
	"testing"

	"github.com/GhostFlying/tend/internal/store"
)

func TestCapabilityProjectionKeepsSuppliedSpecVersion(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.ExecContext(t.Context(), `
		CREATE TABLE tasks(task_id TEXT, spec_version INTEGER);
		CREATE TABLE task_specs(task_id TEXT, spec_version INTEGER, body_json TEXT);
		INSERT INTO tasks VALUES('task', 2);
		INSERT INTO task_specs VALUES('task', 1, '{"agent_selection":{"implementation":{"required_capabilities":[{"capability_id":"image_gen","definition_revision":1}]}}}');
		INSERT INTO task_specs VALUES('task', 2, '{"agent_selection":{"acceptance":{"required_capabilities":[{"capability_id":"image_gen","definition_revision":2}]}}}');
	`)
	if err != nil {
		t.Fatal(err)
	}
	facts := executionFacts{}
	old, err := facts.capabilitySelections(t.Context(), db, []store.TaskRow{{TaskID: "task", SpecVersion: 1}})
	if err != nil {
		t.Fatal(err)
	}
	selection := old["task"]
	if selection == nil || len(selection.Implementation.RequiredCapabilities) != 1 || len(selection.Acceptance.RequiredCapabilities) != 0 {
		t.Fatalf("historical row read newer requirements: %+v", selection)
	}
	current, err := facts.capabilitySelections(t.Context(), db, []store.TaskRow{{TaskID: "task", SpecVersion: 2}})
	if err != nil {
		t.Fatal(err)
	}
	selection = current["task"]
	if selection == nil || len(selection.Acceptance.RequiredCapabilities) != 1 || len(selection.Implementation.RequiredCapabilities) != 0 {
		t.Fatalf("current row lost requirements: %+v", selection)
	}
}
