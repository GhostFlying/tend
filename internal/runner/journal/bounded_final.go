package journal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
)

// BoundedFinalAcknowledgement preserves the original successful terminal
// envelope after its run credential has ended. It grants no execution authority.
type BoundedFinalAcknowledgement struct {
	RecoveryAuthorizationID string             `json:"recovery_authorization_id"`
	AssignmentSHA256        string             `json:"assignment_sha256"`
	Envelope                protocol.Envelope  `json:"envelope"`
	ManagerCloseEnvelope    *protocol.Envelope `json:"manager_close_envelope,omitempty"`
	WorkerThreadID          string             `json:"worker_thread_id"`
	WorkerAttempts          int                `json:"worker_attempts"`
}

func boundedFinalOperation(role protocol.RunRole, phase, operation string) bool {
	if role == protocol.RoleIssueManager {
		return (phase == PhaseStarted || phase == PhaseReleasing) && (operation == protocol.OpManagerNativeReply || operation == protocol.OpManagerNativeStopped)
	}
	return phase == PhaseSubmitting && (role == protocol.RoleImplement && operation == protocol.OpRunsSubmitArtifact ||
		role == protocol.RoleAccept && operation == protocol.OpAcceptancesSubmit)
}

func validateManagerFinal(ctx context.Context, tx *sql.Tx, runID string, envelope protocol.Envelope) (bool, error) {
	var last int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT next_seq-1 FROM manager_event_sequences WHERE run_id=?),0)`, runID).Scan(&last); err != nil {
		return false, err
	}
	if envelope.Operation == protocol.OpManagerNativeStopped {
		var stopped protocol.ManagerStoppedPayload
		err := json.Unmarshal(envelope.Payload, &stopped)
		return err == nil && stopped.Reason != "" && stopped.LastSequence == last, nil
	}
	var reply protocol.ManagerNativeReply
	if json.Unmarshal(envelope.Payload, &reply) != nil || reply.ID == "" || reply.Error != "" || reply.LastSequence != last {
		return false, nil
	}
	var original string
	err := tx.QueryRowContext(ctx, `SELECT reply_json FROM manager_native_operations WHERE run_id=? AND request_id=?
	 AND status='completed' AND json_extract(request_json,'$.method')='close'
	 AND COALESCE(json_extract(reply_json,'$.error'),'')=''`, runID, reply.ID).Scan(&original)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	saved := envelope
	saved.Payload = json.RawMessage(original)
	a, err := protocol.PayloadHash(&saved)
	if err != nil {
		return false, err
	}
	b, err := protocol.PayloadHash(&envelope)
	return err == nil && a == b, err
}

// AcknowledgeBoundedFinal atomically moves only an original terminal envelope
// from its pending queue to durable fact metadata. Accepted is not settlement.
func (j *Journal) AcknowledgeBoundedFinal(ctx context.Context, request Request, result protocol.Operation) error {
	if request.Envelope.RequestID == "" || result.RequestID != request.Envelope.RequestID || result.Status != protocol.OpSucceeded || result.Error != nil {
		return errors.New("journal: terminal acknowledgement does not match the original successful request")
	}
	return j.tx(ctx, func(tx *sql.Tx) error {
		var assignment, phase, authority, operation, target, payload, thread string
		var stopped bool
		var attempts int
		var revision sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT r.assignment_json,r.phase,r.stopped_confirmed,q.request_id,p.operation,p.target_json,p.payload_json,p.expected_control_revision,COALESCE(r.thread_id,''),r.attempts
		 FROM runs r JOIN requests p ON p.run_id=r.run_id
		 JOIN bounded_recovery_quarantines q ON q.active=1
		 JOIN json_each(q.snapshot_json,'$.runs') original ON json_extract(original.value,'$.assignment.run_id')=r.run_id
		 JOIN json_each(q.snapshot_json,'$.requests') saved ON json_extract(saved.value,'$.request_id')=p.request_id
		 WHERE r.run_id=? AND p.request_id=? AND r.phase<>'done'
		 AND json_extract(original.value,'$.assignment')=r.assignment_json
		 AND json_extract(saved.value,'$.run_id')=p.run_id
		 AND json_extract(saved.value,'$.operation')=p.operation
		 AND json_extract(saved.value,'$.target_json')=p.target_json
		 AND json_extract(saved.value,'$.payload_json')=p.payload_json
		 AND json_extract(saved.value,'$.expected_control_revision') IS p.expected_control_revision`, request.RunID, request.Envelope.RequestID).
			Scan(&assignment, &phase, &stopped, &authority, &operation, &target, &payload, &revision, &thread, &attempts)
		if err != nil {
			return err
		}
		var as protocol.Assignment
		var original protocol.Envelope
		if err := json.Unmarshal([]byte(assignment), &as); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(target), &original.Target); err != nil {
			return err
		}
		original.RequestID, original.Operation, original.Payload = request.Envelope.RequestID, operation, json.RawMessage(payload)
		if revision.Valid {
			original.ExpectedControlRevision = &revision.Int64
		}
		originalHash, err := protocol.PayloadHash(&original)
		if err != nil {
			return err
		}
		actualHash, err := protocol.PayloadHash(&request.Envelope)
		if err != nil {
			return err
		}
		if !stopped || !boundedFinalOperation(as.Role, phase, operation) || originalHash != actualHash ||
			request.Envelope.Target != (protocol.Target{Type: "run", ID: as.RunID}) {
			return errors.New("journal: original terminal identity or physical stop proof changed")
		}
		ack := BoundedFinalAcknowledgement{RecoveryAuthorizationID: authority, AssignmentSHA256: assignmentDigest(assignment), Envelope: request.Envelope, WorkerThreadID: thread, WorkerAttempts: attempts}
		if as.Role == protocol.RoleIssueManager {
			valid, err := validateManagerFinal(ctx, tx, as.RunID, request.Envelope)
			if err != nil {
				return err
			}
			if !valid {
				return errors.New("journal: original manager terminal receipt is not a completed close or stopped fact")
			}
			var raw string
			err = tx.QueryRowContext(ctx, `SELECT authority_json FROM retained_run_resumes WHERE run_id=?`, as.RunID).Scan(&raw)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err == nil {
				var prior retainedResumeRecord
				if err := json.Unmarshal([]byte(raw), &prior); err != nil {
					return err
				}
				if p := prior.BoundedFinal; p != nil && p.AssignmentSHA256 == ack.AssignmentSHA256 && p.WorkerThreadID == thread && p.WorkerAttempts == attempts {
					ack.ManagerCloseEnvelope = p.ManagerCloseEnvelope
				}
			}
			if operation == protocol.OpManagerNativeReply {
				closeEnvelope := request.Envelope
				ack.ManagerCloseEnvelope = &closeEnvelope
			}
		}
		body, err := json.Marshal(ack)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO retained_run_resumes(run_id,authority_json) VALUES(?,json_object('bounded_final_ack',json(?)))
		 ON CONFLICT(run_id) DO UPDATE SET authority_json=json_set(authority_json,'$.bounded_final_ack',json(?))`, as.RunID, string(body), string(body)); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM requests WHERE request_id=? AND run_id=?`, request.Envelope.RequestID, as.RunID)
		return err
	})
}

