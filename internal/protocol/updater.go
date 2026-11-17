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
	Diagnostics                    *UpgradeHealthDiagnostics `json:"diagnostics,omitempty"`
	BoundedRecoveryAuthorizationID string                    `json:"bounded_recovery_authorization_id,omitempty"`
	JournalVersion                 int64                     `json:"journal_version,omitempty"`
	Binding                        UpgradeBinding            `json:"binding"`
	ServerConfirmed                bool                      `json:"server_confirmed"`
	ServerRevision                 int64                     `json:"server_revision"`
	Healthy                        bool                      `json:"healthy"`
	HeartbeatSequence              uint64                    `json:"heartbeat_sequence"`
	NewWorkAccepted                bool                      `json:"new_work_accepted"`
	RollbackSafe                   bool                      `json:"rollback_safe"`
	// AdmissionFenced promises that this candidate cannot accept new work until
	// its supervisor publishes an irreversible committed receipt. ControlRevision
	// binds that promise to the host control observed in the same transaction.
	AdmissionFenced bool  `json:"admission_fenced"`
	ControlRevision int64 `json:"control_revision"`
}

// UpgradeHealthDiagnostics is observational, never installation authority.
// Nil facts mean that phase was not observed, not that the condition failed.
// Authorities return it only when the caller opts in with diagnostics=1, so
// older strict clients keep their existing response contract.
type UpgradeHealthDiagnostics struct {
	ProcessAlive             *bool                    `json:"process_alive,omitempty"`
	ProcessIdentityMatched   *bool                    `json:"process_identity_matched,omitempty"`
	AuthorityIdentityMatched *bool                    `json:"authority_identity_matched,omitempty"`
	ServerConfirmed          *bool                    `json:"server_confirmed,omitempty"`
	ServiceReady             *bool                    `json:"service_ready,omitempty"`
	MainDrained              *bool                    `json:"main_drained,omitempty"`
	InstallationHealthy      *bool                    `json:"installation_healthy,omitempty"`
	AuthorityHealthy         *bool                    `json:"authority_healthy,omitempty"`
	Components               []UpgradeHealthComponent `json:"components,omitempty"`
	Failures                 []string                 `json:"failures,omitempty"`
	PhaseMillis              map[string]int64         `json:"phase_ms,omitempty"`
	FailureCause             string                   `json:"failure_cause,omitempty"`
}
type UpgradeHealthComponent struct {
	Name    string `json:"name"`
	Ready   *bool  `json:"ready,omitempty"`
	Drained *bool  `json:"drained,omitempty"`
	Failure string `json:"failure,omitempty"`
}

// UpgradeStartRequest is accepted only over the service user's local control socket.
type UpgradeStartRequest struct {
	// ExecutionRequestID is supplied only by the admitted in-process main tool.
	ExecutionRequestID   string                         `json:"-"`
	ParkIdleManagers     bool                           `json:"park_idle_managers,omitempty"`
	DeliveryTaskID       string                         `json:"delivery_task_id,omitempty"`
	RunnerConfigurations map[string]RunnerConfiguration `json:"runner_configurations,omitempty"`
	RequestID            string                         `json:"request_id"`
	UpgradeID            string                         `json:"upgrade_id"`
	Release              string                         `json:"release"`
}
type UpgradeChangeRequest struct {
	RequestID               string `json:"request_id"`
	UpgradeID               string `json:"upgrade_id"`
	ExpectedControlRevision int64  `json:"expected_control_revision"`
}

// UpdaterReceipt is local supervisor proof of prelaunch cancellation, an
// irreversible commit or a completed reversible rollback, including a proven
// same-version successor.
// The authenticated reporting runtime must match CurrentProcessInstanceID and
// CurrentSHA256; Binding still identifies the original installation candidate.
type UpdaterReceipt struct {
	SupersededUpgradeID      string         `json:"superseded_upgrade_id,omitempty"`
	ExternalRecovery         bool           `json:"external_recovery,omitempty"`
	Binding                  UpgradeBinding `json:"binding"`
	Phase                    string         `json:"phase"`
	CurrentProcessInstanceID string         `json:"current_process_instance_id"`
	CurrentSHA256            string         `json:"current_sha256"`
	RollbackConfirmed        bool           `json:"rollback_confirmed"`
	CommittedConfirmed       bool           `json:"committed_confirmed"`
}
