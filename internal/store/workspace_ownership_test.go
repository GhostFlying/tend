package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/testkit"
)

func TestWorkspaceOwnershipMigrationUsesOnlyInitialPrimaryContext(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "ledger.db")
	clock := testkit.NewClock()
	write, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		t.Fatal(err)
	}
	old := &Store{write: write, read: write, now: clock.Now}
	if err := old.migrate(ctx, 35); err != nil {
		t.Fatal(err)
	}
	mustExec(t, old, `INSERT INTO hosts(host_id,os,arch,labels_json,repos_json,slots,token_hash,nodes_json,status,created_at,updated_at) VALUES('runner','linux','amd64','[]','[]',2,'hash','[]','active',1,1)`)
	for _, id := range []string{"wi_primary", "wi_secondary"} {
		mustExec(t, old, `INSERT INTO work_items(work_item_id,project,revision,status,body_json,created_at,updated_at) VALUES(?,'default',1,'open','{}',1,1)`, id)
	}
	for _, fixture := range []struct {
		id, original string
	}{
		{"task_attributed", `{"shared_context":[{"work_item_id":"wi_primary"},{"work_item_id":"wi_secondary"}]}`},
		{"task_unknown", `{}`},
		{"task_missing_item", `{"shared_context":[{"work_item_id":"wi_missing"}]}`},
	} {
		insertTask(t, old, fixture.id)
		mustExec(t, old, `INSERT INTO task_specs(task_id,spec_version,body_json,created_at) VALUES(?,1,?,1)`, fixture.id, fixture.original)
		mustExec(t, old, `INSERT INTO task_specs(task_id,spec_version,body_json,created_at) VALUES(?,2,'{"shared_context":[{"work_item_id":"wi_secondary"},{"work_item_id":"wi_primary"}]}',2)`, fixture.id)
		mustExec(t, old, `UPDATE tasks SET spec_version=2,lifecycle='running' WHERE task_id=?`, fixture.id)
		mustExec(t, old, `INSERT INTO work_item_task_links(work_item_id,stage_id,task_id) VALUES('wi_secondary','linked',?)`, fixture.id)
		if err := insertAction(t, old, "act_"+fixture.id, fixture.id, fixture.id); err != nil {
			t.Fatal(err)
		}
		mustExec(t, old, `INSERT INTO runs(run_id,task_id,action_id,generation,role,host_id,desired_state,observed_state,created_at,updated_at) VALUES(?,?,?,1,'implement','runner','run','running',1,1)`, "run_"+fixture.id, fixture.id, "act_"+fixture.id)
		mustExec(t, old, `INSERT INTO workspaces(workspace_id,task_id,name,host_id,status,occupied_by,provider_id,provider_instance_id,path,created_at,updated_at) VALUES(?,?,?,'runner','ready',?,'provider','instance','/existing/checkout',1,1)`, "wsp_"+fixture.id, fixture.id, fixture.id, fixture.id)
		mustExec(t, old, `INSERT INTO task_workspace_bindings(task_id,workspace_id,mode,created_at) VALUES(?,?,'new',1)`, fixture.id, "wsp_"+fixture.id)
		mustExec(t, old, `INSERT INTO workspace_runs(run_id,workspace_id) VALUES(?,?)`, "run_"+fixture.id, "wsp_"+fixture.id)
	}
	if err := write.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(ctx, path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	if err := upgraded.Read(ctx, func(tx *ReadTx) error {
		for taskID, want := range map[string]string{"task_attributed": "wi_primary", "task_unknown": "", "task_missing_item": ""} {
			owner, err := GetTaskWorkItemOwner(ctx, tx.Q(), taskID)
			if err != nil || owner != want {
				t.Fatalf("owner %s = %q, want %q: %v", taskID, owner, want, err)
			}
			w, err := GetWorkspaceByTask(ctx, tx.Q(), taskID)
			if err != nil || w.OwnerWorkItemID != want || w.OccupiedBy != taskID || w.Path != "/existing/checkout" || w.ProviderInstanceID != "instance" || w.Status != WorkspaceReady {
				t.Fatalf("existing workspace facts changed: %+v %v", w, err)
			}
			run, err := GetRun(ctx, tx.Q(), "run_"+taskID)
			if err != nil || run.ObservedState != "running" {
				t.Fatalf("existing run changed: %+v %v", run, err)
			}
		}
		var violations int
		if err := tx.Q().QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil {
			return err
		}
		if violations != 0 {
			t.Fatalf("migration left %d foreign-key violations", violations)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Reopening cannot change immutable provenance after newer links/specs exist.
	if err := upgraded.Write(context.Background(), func(tx *Tx) error {
		return tx.InsertTaskWorkItemOwner(ctx, "task_attributed", "wi_secondary")
	}); err == nil {
		t.Fatal("existing primary owner was overwritten")
	}
}
