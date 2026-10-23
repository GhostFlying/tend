package journal

import (
	"database/sql"
	"github.com/GhostFlying/tend/internal/protocol"
	"path/filepath"
	"testing"
	"time"
)

func TestAssignmentOriginPersistsAcrossReconfigurationAndCompletion(t *testing.T) {
	j, err := Open(t.Context(), filepath.Join(t.TempDir(), "journal.db"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	as := protocol.Assignment{RunID: "run_origin", TaskID: "task"}
	if err := j.AcceptAssignmentFromWorker(t.Context(), as, "", protocol.WorkerOrca); err != nil {
		t.Fatal(err)
	}
	if err := j.AcceptAssignmentFromWorker(t.Context(), as, "", protocol.WorkerAppServer); err != nil {
		t.Fatal(err)
	}
	run, err := j.Run(t.Context(), as.RunID)
	if err != nil || run.WorkerKind != protocol.WorkerOrca {
		t.Fatalf("reconnect relabeled origin: %+v %v", run, err)
	}
	if err := j.Done(t.Context(), as.RunID); err != nil {
		t.Fatal(err)
	}
	run, err = j.HistoricalRun(t.Context(), as.RunID)
	if err != nil || run.WorkerKind != protocol.WorkerOrca {
		t.Fatalf("completion lost origin: %+v %v", run, err)
	}
}

func TestJournalSevenMigrationPreservesUnknownLegacyOrigin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries[:7] {
		body, err := migrationFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(t.Context(), string(body)); err != nil {
			t.Fatal(err)
		}
	}
	_, err = db.ExecContext(t.Context(), `PRAGMA user_version=7;INSERT INTO runs(run_id,lease_id,run_token,assignment_json,phase,updated_at,thread_id) VALUES('legacy','','','{"run_id":"legacy","task_id":"task"}','done',123,'00000000-0000-7000-8000-000000000001')`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	j, err := Open(t.Context(), path, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	var version int
	if err := j.db.QueryRowContext(t.Context(), `PRAGMA user_version`).Scan(&version); err != nil || version != 9 {
		t.Fatalf("version %d %v", version, err)
	}
	if err := j.AcceptAssignmentFromWorker(t.Context(), protocol.Assignment{RunID: "legacy"}, "", protocol.WorkerOrca); err != nil {
		t.Fatal(err)
	}
	run, err := j.HistoricalRun(t.Context(), "legacy")
	if err != nil || run.WorkerKind != "" || run.Phase != PhaseDone || run.Thread != "00000000-0000-7000-8000-000000000001" {
		t.Fatalf("legacy origin incorrectly backfilled or history changed: %+v %v", run, err)
	}
}

func TestOnlyNeverStartedLegacyRunCanBindWorkerOrigin(t *testing.T) {
	j, err := Open(t.Context(), filepath.Join(t.TempDir(), "journal.db"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	for _, id := range []string{"unstarted", "attempted", "threaded", "explicit"} {
		worker := protocol.WorkerKind("")
		if id == "explicit" {
			worker = protocol.WorkerOrca
		}
		if err := j.AcceptAssignmentFromWorker(t.Context(), protocol.Assignment{RunID: id}, "", worker); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := j.NextAttempt(t.Context(), "attempted"); err != nil {
		t.Fatal(err)
	}
	if err := j.SetThread(t.Context(), "threaded", "native"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"unstarted", "attempted", "threaded", "explicit"} {
		if err := j.BindUnstartedWorkerOrigin(t.Context(), id, protocol.WorkerAppServer); err != nil {
			t.Fatal(err)
		}
		run, err := j.Run(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		want := protocol.WorkerKind("")
		if id == "unstarted" {
			want = protocol.WorkerAppServer
		}
		if id == "explicit" {
			want = protocol.WorkerOrca
		}
		if run.WorkerKind != want {
			t.Fatalf("unsafe origin binding for %s: %q want %q", id, run.WorkerKind, want)
		}
	}
}
