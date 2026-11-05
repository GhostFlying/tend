package store

import (
	"context"
	"fmt"
)

type RetainedRunContinuation struct {
	RequestDomain, RequestID, RunID, TaskID, LeaseID, ProtectionSHA256, PayloadJSON string
	TaskRevision, ActivatedAt, CreatedAt                                            int64
}

func LatestRetainedRunContinuation(ctx context.Context, q Querier, run string) (RetainedRunContinuation, error) {
	var r RetainedRunContinuation
	err := q.QueryRowContext(ctx, `SELECT request_domain,request_id,run_id,task_id,lease_id,task_revision,protection_sha256,payload_json,COALESCE(activated_at,0),created_at FROM retained_run_continuations WHERE run_id=? ORDER BY created_at DESC,rowid DESC LIMIT 1`, run).Scan(&r.RequestDomain, &r.RequestID, &r.RunID, &r.TaskID, &r.LeaseID, &r.TaskRevision, &r.ProtectionSHA256, &r.PayloadJSON, &r.ActivatedAt, &r.CreatedAt)
	if err != nil {
		return r, notFound(err, "retained run continuation")
	}
	return r, nil
}
func (t *Tx) InsertRetainedRunContinuation(ctx context.Context, r RetainedRunContinuation) error {
	_, err := t.Exec(ctx, `INSERT INTO retained_run_continuations(request_domain,request_id,run_id,task_id,lease_id,task_revision,protection_sha256,payload_json,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, r.RequestDomain, r.RequestID, r.RunID, r.TaskID, r.LeaseID, r.TaskRevision, r.ProtectionSHA256, r.PayloadJSON, r.CreatedAt)
	if err != nil {
		return fmt.Errorf("store: record retained continuation: %w", err)
	}
	return nil
}
func (t *Tx) ActivateRetainedRunContinuation(ctx context.Context, r RetainedRunContinuation, now int64) error {
	_, err := t.Exec(ctx, `UPDATE retained_run_continuations SET activated_at=COALESCE(activated_at,?) WHERE request_domain=? AND request_id=?`, now, r.RequestDomain, r.RequestID)
	return err
}
