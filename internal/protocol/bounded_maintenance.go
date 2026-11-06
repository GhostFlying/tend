package protocol

// MaintenanceTimeoutPolicy bounds one component without renewing on polling.
type MaintenanceTimeoutPolicy struct {
	OverallMillis     int64 `json:"overall_ms"`
	HandoffMillis     int64 `json:"handoff_ms"`
	FenceMillis       int64 `json:"fence_ms"`
	DrainMillis       int64 `json:"drain_ms"`
	StopMillis        int64 `json:"stop_ms"`
	KillMillis        int64 `json:"kill_ms"`
	BackupMillis      int64 `json:"backup_ms"`
	StartHealthMillis int64 `json:"start_health_ms"`
	CompleteMillis    int64 `json:"complete_ms"`
}

// DefaultMaintenanceTimeoutPolicy reserves twenty seconds for transitions.
func DefaultMaintenanceTimeoutPolicy() MaintenanceTimeoutPolicy {
	return MaintenanceTimeoutPolicy{OverallMillis: 360000, HandoffMillis: 60000,
		FenceMillis: 30000, DrainMillis: 60000, StopMillis: 30000, KillMillis: 10000,
		BackupMillis: 30000, StartHealthMillis: 90000, CompleteMillis: 30000}
}

// BoundedMaintenanceAuthorization is an explicit operator decision, not a grant
// inferred from a heartbeat, an empty slot, or an ordinary upgrade permission.
type BoundedMaintenanceAuthorization struct {
	RequestID           string                   `json:"request_id"`
	ExpectedGeneration  uint64                   `json:"expected_generation"`
	ExpectedPhase       string                   `json:"expected_phase"`
	PlanSHA256          string                   `json:"plan_sha256"`
	Role                string                   `json:"role"`
	Source              RecoveryIdentity         `json:"source"`
	Target              UpgradeBinding           `json:"target"`
	Reason              string                   `json:"reason"`
	ExpiresAtMillis     int64                    `json:"expires_at_ms"`
	Timeouts            MaintenanceTimeoutPolicy `json:"timeouts"`
	ForceOnDrainTimeout bool                     `json:"force_on_drain_timeout"`
	UpdaterTarget       *BoundedUpdaterTarget    `json:"updater_target,omitempty"`
	UpdaterSource       *RecoveryIdentity        `json:"updater_source,omitempty"`
	UpdaterLock         *BoundedDataIdentity     `json:"updater_lock,omitempty"`
	DataIdentity        *BoundedDataIdentity     `json:"data_identity,omitempty"`
}

// BoundedDataIdentity binds a local ledger or journal; only the local installer
// verifies its kernel identity. A server observation is not such a proof.
type BoundedDataIdentity struct {
	Path        string `json:"path,omitempty"`
	Device      uint64 `json:"device"`
	Inode       uint64 `json:"inode"`
	Incarnation string `json:"incarnation,omitempty"`
	Version     int64  `json:"version,omitempty"`
}

// BoundedProcessExit records only the exact process whose exit was confirmed.
// It says nothing about that process's native children or business outcomes.
type BoundedProcessExit struct {
	Process           RecoveryIdentity `json:"process"`
	ConfirmedAtMillis int64            `json:"confirmed_at_ms"`
	Method            string           `json:"method"`
}

// BoundedMaintenanceAudit preserves the original observation and every explicit
// force decision separately from ordinary drain and health proofs.
type BoundedMaintenanceAudit struct {
	PreviousWindow   *BoundedMaintenanceWindowSnapshot `json:"previous_window,omitempty"`
	RequestID        string                            `json:"request_id"`
	Action           string                            `json:"action"`
	Phase            string                            `json:"phase"`
	Reason           string                            `json:"reason"`
	RecordedAtMillis int64                             `json:"recorded_at_ms"`
	Archive          *ArchiveOnlineStatus              `json:"archive,omitempty"`
	ObservationError string                            `json:"observation_error,omitempty"`
	Outcome          string                            `json:"outcome"`
}

// BoundedMaintenanceState requires updater format 4. Old readers must reject
// that format before writing; ignoring these fields would erase authority.
type BoundedMaintenanceState struct {
	RecoveryAuthorizationID string                                     `json:"recovery_authorization_id,omitempty"`
	Authorization           BoundedMaintenanceAuthorization            `json:"authorization"`
	Authorizations          map[string]BoundedMaintenanceAuthorization `json:"authorizations,omitempty"`
	Phase                   string                                     `json:"phase"`
	PhaseEnteredAtMillis    int64                                      `json:"phase_entered_at_ms"`
	PhaseDeadlineMillis     int64                                      `json:"phase_deadline_ms"`
	OverallStartedAtMillis  int64                                      `json:"overall_started_at_ms"`
	OverallDeadlineMillis   int64                                      `json:"overall_deadline_ms"`
	BootID                  string                                     `json:"boot_id"`
	PhaseDeadlineMono       int64                                      `json:"phase_deadline_mono_ms"`
	OverallDeadlineMono     int64                                      `json:"overall_deadline_mono_ms"`
	SourceExit              *BoundedProcessExit                        `json:"source_exit,omitempty"`
	Audit                   []BoundedMaintenanceAudit                  `json:"audit,omitempty"`
	Outcome                 string                                     `json:"outcome"`
}

// BoundedMaintenanceActions names the complete independent installer surface.
func BoundedMaintenanceActions() []string {
	return []string{"plan", "apply", "force", "extend", "cancel", "status", "reconcile", "takeover"}
}

// BoundedUpdaterTarget pins the local supervisor replacement. Its configuration
// and manager facts are independently checked by the local installer.
type BoundedUpdaterTarget struct {
	Unit             string `json:"unit"`
	UnitSHA256       string `json:"unit_sha256"`
	ManagerSHA256    string `json:"manager_sha256"`
	UpdaterBinary    string `json:"updater_binary"`
	Candidate        string `json:"candidate"`
	CandidateSHA256  string `json:"candidate_sha256"`
	ConfigFile       string `json:"config_file"`
	ConfigFileSHA256 string `json:"config_file_sha256"`
}

// BoundedMaintenanceWindowSnapshot preserves the expired window when a new
// independent authorization explicitly opens a recovery window.
type BoundedMaintenanceWindowSnapshot struct {
	Phase                  string `json:"phase"`
	PhaseEnteredAtMillis   int64  `json:"phase_entered_at_ms"`
	PhaseDeadlineMillis    int64  `json:"phase_deadline_ms"`
	OverallStartedAtMillis int64  `json:"overall_started_at_ms"`
	OverallDeadlineMillis  int64  `json:"overall_deadline_ms"`
	BootID                 string `json:"boot_id"`
	PhaseDeadlineMono      int64  `json:"phase_deadline_mono_ms"`
	OverallDeadlineMono    int64  `json:"overall_deadline_mono_ms"`
	Outcome                string `json:"outcome"`
}
