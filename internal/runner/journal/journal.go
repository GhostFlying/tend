// Package journal is the runner's local record of the runs it holds and the
// requests it must resend. The runner writes to it before acting, so after
// a restart it can reconcile with tend and resend requests under their
// original IDs.
package journal

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"sort"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"

	_ "modernc.org/sqlite" // registers the sqlite driver
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Run phases.
const (
	PhaseClaimed    = "claimed"
	PhaseStarted    = "started"
	PhaseWorkerDone = "worker_done"
	PhaseSubmitting = "submitting"
	PhaseReleasing  = "releasing"
	PhaseDone       = "done"
)

// Run is a run the runner holds.
type Run struct {
	WorkerDoneAtMillis int64
	WorkerKind         protocol.WorkerKind
	WorkerResult       json.RawMessage
	UpdatedAtMillis    int64
	StoppedConfirmed   bool
	Assignment         protocol.Assignment
	Phase              string
	Fenced             bool
	Attempts           int
	// Thread is the harness thread of the run's last worker, if any.
	Thread string
	// Held says the owner took the run's agent over and has not handed
	// it back to a worker yet (D67).
	Held bool
}

// Request is a request whose ID must survive restarts.
type Request struct {
	CreatedAtMillis int64
	Envelope        protocol.Envelope
	// RunID is set when the request is sent with the run's credential.
	RunID string
}

// Journal is the runner's journal file.
type Journal struct {
	path string
	db   *sql.DB
	now  func() time.Time
}

// Open opens or creates the journal at path.
func Open(ctx context.Context, path string, now func() time.Time) (*Journal, error) {
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "synchronous(FULL)")
	q.Set("_txlock", "immediate")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("journal: open: %w", err)
	}
	db.SetMaxOpenConns(1)
	j := &Journal{db: db, now: now, path: path}
	if err := j.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return j, nil
}

// Close closes the journal.
func (j *Journal) Close() error { return j.db.Close() }

func (j *Journal) migrate(ctx context.Context) error {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return fmt.Errorf("journal: list migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return j.tx(ctx, func(tx *sql.Tx) error {
		var version int
		if err := tx.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
			return fmt.Errorf("journal: read version: %w", err)
		}
		if version > len(names) {
			return fmt.Errorf("journal: version %d is newer than this runner (%d)", version, len(names))
		}
		for i, name := range names[version:] {
			body, err := migrationFS.ReadFile("migrations/" + name)
			if err != nil {
				return fmt.Errorf("journal: read %s: %w", name, err)
			}
			if _, err := tx.ExecContext(ctx, string(body)); err != nil {
				return fmt.Errorf("journal: apply %s: %w", name, err)
			}
			if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, version+i+1)); err != nil {
				return fmt.Errorf("journal: set version: %w", err)
			}
		}
		return nil
	})
}

