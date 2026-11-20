package protocol

// MaintenanceBackend pins an external native executor, never a quota policy.
type MaintenanceBackend struct {
	NetworkAccess  bool     `json:"network_access,omitempty"`
	WritableRoots  []string `json:"writable_roots,omitempty"`
	TUIURL         string   `json:"tui_url,omitempty"`
	Agent          string   `json:"agent"`
	Socket         string   `json:"socket"`
	Model          string   `json:"model,omitempty"`
	Effort         string   `json:"effort,omitempty"`
	ApprovalPolicy string   `json:"approval_policy"`
	Sandbox        string   `json:"sandbox"`
}

// MaintenanceSubject pins the original business responsibility independent of its diagnostic case.
type MaintenanceSubject struct {
	IssueID  string `json:"issue_id"`
	TaskID   string `json:"task_id"`
	RunID    string `json:"run_id"`
	WaitID   string `json:"wait_id,omitempty"`
	Evidence string `json:"evidence"`
	Expected string `json:"expected"`
}

type MaintenanceChange struct {
	OwnerRunID      string `json:"owner_run_id,omitempty"`
	ContextRevision int64  `json:"context_revision,omitempty"`
	OwnerState      string `json:"owner_state,omitempty"`
	WorkStatus      string `json:"work_status,omitempty"`
	TaskCount       int    `json:"task_count,omitempty"`
	Sequence        int64  `json:"sequence"`
	IssueID         string `json:"issue_id"`
	TaskID          string `json:"task_id,omitempty"`
	State           string `json:"state"`
	Revision        int64  `json:"revision"`
	Fingerprint     string `json:"fingerprint"`
}

type MaintenanceAudit struct {
	Since            int64               `json:"since"`
	SnapshotAt       int64               `json:"snapshot_at,omitempty"`
	ObservationError string              `json:"observation_error,omitempty"`
	CIError          string              `json:"ci_error,omitempty"`
	Through          int64               `json:"through"`
	Changes          []MaintenanceChange `json:"changes,omitempty"`
	Recheck          []string            `json:"recheck,omitempty"`
	More             bool                `json:"more,omitempty"`
}

type MaintenanceCase struct {
	Subject      *MaintenanceSubject `json:"subject,omitempty"`
	Intervention string              `json:"intervention,omitempty"`
	Verification string              `json:"verification,omitempty"`

	Observed    bool   `json:"observed"`
	ID          string `json:"id"`
	Source      string `json:"source"`
	Code        string `json:"code"`
	Evidence    string `json:"evidence"`
	Fingerprint string `json:"fingerprint"`
	Status      string `json:"status"`
	Reason      string `json:"reason,omitempty"`
	FirstSeen   int64  `json:"first_seen"`
	LastSeen    int64  `json:"last_seen"`
	DeferUntil  int64  `json:"defer_until,omitempty"`
}

// MaintenancePredecessor exists only after a confirmed interrupt switch.
type MaintenancePredecessor struct {
	Agent     string `json:"agent"`
	SessionID string `json:"session_id"`
}

// MaintenanceAuditSession is an idle, previously successful scheduled audit thread.
type MaintenanceAuditSession struct {
	SessionID     string         `json:"session_id"`
	RolloutPath   string         `json:"rollout_path,omitempty"`
	SandboxPolicy map[string]any `json:"sandbox_policy,omitempty"`
	Profile       string         `json:"profile"`
}

type MaintenanceSession struct {
	ScheduledAuditID   string                  `json:"scheduled_audit_id,omitempty"`
	SessionProfile     string                  `json:"session_profile,omitempty"`
	CreationError      string                  `json:"creation_error,omitempty"`
	ObservedSessionID  string                  `json:"observed_session_id,omitempty"`
	ReconciledBy       string                  `json:"reconciled_by,omitempty"`
	Audit              *MaintenanceAudit       `json:"audit,omitempty"`
	Outcome            string                  `json:"outcome,omitempty"`
	SandboxPolicy      map[string]any          `json:"sandbox_policy,omitempty"`
	InputReasons       []string                `json:"input_reasons,omitempty"`
	InterruptRequested bool                    `json:"interrupt_requested,omitempty"`
	ID                 string                  `json:"id"`
	Backend            MaintenanceBackend      `json:"backend"`
	SessionID          string                  `json:"session_id,omitempty"`
	RolloutPath        string                  `json:"rollout_path,omitempty"`
	Phase              string                  `json:"phase"`
	InputID            string                  `json:"input_id,omitempty"`
	TurnID             string                  `json:"turn_id,omitempty"`
	Prompt             string                  `json:"prompt,omitempty"`
	StartedAt          int64                   `json:"started_at"`
	EndedAt            int64                   `json:"ended_at,omitempty"`
	Predecessor        *MaintenancePredecessor `json:"predecessor,omitempty"`
}

