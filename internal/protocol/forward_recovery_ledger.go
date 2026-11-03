package protocol

import "encoding/json"

// ForwardRecoveryLedger is an independent appended record. Original upgrade
// status, process/hash, acceptance and unknown history are never rewritten.
type ForwardRecoveryLedger struct {
	OriginalSnapshot       json.RawMessage `json:"original_snapshot"`
	OriginalSnapshotSHA256 string          `json:"original_snapshot_sha256"`

	Plan    ForwardRecoveryPlan `json:"plan"`
	Process RecoveryIdentity    `json:"process"`
	Receipt *RecoveryReceipt    `json:"receipt,omitempty"`
	Linked  bool                `json:"linked"`
}