// AuthorizeBoundedFinalStop joins a fresh host Stop to the saved successful
// terminal fact and original physical exit. Missing facts keep the run isolated.
// It never turns the acknowledgement into an attachment permit.
func (j *Journal) AuthorizeBoundedFinalStop(ctx context.Context, as protocol.Assignment, helloRequestID string) (*RetainedStop, error) {
	if helloRequestID == "" {
		return nil, errors.New("journal: terminal Stop has no Hello identity")
	}
	var stop *RetainedStop
	err := j.tx(ctx, func(tx *sql.Tx) error {
		var raw, assignment, phase, thread string
		var stopped bool
		var attempts int
		err := tx.QueryRowContext(ctx, `SELECT a.authority_json,r.assignment_json,r.phase,r.stopped_confirmed,COALESCE(r.thread_id,''),r.attempts
		 FROM retained_run_resumes a JOIN runs r USING(run_id) WHERE r.run_id=? AND r.phase<>'done'`, as.RunID).
			Scan(&raw, &assignment, &phase, &stopped, &thread, &attempts)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var record retainedResumeRecord
		if err := json.Unmarshal([]byte(raw), &record); err != nil {
			return err
		}
		body, err := json.Marshal(as)
		if err != nil {
			return err
		}
		ack := record.BoundedFinal
		if ack == nil || !stopped || assignmentDigest(string(body)) != assignmentDigest(assignment) ||
			ack.AssignmentSHA256 != assignmentDigest(assignment) || ack.Envelope.RequestID == "" ||
			ack.WorkerAttempts != attempts || ack.WorkerThreadID != thread ||
			ack.Envelope.Target != (protocol.Target{Type: "run", ID: as.RunID}) || !boundedFinalOperation(as.Role, phase, ack.Envelope.Operation) {
			return nil
		}
		if as.Role == protocol.RoleIssueManager {
			valid, err := validateManagerFinal(ctx, tx, as.RunID, ack.Envelope)
			if err != nil || !valid {
				return err
			}
		}
		var original bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM bounded_recovery_quarantines q,
		 json_each(q.snapshot_json,'$.runs') run,json_each(q.snapshot_json,'$.requests') request
		 WHERE q.request_id=? AND json_extract(run.value,'$.assignment')=?
		 AND json_extract(request.value,'$.request_id')=? AND json_extract(request.value,'$.run_id')=?)`,
			ack.RecoveryAuthorizationID, assignment, ack.Envelope.RequestID, as.RunID).Scan(&original); err != nil {
			return err
		}
		if !original {
			return nil
		}
		stop = &RetainedStop{HelloRequestID: helloRequestID, AssignmentSHA256: ack.AssignmentSHA256}
		record.Stop = stop
		body, err = json.Marshal(record)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE retained_run_resumes SET authority_json=? WHERE run_id=?`, string(body), as.RunID)
		return err
	})
	return stop, err
}