type MaintenanceStatus struct {
	IdleAuditSession   *MaintenanceAuditSession     `json:"idle_audit_session,omitempty"`
	PendingAudits      []string                     `json:"pending_audits,omitempty"`
	LastSnapshotAt     int64                        `json:"last_snapshot_at,omitempty"`
	ScheduledHour      string                       `json:"scheduled_hour,omitempty"`
	RecheckedAfter     string                       `json:"rechecked_after,omitempty"`
	CompletedAudit     *MaintenanceAudit            `json:"completed_audit,omitempty"`
	LastCICheck        int64                        `json:"last_ci_check,omitempty"`
	CICursor           string                       `json:"ci_cursor,omitempty"`
	CIError            string                       `json:"ci_error,omitempty"`
	Changes            map[string]MaintenanceChange `json:"changes,omitempty"`
	ChangeSequence     int64                        `json:"change_sequence,omitempty"`
	AuditedThrough     int64                        `json:"audited_through,omitempty"`
	LastAuditCompleted int64                        `json:"last_audit_completed,omitempty"`

	Failure        *MaintenanceSession     `json:"failure,omitempty"`
	Review         *MaintenanceReview      `json:"review,omitempty"`
	NativeError    string                  `json:"native_error,omitempty"`
	OpenCases      int                     `json:"open_cases"`
	Upgrade        *MaintenanceUpgrade     `json:"upgrade,omitempty"`
	CheckRequested bool                    `json:"check_requested,omitempty"`
	Format         int                     `json:"format"`
	Revision       int64                   `json:"revision"`
	Paused         bool                    `json:"paused"`
	Backend        MaintenanceBackend      `json:"backend"`
	PendingBackend *MaintenanceBackend     `json:"pending_backend,omitempty"`
	SwitchMode     string                  `json:"switch_mode,omitempty"`
	Active         *MaintenanceSession     `json:"active,omitempty"`
	Predecessor    *MaintenancePredecessor `json:"predecessor,omitempty"`
	Pending        []string                `json:"pending"`
	Cases          []MaintenanceCase       `json:"cases"`
	History        []MaintenanceSession    `json:"history"`
	LastCheck      int64                   `json:"last_check,omitempty"`
	LastAudit      int64                   `json:"last_audit,omitempty"`
	LastError      string                  `json:"last_error,omitempty"`
	EventSeq       int64                   `json:"event_seq,omitempty"`
	HealthFailures int                     `json:"health_failures,omitempty"`
}

// MaintenanceOverview is optional read-only reporting, without native content.
type MaintenanceOverview struct {
	LastAuditCompleted int64  `json:"last_audit_completed,omitempty"`
	Configured         bool   `json:"configured"`
	Available          bool   `json:"available"`
	ObservedAt         int64  `json:"observed_at"`
	LastCheck          int64  `json:"last_check,omitempty"`
	LastAudit          int64  `json:"last_audit,omitempty"`
	Paused             bool   `json:"paused"`
	OpenCases          int    `json:"open_cases"`
	Pending            int    `json:"pending"`
	Agent              string `json:"agent,omitempty"`
	SessionID          string `json:"session_id,omitempty"`
	Phase              string `json:"phase,omitempty"`
	UpgradePhase       string `json:"upgrade_phase,omitempty"`
	Error              string `json:"error,omitempty"`
}

type MaintenanceRequest struct {
	CreationID       string              `json:"creation_id,omitempty"`
	ExitEvidence     string              `json:"exit_evidence,omitempty"`
	ScheduledAudit   bool                `json:"scheduled_audit,omitempty"`
	Subject          *MaintenanceSubject `json:"subject,omitempty"`
	Verification     string              `json:"verification,omitempty"`
	TurnID           string              `json:"turn_id,omitempty"`
	Base             string              `json:"base,omitempty"`
	Head             string              `json:"head,omitempty"`
	ReleaseDir       string              `json:"release_dir,omitempty"`
	UpgradeID        string              `json:"upgrade_id,omitempty"`
	RequestID        string              `json:"request_id"`
	ExpectedRevision int64               `json:"expected_revision"`
	Backend          *MaintenanceBackend `json:"backend,omitempty"`
	Mode             string              `json:"mode,omitempty"`
	CaseID           string              `json:"case_id,omitempty"`
	Reason           string              `json:"reason,omitempty"`
	Until            int64               `json:"until,omitempty"`
	SessionID        string              `json:"session_id,omitempty"`
}

type MaintenanceReceipt struct {
	CaseID    string              `json:"case_id,omitempty"`
	Action    string              `json:"action,omitempty"`
	Request   *MaintenanceRequest `json:"request,omitempty"`
	RequestID string              `json:"request_id"`
	Revision  int64               `json:"revision"`
}

func MaintenanceActions() []string {
	return []string{"status", "cases", "show", "history", "session", "request", "check", "audit", "pause", "resume", "report", "intervene", "defer", "resolve", "config", "reconcile", "review", "upgrade-plan", "upgrade-apply", "upgrade-status"}
}

type MaintenanceReview struct {
	Outcome   string `json:"outcome,omitempty"`
	RequestID string `json:"request_id"`
	Base      string `json:"base"`
	Head      string `json:"head"`
	Phase     string `json:"phase"`
}

type MaintenanceUpgrade struct {
	ID             string          `json:"id"`
	ReleaseDir     string          `json:"release_dir"`
	ReleaseID      string          `json:"release_id"`
	ManifestSHA256 string          `json:"manifest_sha256"`
	ArtifactSHA256 string          `json:"artifact_sha256"`
	Phase          string          `json:"phase"`
	Binding        *UpgradeBinding `json:"binding,omitempty"`
}
