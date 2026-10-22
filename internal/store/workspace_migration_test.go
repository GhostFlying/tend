package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/testkit"
)

func TestWorkspaceResourcesMigrationPreservesPopulatedLedger(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "tend.db")
	clock := testkit.NewClock()
	write, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		t.Fatal(err)
	}
	old := &Store{write: write, read: write, now: clock.Now}
	if err := old.migrate(ctx, 23); err != nil {
		write.Close()
		t.Fatal(err)
	}
	mustExec(t, old, `INSERT INTO hosts(host_id,os,arch,labels_json,repos_json,slots,token_hash,nodes_json,status,created_at,updated_at) VALUES('runner','linux','amd64','[]','["repo"]',3,'hash','[]','active',0,0)`)
	mustExec(t, old, `INSERT INTO repos(repo,remote_url,base_candidates_json,created_at,updated_at) VALUES('repo','https://example.com/repo.git','[]',0,0)`)
	for _, fixture := range []struct{ id, state, runState string }{{"active", "running", "running"}, {"complete", "completed", "finished"}, {"uncertain", "cancelled", "unknown"}} {
		id := "task_" + fixture.id
		insertTask(t, old, id)
		mustExec(t, old, `UPDATE tasks SET lifecycle=?,workspace_name=? WHERE task_id=?`, fixture.state, fixture.id, id)
		mustExec(t, old, `INSERT INTO task_specs(task_id,spec_version,body_json,created_at) VALUES(?,1,'{"delivery":"commit"}',0)`, id)
		if err := insertAction(t, old, "act_"+fixture.id, id, "effect_"+fixture.id); err != nil {
			t.Fatal(err)
		}
		mustExec(t, old, `INSERT INTO runs(run_id,task_id,action_id,generation,role,host_id,desired_state,observed_state,created_at,updated_at) VALUES(?,?,?,1,'implement','runner','run',?,0,0)`, "run_"+fixture.id, id, "act_"+fixture.id, fixture.runState)
		mustExec(t, old, `INSERT INTO workspaces(workspace_id,task_id,name,host_id,status,created_at,updated_at) VALUES(?,?,?,'runner','ready',0,0)`, "wsp_"+fixture.id, id, fixture.id)
		mustExec(t, old, `INSERT INTO workspace_mounts(workspace_id,path,repo,base_json,resolved_base_sha,resolved_from,branch,added_at) VALUES(?,'repo','repo','{}',?,'candidate:main','retained-branch',0)`, "wsp_"+fixture.id, "1111111111111111111111111111111111111111")
	}
	mustExec(t, old, `INSERT INTO artifacts(artifact_id,task_id,run_id,kind,ref_json,digest,created_at) VALUES('art_existing','task_complete','run_complete','git_commits','{"mounts":[]}','original-digest',0)`)
	if err := write.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(context.Background(), path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	err = upgraded.Read(ctx, func(tx *ReadTx) error {
		host, err := GetHost(ctx, tx.Q(), "runner")
		if err != nil {
			return err
		}
		if host.WorkspaceProtocol != 0 {
			t.Fatal("migration invented runner capability")
		}
		for _, id := range []string{"active", "complete", "uncertain"} {
			w, err := GetWorkspaceByTask(ctx, tx.Q(), "task_"+id)
			if err != nil {
				return err
			}
			expectedOwner := "task_" + id
			if w.WorkspaceID != "wsp_"+id || w.CreatorTaskID != "task_"+id || w.Name != id || w.HostID != "runner" || w.Kind != "git" || w.Mode != "new" || w.OccupiedBy != expectedOwner {
				t.Fatalf("migrated resource: %+v", w)
			}
			facts, err := ListMountFacts(ctx, tx.Q(), w.WorkspaceID)
			if err != nil {
				return err
			}
			if facts["repo"].Branch != "retained-branch" {
				t.Fatalf("mount facts lost: %+v", facts)
			}
			var resource string
			if err := tx.Q().QueryRowContext(ctx, `SELECT workspace_id FROM workspace_runs WHERE run_id=?`, "run_"+id).Scan(&resource); err != nil {
				return err
			}
			if resource != w.WorkspaceID {
				t.Fatalf("run resource changed: %s", resource)
			}
		}
		artifact, err := GetArtifact(ctx, tx.Q(), "art_existing")
		if err != nil {
			return err
		}
		if artifact.Kind != "git_commits" || artifact.Digest != "original-digest" || artifact.RefJSON != "{\"mounts\":[]}" {
			t.Fatalf("artifact rewritten: %+v", artifact)
		}
		var version int
		if err := tx.Q().QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
			return err
		}
		if version < 25 {
			t.Fatalf("schema did not reach files migration: %d", version)
		}
		var violations int
		if err := tx.Q().QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil {
			return err
		}
		if violations != 0 {
			t.Fatalf("foreign key violations=%d", violations)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// The creator may acquire a new resource after a start-over; history remains.
	err = upgraded.Write(ctx, func(tx *Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM task_workspace_bindings WHERE task_id='task_active'`); err != nil {
			return err
		}
		return tx.InsertWorkspace(ctx, WorkspaceRow{WorkspaceID: "wsp_replacement", TaskID: "task_active", Name: "active", HostID: "runner", Status: WorkspaceAssigned, Kind: "git", Mode: "new"}, clock.Now().UnixMilli())
	})
	if err != nil {
		t.Fatalf("creator cannot start over while preserving old resource: %v", err)
	}
}
