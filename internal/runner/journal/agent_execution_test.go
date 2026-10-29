package journal

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestNamedExecutionPinSurvivesRestartAndCannotBeRelabeled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	j := open(t, path)
	pin := protocol.RunExecutionPin{Identity: protocol.AgentIdentity{ExecutionEnvironmentID: "runner-a", LocalID: "review"}, AgentRevision: 3, Runtime: protocol.AgentRuntimeReference{RuntimeID: "orca", Revision: 2}, InventoryRevision: 5, SnapshotID: "snapshot-original", PublicSHA256: "sha256:" + strings.Repeat("a", 64), Profile: protocol.ExecutionProfile{Model: "review-model", Effort: "xhigh"}, Worker: protocol.WorkerOrca, BackendID: "orca"}
	as := protocol.Assignment{RunID: "run_named", Execution: &pin, Workspace: protocol.AssignmentWorkspace{ID: "wsp_original", BackendID: "orca"}}
	if err := j.AcceptAssignmentFromWorker(t.Context(), as, "", protocol.WorkerAppServer); err == nil {
		t.Fatal("wrong recorded worker accepted")
	}
	if err := j.AcceptAssignmentFromWorker(t.Context(), as, "", protocol.WorkerOrca); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j = open(t, path)
	defer j.Close()
	changed := pin
	changed.SnapshotID = "snapshot-new"
	changed.Profile.Model = "new-model"
	as.Execution = &changed
	if err := j.AcceptAssignmentFromWorker(t.Context(), as, "", protocol.WorkerOrca); err != nil {
		t.Fatal(err)
	}
	run, err := j.Run(t.Context(), as.RunID)
	if err != nil || run.Assignment.Execution == nil || *run.Assignment.Execution != pin || run.WorkerKind != protocol.WorkerOrca {
		t.Fatal("replayed run changed its pin", run, err)
	}
	if err := j.Done(t.Context(), as.RunID); err != nil {
		t.Fatal(err)
	}
	history, err := j.HistoricalRun(t.Context(), as.RunID)
	if err != nil || *history.Assignment.Execution != pin {
		t.Fatal(history, err)
	}
}
func TestJournalElevenUpgradePreservesLegacyAssignmentWithoutPin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries[:11] {
		body, err := migrationFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.ExecContext(t.Context(), string(body)); err != nil {
			t.Fatal(err)
		}
	}
	const original = `{"run_id":"run_legacy","task_id":"task_legacy","workspace":{"name":"original"}}`
	if _, err = db.ExecContext(t.Context(), `PRAGMA user_version=11; INSERT INTO runs(run_id,lease_id,run_token,assignment_json,phase,updated_at,worker_kind) VALUES('run_legacy','','',?,'claimed',123,'appserver')`, original); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	j := open(t, path)
	defer j.Close()
	run, err := j.Run(t.Context(), "run_legacy")
	if err != nil || run.Assignment.Execution != nil || run.Assignment.Workspace.BackendID != "" || run.WorkerKind != protocol.WorkerAppServer {
		t.Fatal("legacy default changed", run, err)
	}
	var raw string
	var version int64
	if err = j.db.QueryRowContext(t.Context(), `SELECT assignment_json FROM runs WHERE run_id='run_legacy'`).Scan(&raw); err != nil || raw != original {
		t.Fatal("legacy payload rewritten", raw, err)
	}
	if err = j.db.QueryRowContext(t.Context(), `PRAGMA user_version`).Scan(&version); err != nil || version != 12 {
		t.Fatal(version, err)
	}
	// The old shape remains ordinary JSON; storage version, not an invented
	// required wire field, prevents an old binary from recovering named state.
	var decoded map[string]any
	if err = json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatal(err)
	}
}
