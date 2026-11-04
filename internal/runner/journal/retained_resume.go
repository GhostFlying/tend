package journal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
)

func (j *Journal) SetRetainedResume(ctx context.Context, id string, p protocol.RetainedRunResume) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return j.exec(ctx, `INSERT INTO retained_run_resumes(run_id,authority_json) VALUES(?,?) ON CONFLICT(run_id) DO UPDATE SET authority_json=excluded.authority_json`, id, string(b))
}
func (j *Journal) RetainedResume(ctx context.Context, id string) (*protocol.RetainedRunResume, error) {
	var raw string
	err := j.db.QueryRowContext(ctx, `SELECT authority_json FROM retained_run_resumes WHERE run_id=?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p protocol.RetainedRunResume
	err = json.Unmarshal([]byte(raw), &p)
	return &p, err
}
