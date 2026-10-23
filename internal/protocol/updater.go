// Package protocol defines the durable service and runner wire contracts.
package protocol

import "time"

// Binding is an immutable authorization identity. A grant for another process,
// service, bundle, or upgrade must never authorize this installation.
type UpgradeBinding struct {
	UpgradeID         string `json:"upgrade_id"`
	InstanceID        string `json:"instance_id"`
	ProcessInstanceID string `json:"process_instance_id"`
	ReleaseID         string `json:"release_id"`
	ManifestSHA256    string `json:"manifest_sha256"`
	ArtifactSHA256    string `json:"artifact_sha256"`
}
type UpgradeGrant struct {
	Binding         UpgradeBinding `json:"binding"`
	Allowed         bool           `json:"allowed"`
	ExpiresAt       time.Time      `json:"expires_at"`
	RemainingMillis int64          `json:"remaining_ms"`
}

// Health must come from the configured authority, not the candidate's own
// unauthenticated HTTP server. Sequence identifies distinct accepted heartbeats.
type UpgradeHealth struct {
	Binding           UpgradeBinding `json:"binding"`
	ServerConfirmed   bool           `json:"server_confirmed"`
	ServerRevision    int64          `json:"server_revision"`
	Healthy           bool           `json:"healthy"`
	HeartbeatSequence uint64         `json:"heartbeat_sequence"`
	NewWorkAccepted   bool           `json:"new_work_accepted"`
	RollbackSafe      bool           `json:"rollback_safe"`
}

// UpgradeStartRequest is accepted only over the service user's local control socket.
type UpgradeStartRequest struct {
	RequestID string `json:"request_id"`
	UpgradeID string `json:"upgrade_id"`
	Release   string `json:"release"`
}
type UpgradeChangeRequest struct {
	RequestID               string `json:"request_id"`
	UpgradeID               string `json:"upgrade_id"`
	ExpectedControlRevision int64  `json:"expected_control_revision"`
}

// UpdaterReceipt is local supervisor proof of a completed reversible rollback.
// The authenticated reporting runtime must match CurrentProcessInstanceID and
// CurrentSHA256; Binding still identifies the original failed candidate.
type UpdaterReceipt struct {
	Binding                  UpgradeBinding `json:"binding"`
	Phase                    string         `json:"phase"`
	CurrentProcessInstanceID string         `json:"current_process_instance_id"`
	CurrentSHA256            string         `json:"current_sha256"`
	RollbackConfirmed        bool           `json:"rollback_confirmed"`
}
