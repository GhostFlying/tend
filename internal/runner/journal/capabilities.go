package journal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
)

func (j *Journal) CapabilityVerification(ctx context.Context, id string) (protocol.LocalCapabilityRequestStatus, error) {
	var status protocol.LocalCapabilityRequestStatus
	var body string
	err := j.db.QueryRowContext(ctx, `SELECT sequence,status_json FROM capability_verifications WHERE request_id=?`, id).Scan(&status.Sequence, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return status, protocol.Errorf(protocol.CodeNotFound, "capability verification request not recorded")
	}
	if err != nil {
		return status, err
	}
	sequence := status.Sequence
	err = json.Unmarshal([]byte(body), &status)
	status.Sequence = sequence
	return status, err
}

// BeginCapabilityVerification atomically deduplicates before any adapter call.
// Only its successful inserter may probe. Retrying an interrupted request reads
// its unknown state, without opening another session or generating another ID.
func (j *Journal) BeginCapabilityVerification(ctx context.Context, p protocol.VerifyLocalCapabilityRequest, instance string, generation int64) (protocol.LocalCapabilityRequestStatus, bool, error) {
	status := protocol.LocalCapabilityRequestStatus{ProbeInstanceID: instance, ProbeGeneration: generation, Request: p, State: "unknown", Recovery: "read or reconcile this original request; do not repeat a probe whose outcome is unknown"}
	body, err := json.Marshal(p)
	if err != nil {
		return status, false, err
	}
	fresh := false
	err = j.tx(ctx, func(tx *sql.Tx) error {
		var old, oldStatus string
		var sequence int64
		err := tx.QueryRowContext(ctx, `SELECT sequence,request_json,status_json FROM capability_verifications WHERE request_id=?`, p.RequestID).Scan(&sequence, &old, &oldStatus)
		if err == nil {
			if old != string(body) {
				return protocol.Errorf(protocol.CodeConflictPayload, "capability verification request payload changed")
			}
			if err := json.Unmarshal([]byte(oldStatus), &status); err != nil {
				return err
			}
			status.Sequence = sequence
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var unresolved int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM capability_verifications WHERE json_extract(request_json,'$.pin.identity.local_id')=? AND json_extract(request_json,'$.capability.capability_id')=? AND json_extract(status_json,'$.state') IN ('unknown','report_pending')`, p.Pin.Identity.LocalID, p.Capability.CapabilityID).Scan(&unresolved); err != nil {
			return err
		}
		if unresolved > 0 {
			return protocol.Errorf(protocol.CodeInvalidTransition, "reconcile the original unresolved capability request before a new verification")
		}
		raw, _ := json.Marshal(status)
		result, err := tx.ExecContext(ctx, `INSERT INTO capability_verifications(request_id,request_json,status_json) VALUES(?,?,?)`, p.RequestID, string(body), string(raw))
		if err != nil {
			return err
		}
		status.Sequence, err = result.LastInsertId()
		fresh = err == nil
		return err
	})
	return status, fresh, err
}

func (j *Journal) SaveCapabilityVerification(ctx context.Context, s protocol.LocalCapabilityRequestStatus) error {
	body, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = j.db.ExecContext(ctx, `UPDATE capability_verifications SET status_json=? WHERE request_id=?`, string(body), s.Request.RequestID)
	return err
}
