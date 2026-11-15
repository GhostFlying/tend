package e2e

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/store"
)

const diagnosticRows = 8

type diagnosticTable struct {
	Rows      []map[string]any `json:"rows"`
	Truncated bool             `json:"truncated,omitempty"`
	Error     string           `json:"error,omitempty"`
}

type acceptanceObservation struct {
	TaskID           string                     `json:"task_id"`
	StartedAt        time.Time                  `json:"started_at"`
	ServerFinishedAt time.Time                  `json:"server_finished_at"`
	FinishedAt       time.Time                  `json:"finished_at"`
	Tables           map[string]diagnosticTable `json:"tables"`
	LastObserved     string                     `json:"last_observed"`
	FirstUnobserved  string                     `json:"first_unobserved"`
}

// Each database has its own read snapshot. The common observation window does
// not claim an atomic snapshot across the server and runner, or stop either.
func acceptanceTimeoutSnapshot(s *store.Store, journalPath, taskID string) acceptanceObservation {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return readAcceptanceObservation(ctx, s, journalPath, taskID)
}

func readAcceptanceObservation(ctx context.Context, s *store.Store, journalPath, taskID string) acceptanceObservation {
	o := acceptanceObservation{TaskID: taskID, StartedAt: time.Now().UTC(), Tables: map[string]diagnosticTable{}}
	const runs = `SELECT run_id FROM runs WHERE task_id=?`
	queries := []struct{ name, sql string }{
		{"task", `SELECT task_id,lifecycle,spec_version,current_artifact_id,updated_at FROM tasks WHERE task_id=?`},
		{"actions", `SELECT action_id,kind,status,generation,target_spec_version,target_artifact_id,created_at,updated_at FROM plan_actions WHERE task_id=? ORDER BY created_at,action_id`},
		{"runs", `SELECT run_id,action_id,role,observed_state,desired_state,created_at,updated_at FROM runs WHERE task_id=? ORDER BY created_at,run_id`},
		{"leases", `SELECT run_id,lease_id,status,expires_at,updated_at FROM leases WHERE run_id IN (` + runs + `) ORDER BY run_id`},
		{"artifacts", `SELECT artifact_id,run_id,kind,created_at FROM artifacts WHERE task_id=? ORDER BY created_at,artifact_id`},
		{"acceptances", `SELECT acceptance_id,run_id,artifact_id,verdict,created_at FROM acceptances WHERE task_id=? ORDER BY created_at,acceptance_id`},
		{"native_requests", `SELECT request_id,run_id,method,status,created_at FROM manager_native_requests WHERE run_id IN (` + runs + `) ORDER BY created_at,request_id`},
	}
	err := s.Read(ctx, func(tx *store.ReadTx) error {
		for _, q := range queries {
			o.Tables[q.name] = diagnosticQuery(ctx, tx.Q(), q.sql, taskID)
		}
		return nil
	})
	if err != nil {
		o.Tables["server_read"] = diagnosticTable{Error: err.Error()}
	}
	o.ServerFinishedAt = time.Now().UTC()
	// Open only an existing journal; never migrate, take its writer lock, or
	// change pragmas. Connection and transaction have the same total deadline.
	uri := url.URL{Scheme: "file", Path: journalPath, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", uri.String())
	if err == nil {
		defer db.Close()
		var tx *sql.Tx
		tx, err = db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err == nil {
			defer tx.Rollback()
			const held = `SELECT run_id FROM runs WHERE json_extract(assignment_json,'$.task_id')=?`
			o.Tables["journal_runs"] = diagnosticQuery(ctx, tx, `SELECT run_id,lease_id,phase,fenced,held,attempts,stopped_confirmed,result_acknowledged,worker_done_at,worker_result_json IS NOT NULL AS has_worker_result,thread_id,updated_at FROM runs WHERE json_extract(assignment_json,'$.task_id')=? ORDER BY run_id`, taskID)
			o.Tables["journal_requests"] = diagnosticQuery(ctx, tx, `SELECT request_id,operation,run_id,created_at FROM requests WHERE run_id IN (`+held+`) OR run_id IS NULL ORDER BY created_at,request_id`, taskID)
			o.Tables["journal_events"] = diagnosticQuery(ctx, tx, `SELECT run_id,seq,kind FROM events WHERE run_id IN (`+held+`) ORDER BY run_id,seq`, taskID)
		}
	}
	if err != nil {
		o.Tables["journal_read"] = diagnosticTable{Error: err.Error()}
	}
	o.FinishedAt = time.Now().UTC()
	o.LastObserved, o.FirstUnobserved = acceptanceMilestones(o.Tables)
	return o
}

type diagnosticQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func diagnosticQuery(ctx context.Context, q diagnosticQuerier, query string, args ...any) diagnosticTable {
	out := diagnosticTable{Rows: []map[string]any{}}
	rows, err := q.QueryContext(ctx, query+" LIMIT 9", args...)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		out.Error = err.Error()
		return out
	}
	for rows.Next() {
		if len(out.Rows) == diagnosticRows {
			out.Truncated = true
			break
		}
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			out.Error = err.Error()
			return out
		}
		row := map[string]any{}
		for i, k := range cols {
			v := values[i]
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			if str, ok := v.(string); ok && len(str) > 128 {
				v = str[:128] + "…"
			}
			row[k] = v
		}
		out.Rows = append(out.Rows, row)
	}
	if err := rows.Err(); err != nil {
		out.Error = err.Error()
	}
	return out
}

