package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
)

// DrainingRunner owns its journal until the supervisor confirms physical exit.
type DrainingRunner struct {
	HostRuntimeRow
	Control   protocol.RunnerControl
	UpgradeID string
}

func GetDrainingRunner(ctx context.Context, q Querier, host, instance string) (DrainingRunner, error) {
	var r DrainingRunner
	var observation, control string
	err := q.QueryRowContext(ctx, `SELECT upgrade_id,runtime_json,control_json,last_heartbeat_at
		FROM draining_runners WHERE host_id=? AND instance_id=?`, host, instance).Scan(&r.UpgradeID, &observation, &control, &r.LastHeartbeatAt)
	if err != nil {
		return r, notFound(err, "draining runner")
	}
	if err := json.Unmarshal([]byte(observation), &r.Runtime); err != nil {
		return r, err
	}
	err = json.Unmarshal([]byte(control), &r.Control)
	return r, err
}

// PreserveDrainingRunner is used in the transaction that publishes the next
// primary. Its private maintenance revision fences new work on the old journal.
func (t *Tx) PreserveDrainingRunner(ctx context.Context, host, upgrade string, r HostRuntimeRow, c protocol.RunnerControl) error {
	runtime, err := json.Marshal(r.Runtime)
	if err != nil {
		return err
	}
	control, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = t.Exec(ctx, `INSERT INTO draining_runners(host_id,instance_id,journal_id,upgrade_id,runtime_json,control_json,last_heartbeat_at) VALUES(?,?,?,?,?,?,?)`,
		host, r.Runtime.InstanceID, r.Runtime.JournalID, upgrade, string(runtime), string(control), r.LastHeartbeatAt)
	return err
}

func (t *Tx) ObserveDrainingRunner(ctx context.Context, host string, r protocol.RunnerRuntime, now int64) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = t.Exec(ctx, `UPDATE draining_runners SET runtime_json=?,last_heartbeat_at=? WHERE host_id=? AND instance_id=?`, string(raw), now, host, r.InstanceID)
	return err
}

func RunJournal(ctx context.Context, q Querier, run string) (string, error) {
	var journal string
	err := q.QueryRowContext(ctx, `SELECT journal_id FROM runner_journal_runs WHERE run_id=?`, run).Scan(&journal)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return journal, err
}

func CountJournalRuns(ctx context.Context, q Querier, host, journal string) (int64, error) {
	var count int64
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs r WHERE r.host_id=? AND r.observed_state IN `+openRunStates+
		` AND COALESCE((SELECT journal_id FROM runner_journal_runs j WHERE j.run_id=r.run_id),'')=? AND NOT (`+confirmedWindowExit+`)`, host, journal).Scan(&count)
	return count, err
}

func DrainingInstances(ctx context.Context, q Querier, host string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT instance_id FROM draining_runners WHERE host_id=? ORDER BY instance_id`, host)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (t *Tx) PutDrainingControl(ctx context.Context, host, instance string, control protocol.RunnerControl) error {
	raw, err := json.Marshal(control)
	if err != nil {
		return err
	}
	_, err = t.Exec(ctx, `UPDATE draining_runners SET control_json=? WHERE host_id=? AND instance_id=?`, string(raw), host, instance)
	return err
}
