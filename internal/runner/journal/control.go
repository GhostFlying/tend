package journal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
)

// Control reads the last durably applied host maintenance command.
func (j *Journal) Control(ctx context.Context) (protocol.RunnerControl, error) {
	value := protocol.RunnerControl{Mode: protocol.RunnerNormal}
	var body string
	err := j.db.QueryRowContext(ctx, `SELECT control_json FROM runner_control WHERE singleton=1`).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return value, nil
	}
	if err != nil {
		return value, err
	}
	err = json.Unmarshal([]byte(body), &value)
	return value, err
}

// SaveControl is called before acknowledging a control revision to the server.
func (j *Journal) SaveControl(ctx context.Context, value protocol.RunnerControl) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = j.db.ExecContext(ctx, `INSERT INTO runner_control(singleton,control_json) VALUES(1,?) ON CONFLICT(singleton) DO UPDATE SET control_json=excluded.control_json`, string(body))
	return err
}

// UpdaterAuthorization preserves an uncertain request for one immutable binding.
// Only a durable denial followed by a higher control revision starts a new request.
func (j *Journal) UpdaterAuthorization(ctx context.Context, key string, candidate protocol.Envelope) (protocol.Envelope, error) {
	body, err := json.Marshal(candidate)
	if err != nil {
		return protocol.Envelope{}, err
	}
	if _, err := j.db.ExecContext(ctx, `INSERT INTO updater_authorizations(binding_key,envelope_json) VALUES(?,?) ON CONFLICT(binding_key) DO UPDATE SET envelope_json=excluded.envelope_json
 WHERE json_extract(updater_authorizations.envelope_json,'$.authorization_denied')=1
 AND COALESCE(json_extract(excluded.envelope_json,'$.expected_control_revision'),0)>
 COALESCE(json_extract(updater_authorizations.envelope_json,'$.expected_control_revision'),0)`, key, string(body)); err != nil {
		return protocol.Envelope{}, err
	}
	var stored string
	if err := j.db.QueryRowContext(ctx, `SELECT envelope_json FROM updater_authorizations WHERE binding_key=?`, key).Scan(&stored); err != nil {
		return protocol.Envelope{}, err
	}
	var envelope protocol.Envelope
	err = json.Unmarshal([]byte(stored), &envelope)
	return envelope, err
}

// DenyUpdaterAuthorization records only a definite refusal of the saved request.
// Its identity predicate protects a newer attempt from a late old response.
func (j *Journal) DenyUpdaterAuthorization(ctx context.Context, key, requestID string) error {
	_, err := j.db.ExecContext(ctx, `UPDATE updater_authorizations SET envelope_json=json_set(envelope_json,'$.authorization_denied',json('true')) WHERE binding_key=? AND json_extract(envelope_json,'$.request_id')=?`, key, requestID)
	return err
}