func (j *Journal) tx(ctx context.Context, fn func(*sql.Tx) error) (err error) {
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("journal: begin: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// PutRequest records a request before it is first sent.
func (j *Journal) PutRequest(ctx context.Context, r Request) error {
	return j.tx(ctx, func(tx *sql.Tx) error { return putRequest(ctx, tx, r, j.now()) })
}

func putRequest(ctx context.Context, tx *sql.Tx, r Request, now time.Time) error {
	target, err := json.Marshal(r.Envelope.Target)
	if err != nil {
		return fmt.Errorf("journal: encode target: %w", err)
	}
	payload := string(r.Envelope.Payload)
	if payload == "" {
		payload = "null"
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO requests (request_id, operation, run_id, target_json, payload_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, r.Envelope.RequestID, r.Envelope.Operation, nullString(r.RunID), string(target), payload, now.UnixMilli())
	if err != nil {
		return fmt.Errorf("journal: put request: %w", err)
	}
	return nil
}

// DeleteRequest forgets a request once it is acknowledged.
func (j *Journal) DeleteRequest(ctx context.Context, requestID string) error {
	_, err := j.db.ExecContext(ctx, `DELETE FROM requests WHERE request_id = ?`, requestID)
	if err != nil {
		return fmt.Errorf("journal: delete request: %w", err)
	}
	return nil
}

// Requests returns the journaled requests, oldest first.
func (j *Journal) Requests(ctx context.Context) ([]Request, error) {
	rows, err := j.db.QueryContext(ctx, `SELECT request_id, operation, run_id, target_json, payload_json, created_at
		FROM requests ORDER BY created_at, request_id`)
	if err != nil {
		return nil, fmt.Errorf("journal: list requests: %w", err)
	}
	defer rows.Close()
	var out []Request
	for rows.Next() {
		var (
			r               Request
			runID           sql.NullString
			target, payload string
		)
		if err := rows.Scan(&r.Envelope.RequestID, &r.Envelope.Operation, &runID, &target, &payload, &r.CreatedAtMillis); err != nil {
			return nil, fmt.Errorf("journal: scan request: %w", err)
		}
		if err := json.Unmarshal([]byte(target), &r.Envelope.Target); err != nil {
			return nil, fmt.Errorf("journal: decode target: %w", err)
		}
		if payload != "null" {
			r.Envelope.Payload = json.RawMessage(payload)
		}
		r.RunID = runID.String
		out = append(out, r)
	}
	return out, rows.Err()
}

// RequestForRun returns the journaled request of a run, if any.
func (j *Journal) RequestForRun(ctx context.Context, runID string) (Request, bool, error) {
	all, err := j.Requests(ctx)
	if err != nil {
		return Request{}, false, err
	}
	for _, r := range all {
		// A takeover report is not the run's final report.
		if r.RunID == runID && r.Envelope.Operation != protocol.OpRunsReportTakeover {
			return r, true, nil
		}
	}
	return Request{}, false, nil
}

// AcceptAssignment records a run handed to the runner and forgets the claim
// request that produced it, in one transaction. claimID may be empty for
// runs handed over by hello.
func (j *Journal) AcceptAssignment(ctx context.Context, as protocol.Assignment, claimID string) error {
	return j.AcceptAssignmentFromWorker(ctx, as, claimID, "")
}

// AcceptAssignmentFromWorker records origin on first receipt only. Reconnects
// must not relabel existing runs with the runner's current harness.
func (j *Journal) AcceptAssignmentFromWorker(ctx context.Context, as protocol.Assignment, claimID string, worker protocol.WorkerKind) error {
	if worker != "" && worker != protocol.WorkerAppServer && worker != protocol.WorkerOrca {
		return errors.New("journal: invalid worker origin")
	}
	body, err := json.Marshal(as)
	if err != nil {
		return fmt.Errorf("journal: encode assignment: %w", err)
	}
	return j.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO runs (run_id, lease_id, run_token, assignment_json, phase, updated_at, worker_kind)
			VALUES (?, ?, ?, ?, 'claimed', ?, ?) ON CONFLICT (run_id) DO NOTHING`,
			as.RunID, as.LeaseID, as.RunToken, string(body), j.now().UnixMilli(), worker); err != nil {
			return fmt.Errorf("journal: put run: %w", err)
		}
		if claimID != "" {
			if _, err := tx.ExecContext(ctx, `DELETE FROM requests WHERE request_id = ?`, claimID); err != nil {
				return fmt.Errorf("journal: forget claim: %w", err)
			}
		}
		return nil
	})
}

// BindUnstartedWorkerOrigin labels an old receipt only before any possible worker
// launch. Attempts and thread identity make all started or ambiguous runs immutable.
// Callers must reload the run and verify its resulting origin before acting.
func (j *Journal) BindUnstartedWorkerOrigin(ctx context.Context, runID string, worker protocol.WorkerKind) error {
	if worker != protocol.WorkerAppServer && worker != protocol.WorkerOrca {
		return errors.New("journal: invalid worker origin")
	}
	return j.exec(ctx, `UPDATE runs SET worker_kind=? WHERE run_id=? AND worker_kind='' AND attempts=0 AND COALESCE(thread_id,'')=''`, worker, runID)
}

// Runs returns the runs that are not done.
func (j *Journal) Runs(ctx context.Context) ([]Run, error) {
	rows, err := j.db.QueryContext(ctx, `SELECT assignment_json, phase, fenced, attempts, COALESCE(thread_id, ''), held, COALESCE(worker_result_json,''), COALESCE(worker_done_at,0), stopped_confirmed, worker_kind FROM runs
		WHERE phase <> 'done' ORDER BY run_id`)
	if err != nil {
		return nil, fmt.Errorf("journal: list runs: %w", err)
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var (
			r      Run
			body   string
			result string
		)
		if err := rows.Scan(&body, &r.Phase, &r.Fenced, &r.Attempts, &r.Thread, &r.Held, &result, &r.WorkerDoneAtMillis, &r.StoppedConfirmed, &r.WorkerKind); err != nil {
			return nil, fmt.Errorf("journal: scan run: %w", err)
		}
		r.WorkerResult = json.RawMessage(result)
		if err := json.Unmarshal([]byte(body), &r.Assignment); err != nil {
			return nil, fmt.Errorf("journal: decode assignment: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Run returns one run, or ErrNotFound.
func (j *Journal) Run(ctx context.Context, runID string) (Run, error) {
	all, err := j.Runs(ctx)
	if err != nil {
		return Run{}, err
	}
	for _, r := range all {
		if r.Assignment.RunID == runID {
			return r, nil
		}
	}
	return Run{}, ErrNotFound
}

// ErrNotFound is returned for a run the journal does not hold.
var ErrNotFound = errors.New("journal: not found")

// SetPhase moves a run to a phase.
func (j *Journal) SetPhase(ctx context.Context, runID, phase string) error {
	return j.exec(ctx, `UPDATE runs SET phase = ?, updated_at = ? WHERE run_id = ?`, phase, j.now().UnixMilli(), runID)
}

// WorkerDone persists the observed final result before any external agent is stopped.
func (j *Journal) WorkerDone(ctx context.Context, runID string, result json.RawMessage) error {
	if !json.Valid(result) {
		return errors.New("journal: invalid completed worker result")
	}
	now := j.now().UnixMilli()
	return j.exec(ctx, `UPDATE runs SET phase='worker_done',worker_result_json=?,worker_done_at=COALESCE(worker_done_at,?),updated_at=? WHERE run_id=?`, string(result), now, now, runID)
}

// SetFenced marks or clears a run's fence.
func (j *Journal) SetFenced(ctx context.Context, runID string, fenced bool) error {
	return j.exec(ctx, `UPDATE runs SET fenced = ?, updated_at = ? WHERE run_id = ?`, fenced, j.now().UnixMilli(), runID)
}

// SetThread records the harness thread the run's worker works in.
func (j *Journal) SetThread(ctx context.Context, runID, threadID string) error {
	return j.exec(ctx, `UPDATE runs SET thread_id = ?, updated_at = ? WHERE run_id = ?`, nullString(threadID), j.now().UnixMilli(), runID)
}

// SetHeld records whether the owner holds the run's agent.
func (j *Journal) SetHeld(ctx context.Context, runID string, held bool) error {
	return j.exec(ctx, `UPDATE runs SET held = ?, updated_at = ? WHERE run_id = ?`, held, j.now().UnixMilli(), runID)
}

// NextAttempt counts another start of the run's worker and returns it.
func (j *Journal) NextAttempt(ctx context.Context, runID string) (int, error) {
	var n int
	err := j.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET attempts = attempts + 1, stopped_confirmed = 0, updated_at = ? WHERE run_id = ?`, j.now().UnixMilli(), runID); err != nil {
			return fmt.Errorf("journal: count attempt: %w", err)
		}
		return tx.QueryRowContext(ctx, `SELECT attempts FROM runs WHERE run_id = ?`, runID).Scan(&n)
	})
	return n, err
}

