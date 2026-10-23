package protocol

// CurrentRunnerProtocol is the current server/runner communication generation.
// Version zero denotes runners predating explicit negotiation.
const CurrentRunnerProtocol int64 = 1
const PreviousRunnerProtocol int64 = CurrentRunnerProtocol - 1

const (
	RunnerNormal                = "normal"
	RunnerMaintenance           = "maintenance"
	RunnerReady                 = "ready"
	CapabilityRunnerHeartbeat   = "runner-heartbeat-v1"
	CapabilityRunnerMaintenance = "runner-maintenance-v1"
)

// RunnerBuild identifies the immutable executable and process for supervision.
type RunnerBuild struct {
	Version         string `json:"version,omitempty"`
	Commit          string `json:"commit,omitempty"`
	GOOS            string `json:"goos,omitempty"`
	GOARCH          string `json:"goarch,omitempty"`
	BinarySHA256    string `json:"binary_sha256,omitempty"`
	PID             int64  `json:"pid,omitempty"`
	StartedAtMillis int64  `json:"started_at_ms,omitempty"`
}

// RunnerRuntime is an observation, not authority to renew another incarnation.
// Sequence is monotonic within InstanceID. Replayed claims retain their original
// runtime observation and must still return their original assignment.
type RunnerRuntime struct {
	ConfirmedCancelledUpgradeID string          `json:"confirmed_cancelled_upgrade_id,omitempty"`
	AuthorizationDeadlineMillis int64           `json:"authorization_deadline_ms,omitempty"`
	UpdaterReceipt              *UpdaterReceipt `json:"updater_receipt,omitempty"`
	JournalVersion              int64           `json:"journal_version,omitempty"`
	InstanceID                  string          `json:"instance_id"`
	// Generation orders process starts using non-rollback local durable state.
	Generation             int64       `json:"generation"`
	Sequence               int64       `json:"sequence"`
	Protocol               int64       `json:"protocol"`
	ProtocolMin            int64       `json:"protocol_min"`
	ProtocolMax            int64       `json:"protocol_max"`
	Build                  RunnerBuild `json:"build"`
	Capabilities           []string    `json:"capabilities"`
	UpgradePhase           string      `json:"upgrade_phase,omitempty"`
	UpgradeError           string      `json:"upgrade_error,omitempty"`
	DownloadedBytes        int64       `json:"downloaded_bytes,omitempty"`
	Mode                   string      `json:"mode"`
	UpgradeID              string      `json:"upgrade_id,omitempty"`
	AppliedControlRevision int64       `json:"applied_control_revision"`
	PendingClaims          int64       `json:"pending_claims"`
	PendingOperations      int64       `json:"pending_operations"`
	Blockers               []string    `json:"blockers,omitempty"`
	AuthorizationID        string      `json:"authorization_id,omitempty"`
}

// MaintenanceAuthorization is issued once for a fixed server deadline.
// RemainingMillis is recomputed by the server, never a fresh grant duration.
// Runners derive a conservative local deadline from request send time and must
// confirm an old persisted grant through hello after restarting.
type MaintenanceAuthorization struct {
	ID              string   `json:"id"`
	DeadlineMillis  int64    `json:"deadline_ms"`
	RemainingMillis int64    `json:"remaining_ms"`
	RunIDs          []string `json:"run_ids"`
}

// RunnerControl is the durable, revisioned host control sent by the server.
type RunnerControl struct {
	CancelledUpgradeID string                    `json:"cancelled_upgrade_id,omitempty"`
	Upgrade            *RunnerUpgradeTarget      `json:"upgrade,omitempty"`
	Revision           int64                     `json:"revision"`
	Mode               string                    `json:"mode"`
	UpgradeID          string                    `json:"upgrade_id,omitempty"`
	ReleaseID          string                    `json:"release_id,omitempty"`
	Authorization      *MaintenanceAuthorization `json:"authorization,omitempty"`
}
