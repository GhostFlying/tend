package journal

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/GhostFlying/tend/internal/protocol"
)

func immutableCapabilityBinding(b protocol.RunCapabilityBinding) string {
	b.CurrentMatch = nil
	body, _ := json.Marshal(b)
	return string(body)
}

func recordCapabilityReceipt(ctx context.Context, tx *sql.Tx, as protocol.Assignment) error {
	if as.CapabilityBinding == nil {
		return nil
	}
	var original string
	if err := tx.QueryRowContext(ctx, `SELECT assignment_json FROM runs WHERE run_id=?`, as.RunID).Scan(&original); err != nil {
		return err
	}
	var old protocol.Assignment
	if err := json.Unmarshal([]byte(original), &old); err != nil {
		return err
	}
	// Existing assignment identity remains authoritative. A reconnect cannot
	// add constraints, replace selection evidence or relabel an old pin.
	if old.CapabilityBinding == nil || old.Execution == nil || as.Execution == nil || *old.Execution != *as.Execution || immutableCapabilityBinding(*old.CapabilityBinding) != immutableCapabilityBinding(*as.CapabilityBinding) {
		return nil
	}
	body, err := json.Marshal(as.CapabilityBinding)
	if err != nil {
		return err
	}
	var prior string
	err = tx.QueryRowContext(ctx, `SELECT binding_json FROM run_capability_receipts WHERE run_id=? ORDER BY sequence DESC LIMIT 1`, as.RunID).Scan(&prior)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if string(body) == prior {
		return nil
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO run_capability_receipts(run_id,binding_json) VALUES(?,?)`, as.RunID, string(body))
	return err
}