// Finish records the run's final report and moves it to phase, in one
// transaction, so a restart resends exactly that report.
func (j *Journal) Finish(ctx context.Context, runID, phase string, r Request) error {
	return j.tx(ctx, func(tx *sql.Tx) error {
		if err := putRequest(ctx, tx, r, j.now()); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE runs SET phase = ?, updated_at = ? WHERE run_id = ?`, phase, j.now().UnixMilli(), runID)
		return err
	})
}

// Replace swaps a run's final report for another one and moves the run to
// phase, in one transaction.
func (j *Journal) Replace(ctx context.Context, runID, phase, oldRequestID string, r Request) error {
	return j.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM requests WHERE request_id = ?`, oldRequestID); err != nil {
			return fmt.Errorf("journal: drop request: %w", err)
		}
		if err := putRequest(ctx, tx, r, j.now()); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE runs SET phase = ?, updated_at = ? WHERE run_id = ?`, phase, j.now().UnixMilli(), runID)
		return err
	})
}

// Done marks a run done and forgets its request and events.
func (j *Journal) Done(ctx context.Context, runID string) error {
	return j.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET phase = 'done', updated_at = ? WHERE run_id = ?`, j.now().UnixMilli(), runID); err != nil {
			return fmt.Errorf("journal: finish run: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM requests WHERE run_id = ?`, runID); err != nil {
			return fmt.Errorf("journal: forget requests: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM events WHERE run_id = ?`, runID); err != nil {
			return fmt.Errorf("journal: forget events: %w", err)
		}
		return nil
	})
}

