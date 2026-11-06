package journal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/GhostFlying/tend/internal/protocol"
)

// BoundedRecoveryQuarantine records only an independently validated replacement
// authority. Original runs, native identities, requests and resources stay in
// their existing tables; the snapshot preserves the exact predecessor run set.
type BoundedRecoveryQuarantine struct {
	Authorization    protocol.BoundedMaintenanceAuthorization `json:"authorization"`
	Snapshot         json.RawMessage                          `json:"snapshot"`
	AdmissionRelease *protocol.BoundedRunnerAdmissionRelease  `json:"admission_release,omitempty"`
}

// QuarantineBoundedRecovery must run before Hello or native recovery. Fencing
// does not claim that any native worker exited, or that any request succeeded.
// Replaying the same authority neither extends its deadline nor changes its set.
func (j *Journal) QuarantineBoundedRecovery(ctx context.Context, authorization protocol.BoundedMaintenanceAuthorization) error {
	if authorization.RequestID == "" || authorization.Role != "runner" {
		return errors.New("journal: invalid bounded recovery authority")
	}
	if err := j.validateBoundedSource(ctx, authorization, true); err != nil {
		return err
	}
	body, err := json.Marshal(authorization)
	if err != nil {
		return err
	}
	return j.tx(ctx, func(tx *sql.Tx) error {
		var prior string
		err := tx.QueryRowContext(ctx, `SELECT authority_json FROM bounded_recovery_quarantines WHERE request_id=?`, authorization.RequestID).Scan(&prior)
		if err == nil {
			if prior != string(body) {
				return errors.New("journal: bounded recovery authority changed")
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		// Read and freeze the inventory in the same transaction that fences it.
		// A retained Held flag and StoppedConfirmed value are never rewritten.
		var snapshot string
		if err := tx.QueryRowContext(ctx, `SELECT json_object(
		 'runs',json((SELECT json_group_array(json_object('assignment',json(assignment_json),'phase',phase,'fenced',fenced,'held',held,'thread_id',thread_id,'attempts',attempts,'stopped_confirmed',stopped_confirmed)) FROM runs WHERE phase<>'done')),
		 'requests',json((SELECT json_group_array(json_object('request_id',request_id,'operation',operation,'run_id',run_id,'target_json',target_json,'payload_json',payload_json,'created_at',created_at,'expected_control_revision',expected_control_revision)) FROM requests)),
		 'workspace_releases',json((SELECT json_group_array(json_object('workspace_id',workspace_id,'task_id',task_id)) FROM workspace_releases)))`).Scan(&snapshot); err != nil {
			return err
		}
		// An independently authorized later replacement can supersede a stuck
		// recovery window without deleting its authority or frozen inventory.
		if _, err := tx.ExecContext(ctx, `UPDATE bounded_recovery_quarantines SET active=0,superseded_by=? WHERE active=1`, authorization.RequestID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO bounded_recovery_quarantines(request_id,authority_json,snapshot_json,active) VALUES(?,?,?,1)`, authorization.RequestID, string(body), snapshot); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE runs SET fenced=1 WHERE phase<>'done'`)
		return err
	})
}

func (j *Journal) validateBoundedSource(ctx context.Context, authority protocol.BoundedMaintenanceAuthorization, migrated bool) error {
	data := authority.DataIdentity
	if authority.Role != "runner" || data == nil || data.Path == "" || data.Version <= 0 || data.Version > CurrentVersion || j.writer == nil {
		return errors.New("journal: complete bounded source identity required")
	}
	device, inode, err := kernelFileIdentity(j.writer)
	if err != nil {
		return err
	}
	if device != data.Device || inode != data.Inode {
		return errors.New("journal: bounded source database inode changed")
	}
	held, err := j.writer.Stat()
	if err != nil {
		return err
	}
	named, err := os.Stat(data.Path)
	if err != nil || !os.SameFile(held, named) {
		return errors.New("journal: bounded source path changed")
	}
	incarnation, err := os.Open(j.path + ".incarnation")
	if err != nil {
		return fmt.Errorf("journal: bounded incarnation: %w", err)
	}
	defer incarnation.Close()
	incDevice, incInode, err := kernelFileIdentity(incarnation)
	if err != nil {
		return err
	}
	if data.Incarnation != fmt.Sprintf("%d:%d", incDevice, incInode) {
		return errors.New("journal: bounded source incarnation changed")
	}
	var version int64
	if err := j.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if !migrated && version == CurrentVersion {
		var saved string
		err := j.db.QueryRowContext(ctx, `SELECT authority_json FROM bounded_recovery_quarantines WHERE request_id=?`, authority.RequestID).Scan(&saved)
		body, marshalErr := json.Marshal(authority)
		if err == nil && marshalErr == nil && saved == string(body) {
			return nil
		}
	}
	if !migrated && version != data.Version || migrated && version != CurrentVersion {
		return errors.New("journal: bounded source schema version changed")
	}
	return nil
}

// BoundedDataIdentity reports metadata from this already owned journal. The
// incarnation denotes the kernel identity of the separate generation file.
func (j *Journal) BoundedDataIdentity(ctx context.Context) (protocol.BoundedDataIdentity, error) {
	device, inode, err := kernelFileIdentity(j.writer)
	if err != nil {
		return protocol.BoundedDataIdentity{}, err
	}
	file, err := os.Open(j.path + ".incarnation")
	if err != nil {
		return protocol.BoundedDataIdentity{}, err
	}
	defer file.Close()
	incDevice, incInode, err := kernelFileIdentity(file)
	if err != nil {
		return protocol.BoundedDataIdentity{}, err
	}
	var version int64
	if err := j.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return protocol.BoundedDataIdentity{}, err
	}
	return protocol.BoundedDataIdentity{Path: j.path, Device: device, Inode: inode, Incarnation: fmt.Sprintf("%d:%d", incDevice, incInode), Version: version}, nil
}

// BoundedRecovery reads the durable admission fence even on an unhinted restart.
// Ordinary lease Continue/Stop controls do not authorize clearing this record.
func (j *Journal) BoundedRecovery(ctx context.Context) (*BoundedRecoveryQuarantine, error) {
	var authority, snapshot string
	var released sql.NullString
	err := j.db.QueryRowContext(ctx, `SELECT authority_json,snapshot_json,admission_release_json FROM bounded_recovery_quarantines WHERE active=1 ORDER BY rowid DESC LIMIT 1`).Scan(&authority, &snapshot, &released)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	value := &BoundedRecoveryQuarantine{Snapshot: json.RawMessage(snapshot)}
	if err := json.Unmarshal([]byte(authority), &value.Authorization); err != nil {
		return nil, err
	}
	if value.Authorization.RequestID == "" || value.Authorization.Role != "runner" {
		return nil, errors.New("journal: invalid saved bounded recovery authority")
	}
	if released.Valid {
		if err := json.Unmarshal([]byte(released.String), &value.AdmissionRelease); err != nil {
			return nil, err
		}
	}
	return value, nil
}

// ReleaseBoundedAdmission records independent owner permission for new work.
// It leaves every original run, resource and uncertain request untouched.
func (j *Journal) ReleaseBoundedAdmission(ctx context.Context, release protocol.BoundedRunnerAdmissionRelease) error {
	if err := j.validateBoundedSource(ctx, protocol.BoundedMaintenanceAuthorization{Role: "runner", DataIdentity: &release.Journal}, true); err != nil {
		return err
	}
	body, err := json.Marshal(release)
	if err != nil {
		return err
	}
	return j.tx(ctx, func(tx *sql.Tx) error {
		var authority string
		var prior sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT authority_json,admission_release_json FROM bounded_recovery_quarantines WHERE request_id=? AND active=1`, release.RecoveryAuthorizationID).Scan(&authority, &prior); err != nil {
			return err
		}
		var original protocol.BoundedMaintenanceAuthorization
		if err := json.Unmarshal([]byte(authority), &original); err != nil {
			return err
		}
		if release.RequestID == "" || release.RequestID == original.RequestID || release.Successor != original.Target || original.DataIdentity == nil ||
			release.Journal.Device != original.DataIdentity.Device || release.Journal.Inode != original.DataIdentity.Inode || release.Journal.Incarnation != original.DataIdentity.Incarnation || release.Journal.Path != original.DataIdentity.Path || release.Journal.Version != CurrentVersion {
			return errors.New("journal: independent admission release binding changed")
		}
		if prior.Valid {
			if prior.String != string(body) {
				return errors.New("journal: admission release already has another authority")
			}
			return nil
		}
		_, err := tx.ExecContext(ctx, `UPDATE bounded_recovery_quarantines SET admission_release_json=? WHERE request_id=? AND active=1`, string(body), original.RequestID)
		return err
	})
}

// BoundedAdmissionReleaseKnown recognizes a previously applied receipt without
// reapplying its one-time process generation or normal-mode admission checks.
func (j *Journal) BoundedAdmissionReleaseKnown(ctx context.Context, release protocol.BoundedRunnerAdmissionRelease) (bool, error) {
	body, err := json.Marshal(release)
	if err != nil {
		return false, err
	}
	var known bool
	err = j.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM bounded_recovery_quarantines WHERE admission_release_json=? AND json_extract(admission_release_json,'$.request_id')=?)`, string(body), release.RequestID).Scan(&known)
	return known, err
}

// BoundedRunQuarantined remains independent from global admission release.
func (j *Journal) BoundedRunQuarantined(ctx context.Context, runID string) (bool, error) {
	var value bool
	err := j.db.QueryRowContext(ctx, `SELECT EXISTS(
	 SELECT 1 FROM bounded_recovery_quarantines q,json_each(q.snapshot_json,'$.runs') original
	 WHERE q.active=1 AND json_extract(original.value,'$.assignment.run_id')=?
	 AND NOT EXISTS(SELECT 1 FROM retained_run_resumes a WHERE a.run_id=? AND json_extract(a.authority_json,'$.recovery_authorization_id')=q.request_id))`, runID, runID).Scan(&value)
	return value, err
}

func (j *Journal) BoundedRequestQuarantined(ctx context.Context, requestID string) (bool, error) {
	var value bool
	err := j.db.QueryRowContext(ctx, `SELECT EXISTS(
	 SELECT 1 FROM bounded_recovery_quarantines q,json_each(q.snapshot_json,'$.requests') original
	 WHERE q.active=1 AND json_extract(original.value,'$.request_id')=?)`, requestID).Scan(&value)
	return value, err
}

func (j *Journal) BoundedWorkspaceQuarantined(ctx context.Context, workspaceID, taskID string) (bool, error) {
	var value bool
	err := j.db.QueryRowContext(ctx, `SELECT EXISTS(
	 SELECT 1 FROM bounded_recovery_quarantines q,json_each(q.snapshot_json,'$.workspace_releases') original
	 WHERE q.active=1 AND json_extract(original.value,'$.workspace_id')=? AND json_extract(original.value,'$.task_id')=?)`, workspaceID, taskID).Scan(&value)
	return value, err
}

// validateBoundedContinuation binds an independent strict attachment to both
// this recovery authority and the original frozen assignment. An ordinary
// server Continue cannot manufacture this new permission or broaden its set.
func validateBoundedContinuation(ctx context.Context, tx *sql.Tx, runID string, resume protocol.RetainedRunResume) error {
	var authority string
	err := tx.QueryRowContext(ctx, `SELECT q.request_id
	 FROM bounded_recovery_quarantines q,json_each(q.snapshot_json,'$.runs') original
	 WHERE q.active=1 AND json_extract(original.value,'$.assignment.run_id')=?
	 ORDER BY q.rowid DESC LIMIT 1`, runID).Scan(&authority)
	if errors.Is(err, sql.ErrNoRows) {
		if resume.RecoveryAuthorizationID != "" {
			return errors.New("journal: bounded recovery continuation is outside its original run set")
		}
		return nil
	}
	if err != nil {
		return err
	}
	if resume.RecoveryAuthorizationID != authority || resume.RequestID == "" || resume.Mode != "attach" {
		return errors.New("journal: independent bounded recovery continuation required")
	}
	var matches bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(
	 SELECT 1 FROM bounded_recovery_quarantines q,json_each(q.snapshot_json,'$.runs') original
	 JOIN runs r ON r.run_id=json_extract(original.value,'$.assignment.run_id')
	 WHERE q.request_id=? AND q.active=1 AND r.run_id=? AND r.phase<>'done'
	 AND json_extract(original.value,'$.assignment')=json(r.assignment_json))`, authority, runID).Scan(&matches)
	if err != nil {
		return err
	}
	if !matches {
		return errors.New("journal: bounded recovery original assignment changed")
	}
	return nil
}
