package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
)

// HostRuntimeRow is the latest accepted observation of one runner process.
type HostRuntimeRow struct {
	Runtime         protocol.RunnerRuntime
	LastHeartbeatAt int64
	Online          bool
}

func GetHostRuntime(ctx context.Context, q Querier, host string) (HostRuntimeRow, error) {
	var r HostRuntimeRow
	var raw string
	err := q.QueryRowContext(ctx, `SELECT runtime_json,last_heartbeat_at,online FROM host_runtime WHERE host_id=?`, host).Scan(&raw, &r.LastHeartbeatAt, &r.Online)
	if err != nil {
		return r, notFound(err, "host runtime "+host)
	}
	if err := json.Unmarshal([]byte(raw), &r.Runtime); err != nil {
		return r, fmt.Errorf("store: decode host runtime: %w", err)
	}
	return r, nil
}

func (t *Tx) PutHostRuntime(ctx context.Context, host string, r HostRuntimeRow) error {
	raw, err := json.Marshal(r.Runtime)
	if err != nil {
		return fmt.Errorf("store: encode host runtime: %w", err)
	}
	_, err = t.Exec(ctx, `INSERT INTO host_runtime(host_id,instance_id,sequence,runtime_json,last_heartbeat_at,online) VALUES(?,?,?,?,?,?) ON CONFLICT(host_id) DO UPDATE SET instance_id=excluded.instance_id,sequence=excluded.sequence,runtime_json=excluded.runtime_json,last_heartbeat_at=excluded.last_heartbeat_at,online=excluded.online`, host, r.Runtime.InstanceID, r.Runtime.Sequence, string(raw), r.LastHeartbeatAt, r.Online)
	if err != nil {
		return fmt.Errorf("store: put host runtime: %w", err)
	}
	return nil
}

func HostInstanceRetired(ctx context.Context, q Querier, host, instance string) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM host_retired_instances WHERE host_id=? AND instance_id=?`, host, instance).Scan(&n)
	return n > 0, err
}

func (t *Tx) RetireHostInstance(ctx context.Context, host, instance string) error {
	_, err := t.Exec(ctx, `INSERT OR IGNORE INTO host_retired_instances(host_id,instance_id) VALUES(?,?)`, host, instance)
	if err != nil {
		return fmt.Errorf("store: retire host instance: %w", err)
	}
	return nil
}

func GetHostControl(ctx context.Context, q Querier, host string) (protocol.RunnerControl, error) {
	c := protocol.RunnerControl{Mode: protocol.RunnerNormal}
	var raw string
	err := q.QueryRowContext(ctx, `SELECT control_json FROM host_controls WHERE host_id=?`, host).Scan(&raw)
	if err != nil {
		return c, notFound(err, "host control "+host)
	}
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return c, fmt.Errorf("store: decode host control: %w", err)
	}
	return c, nil
}

func (t *Tx) PutHostControl(ctx context.Context, host string, c protocol.RunnerControl, now int64) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("store: encode host control: %w", err)
	}
	_, err = t.Exec(ctx, `INSERT INTO host_controls(host_id,revision,control_json,updated_at) VALUES(?,?,?,?) ON CONFLICT(host_id) DO UPDATE SET revision=excluded.revision,control_json=excluded.control_json,updated_at=excluded.updated_at`, host, c.Revision, string(raw), now)
	if err != nil {
		return fmt.Errorf("store: put host control: %w", err)
	}
	return nil
}
