package protocol

// BackupPolicy uses one physical checkpoint for all matching retention buckets.
// Explicit protection and unknown recovery responsibility always take priority.
type BackupPolicy struct {
	MinimumVerified        int `json:"minimum_verified"`
	Daily                  int `json:"daily"`
	Weekly                 int `json:"weekly"`
	MigrationDays          int `json:"migration_days"`
	IncompatibleDays       int `json:"incompatible_days"`
	SuccessfulDrillHours   int `json:"successful_drill_hours"`
	FailedDrillDays        int `json:"failed_drill_days"`
	DiagnosticCompressDays int `json:"diagnostic_compress_days"`
	DiagnosticRetainDays   int `json:"diagnostic_retain_days"`
}

type BackupFile struct {
	Path         string `json:"path"`
	Name         string `json:"name"`
	Existed      bool   `json:"existed"`
	SHA256       string `json:"sha256,omitempty"`
	ObjectSHA256 string `json:"object_sha256,omitempty"`
	Mode         uint32 `json:"mode"`
}

type BackupCheckpoint struct {
	ID                         string       `json:"id"`
	Directory                  string       `json:"directory"`
	CreatedAtMillis            int64        `json:"created_at_ms"`
	VerifiedAtMillis           int64        `json:"verified_at_ms,omitempty"`
	ManifestSHA256             string       `json:"manifest_sha256"`
	Class                      string       `json:"class"`
	IncompatibleSchema         bool         `json:"incompatible_schema,omitempty"`
	ObservationCompletedMillis int64        `json:"observation_completed_ms,omitempty"`
	RestoreProofSHA256         string       `json:"restore_proof_sha256,omitempty"`
	CoveredByCheckpointID      string       `json:"covered_by_checkpoint_id,omitempty"`
	ProtectedReason            string       `json:"protected_reason,omitempty"`
	Recoverable                bool         `json:"recoverable"`
	Unknown                    bool         `json:"unknown"`
	Files                      []BackupFile `json:"files,omitempty"`
	Bytes                      int64        `json:"bytes"`
	AllocatedBytes             int64        `json:"allocated_bytes,omitempty"`
	Decision                   string       `json:"decision"`
	Reasons                    []string     `json:"reasons"`
}

type BackupObject struct {
	SHA256         string   `json:"sha256"`
	Bytes          int64    `json:"bytes"`
	AllocatedBytes int64    `json:"allocated_bytes,omitempty"`
	Decision       string   `json:"decision"`
	Reasons        []string `json:"reasons"`
}

type DiagnosticLog struct {
	ID             string   `json:"id"`
	Path           string   `json:"path"`
	ArtifactSHA256 string   `json:"artifact_sha256,omitempty"`
	BodySHA256     string   `json:"body_sha256,omitempty"`
	ClosedAtMillis int64    `json:"closed_at_ms,omitempty"`
	Closed         bool     `json:"closed"`
	Compressed     bool     `json:"compressed"`
	Bytes          int64    `json:"bytes"`
	AllocatedBytes int64    `json:"allocated_bytes,omitempty"`
	Decision       string   `json:"decision"`
	Reasons        []string `json:"reasons"`
}
type DiagnosticLogProtection struct {
	LogID              string `json:"log_id"`
	ExpectedGeneration uint64 `json:"expected_generation"`
	Reason             string `json:"reason"`
}

type BackupPreview struct {
	DiagnosticLogs []DiagnosticLog `json:"diagnostic_logs"`

	Format                          int                `json:"format"`
	InstanceID                      string             `json:"instance_id"`
	ExpectedGeneration              uint64             `json:"expected_generation"`
	CreatedAtMillis                 int64              `json:"created_at_ms"`
	Policy                          BackupPolicy       `json:"policy"`
	Checkpoints                     []BackupCheckpoint `json:"checkpoints"`
	Objects                         []BackupObject     `json:"objects"`
	RecoveryCheckpointID            string             `json:"recovery_checkpoint_id,omitempty"`
	ExpectedReclaimedBytes          int64              `json:"expected_reclaimed_bytes"`
	ExpectedReclaimedAllocatedBytes int64              `json:"expected_reclaimed_allocated_bytes"`
	ExclusivePhysicalBytesKnown     bool               `json:"exclusive_physical_bytes_known"`
	SHA256                          string             `json:"sha256"`
}

type BackupVerification struct {
	CheckpointID               string `json:"checkpoint_id"`
	ExpectedGeneration         uint64 `json:"expected_generation"`
	ManifestSHA256             string `json:"manifest_sha256"`
	Class                      string `json:"class,omitempty"`
	IncompatibleSchema         bool   `json:"incompatible_schema,omitempty"`
	ObservationCompletedMillis int64  `json:"observation_completed_ms,omitempty"`
	ProtectedReason            string `json:"protected_reason,omitempty"`
	RestoreProofPath           string `json:"restore_proof_path,omitempty"`
	RestoreProofSHA256         string `json:"restore_proof_sha256,omitempty"`
}

// BackupRestoreProof is produced by an actual completed restore exercise. Byte
// integrity alone is not a claim that a checkpoint has been restored successfully.
type BackupRestoreProof struct {
	UniqueEvidencePath    string `json:"unique_evidence_path,omitempty"`
	SourceArtifactSHA256  string `json:"source_artifact_sha256,omitempty"`
	SourceManifestSHA256  string `json:"source_manifest_sha256,omitempty"`
	SourceReleaseID       string `json:"source_release_id,omitempty"`
	SourceProtocol        int64  `json:"source_protocol,omitempty"`
	SourceStorageVersion  int64  `json:"source_storage_version,omitempty"`
	Explained             bool   `json:"explained,omitempty"`
	CoveredByCheckpointID string `json:"covered_by_checkpoint_id,omitempty"`
	UniqueEvidenceSHA256  string `json:"unique_evidence_sha256,omitempty"`
	Format                int    `json:"format"`
	CheckpointID          string `json:"checkpoint_id"`
	ManifestSHA256        string `json:"manifest_sha256"`
	Restored              bool   `json:"restored"`
	VerifiedAtMillis      int64  `json:"verified_at_ms"`
}

type BackupPruneRequest struct {
	Preview BackupPreview `json:"preview"`
}

type BackupPruneState struct {
	InFlight string `json:"in_flight,omitempty"`

	Preview                 BackupPreview `json:"preview"`
	Phase                   string        `json:"phase"`
	Completed               []string      `json:"completed"`
	ReclaimedBytes          int64         `json:"reclaimed_bytes"`
	ReclaimedAllocatedBytes int64         `json:"reclaimed_allocated_bytes"`
	StartedAtMillis         int64         `json:"started_at_ms"`
	FinishedAtMillis        int64         `json:"finished_at_ms,omitempty"`
	LastError               string        `json:"last_error,omitempty"`
}

type BackupStatus struct {
	Preview BackupPreview     `json:"preview"`
	Prune   *BackupPruneState `json:"prune,omitempty"`
}