// These are observed ledger milestones, not a causal diagnosis. Missing or
// bounded-out data never proves a stage has not happened.
func acceptanceMilestones(tables map[string]diagnosticTable) (string, string) {
	last := "original implementation resumed (checked before wait)"
	stages := []struct{ name, table, column, value string }{
		{"implementation artifact committed", "artifacts", "", ""},
		{"acceptance action recorded", "actions", "kind", "accept"},
		{"acceptance run allocated", "runs", "role", "accept"},
		{"passing acceptance committed", "acceptances", "verdict", "passed"},
		{"task completed", "task", "lifecycle", "completed"},
	}
	for _, stage := range stages {
		t, ok := tables[stage.table]
		if !ok || t.Error != "" || t.Truncated {
			return last, fmt.Sprintf("unknown: %s observation incomplete", stage.name)
		}
		found := false
		for _, row := range t.Rows {
			if stage.column == "" || row[stage.column] == stage.value {
				found = true
				break
			}
		}
		if !found {
			return last, stage.name + " not observed"
		}
		last = stage.name
	}
	return last, "all ledger milestones observed after the original timeout"
}

func TestAcceptanceTimeoutObservationIsBoundedAndReadOnly(t *testing.T) {
	s := newServer(t, time.Minute)
	taskID := s.createTask()
	path := filepath.Join(t.TempDir(), "journal.db")
	j, err := journal.Open(t.Context(), path, s.now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	runID := s.ids.New(protocol.PrefixRun)
	as := protocol.Assignment{RunID: runID, TaskID: taskID, LeaseID: s.ids.New(protocol.PrefixLease), RunToken: "SECRET_RUN_CREDENTIAL"}
	if err := j.AcceptAssignment(t.Context(), as, ""); err != nil {
		t.Fatal(err)
	}
	if err := j.WorkerDone(t.Context(), runID, json.RawMessage(`{"private":"SECRET_WORKER_RESULT"}`)); err != nil {
		t.Fatal(err)
	}
	foreign := protocol.Assignment{RunID: s.ids.New(protocol.PrefixRun), TaskID: "other-task", LeaseID: s.ids.New(protocol.PrefixLease)}
	if err := j.AcceptAssignment(t.Context(), foreign, ""); err != nil {
		t.Fatal(err)
	}
	if err := j.PutRequest(t.Context(), journal.Request{RunID: foreign.RunID, Envelope: protocol.Envelope{RequestID: s.ids.New(protocol.PrefixRequest), Operation: "foreign-operation", Payload: json.RawMessage(`{}`)}}); err != nil {
		t.Fatal(err)
	}
	claimID := s.ids.New(protocol.PrefixRequest)
	if err := j.PutRequest(t.Context(), journal.Request{Envelope: protocol.Envelope{RequestID: claimID, Operation: "runner.claim", Payload: json.RawMessage(`{}`)}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < diagnosticRows+2; i++ {
		req := journal.Request{RunID: runID, Envelope: protocol.Envelope{RequestID: s.ids.New(protocol.PrefixRequest), Operation: "worker.result", Payload: json.RawMessage(`{"private":"SECRET_REQUEST"}`)}}
		if err := j.PutRequest(t.Context(), req); err != nil {
			t.Fatal(err)
		}
	}
	before, err := j.Run(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	o := readAcceptanceObservation(ctx, s.store, path, taskID)
	for name, table := range o.Tables {
		if table.Error != "" {
			t.Errorf("%s: %s", name, table.Error)
		}
	}
	if o.TaskID != taskID || o.ServerFinishedAt.Before(o.StartedAt) || o.FinishedAt.Before(o.ServerFinishedAt) {
		t.Fatalf("bad observation windows: %+v", o)
	}
	if len(o.Tables["task"].Rows) != 1 || len(o.Tables["journal_runs"].Rows) != 1 || o.Tables["journal_runs"].Rows[0]["run_id"] != runID {
		t.Fatalf("identity missing: %+v", o)
	}
	if !o.Tables["journal_requests"].Truncated || len(o.Tables["journal_requests"].Rows) != diagnosticRows {
		t.Fatal("unbounded pending requests", o.Tables["journal_requests"])
	}
	raw, _ := json.Marshal(o)
	if strings.Contains(string(raw), "SECRET_") {
		t.Fatal("diagnostics exposed native content or credentials")
	}
	if strings.Contains(string(raw), foreign.RunID) || strings.Contains(string(raw), "foreign-operation") || !strings.Contains(string(raw), claimID) {
		t.Fatal("diagnostic lost pending claim or crossed the task boundary")
	}
	after, err := j.Run(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	reqs, err := j.Requests(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if before.Phase != after.Phase || before.UpdatedAtMillis != after.UpdatedAtMillis || len(reqs) != diagnosticRows+4 {
		t.Fatal("diagnostics changed journal")
	}
	cancelled, stop := context.WithCancel(t.Context())
	stop()
	failed := readAcceptanceObservation(cancelled, s.store, path, taskID)
	if !strings.HasPrefix(failed.FirstUnobserved, "unknown:") {
		t.Fatalf("failed read claimed absence: %+v", failed)
	}
	missing := filepath.Join(t.TempDir(), "missing.db")
	failed = readAcceptanceObservation(ctx, s.store, missing, taskID)
	if failed.Tables["journal_read"].Error == "" {
		t.Fatal("missing journal was not reported")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("read-only diagnostic created a journal", err)
	}
}

func TestEventuallyTimeoutDiagnosticsPrecedeCleanup(t *testing.T) {
	if os.Getenv("TEND_TEST_TIMEOUT_DIAGNOSTIC_CHILD") == "1" {
		t.Cleanup(func() { t.Log("diagnostic child cleanup") })
		eventually(t, "original failure marker", func() bool { return false }, func() { t.Log("diagnostic child snapshot") })
		return
	}
	called := false
	eventually(t, "already complete", func() bool { return true }, func() { called = true })
	if called {
		t.Fatal("success path collected timeout diagnostics")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestEventuallyTimeoutDiagnosticsPrecedeCleanup$", "-test.v")
	cmd.Env = append(os.Environ(), "TEND_TEST_TIMEOUT_DIAGNOSTIC_CHILD=1")
	raw, err := cmd.CombinedOutput()
	text := string(raw)
	snapshot, failure, cleanup := strings.Index(text, "diagnostic child snapshot"), strings.Index(text, "timed out waiting until original failure marker"), strings.Index(text, "diagnostic child cleanup")
	if err == nil || ctx.Err() != nil || snapshot < 0 || failure < snapshot || cleanup < failure || strings.Count(text, "diagnostic child snapshot") != 1 {
		t.Fatalf("lost original timeout/observation order: %v %s", err, text)
	}
}