// AppendEvent assigns the run's next sequence number to a worker event and
// keeps it until tend acknowledges it.
func (j *Journal) AppendEvent(ctx context.Context, runID string, e protocol.RunEvent) error {
	return j.tx(ctx, func(tx *sql.Tx) error {
		var seq int64
		if err := tx.QueryRowContext(ctx, `SELECT next_seq FROM runs WHERE run_id = ?`, runID).Scan(&seq); err != nil {
			return fmt.Errorf("journal: next seq: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET next_seq = ? WHERE run_id = ?`, seq+1, runID); err != nil {
			return fmt.Errorf("journal: advance seq: %w", err)
		}
		body := string(e.Body)
		_, err := tx.ExecContext(ctx, `INSERT INTO events (run_id, seq, native_id, kind, body_json) VALUES (?, ?, ?, ?, ?)`,
			runID, seq, nullString(e.NativeID), e.Kind, nullString(body))
		if err != nil {
			return fmt.Errorf("journal: append event: %w", err)
		}
		return nil
	})
}

// PendingEvents returns up to limit unacknowledged events of a run in order.
func (j *Journal) PendingEvents(ctx context.Context, runID string, limit int) ([]protocol.RunEvent, error) {
	rows, err := j.db.QueryContext(ctx, `SELECT seq, native_id, kind, body_json FROM events WHERE run_id = ? ORDER BY seq LIMIT ?`, runID, limit)
	if err != nil {
		return nil, fmt.Errorf("journal: pending events: %w", err)
	}
	defer rows.Close()
	var out []protocol.RunEvent
	for rows.Next() {
		var (
			e            protocol.RunEvent
			native, body sql.NullString
		)
		if err := rows.Scan(&e.Seq, &native, &e.Kind, &body); err != nil {
			return nil, fmt.Errorf("journal: scan event: %w", err)
		}
		e.NativeID = native.String
		if body.Valid {
			e.Body = json.RawMessage(body.String)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AckEvents forgets a run's events up to and including seq.
func (j *Journal) AckEvents(ctx context.Context, runID string, seq int64) error {
	return j.exec(ctx, `DELETE FROM events WHERE run_id = ? AND seq <= ?`, runID, seq)
}

func (j *Journal) exec(ctx context.Context, q string, args ...any) error {
	if _, err := j.db.ExecContext(ctx, q, args...); err != nil {
		return fmt.Errorf("journal: %w", err)
	}
	return nil
}

func nullString(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }
