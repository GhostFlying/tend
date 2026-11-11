// Package updater supervises a single service and installs authorized releases.
// It never reads or writes application tables or service credentials.
package updater

import (
	"context"
	"errors"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
)

// Wire aliases keep the process supervisor independent from application packages.
type Binding = protocol.UpgradeBinding
type Grant = protocol.UpgradeGrant
type Health = protocol.UpgradeHealth

type Authority interface {
	Authorize(context.Context, Binding) (Grant, error)
	Health(context.Context, Binding) (Health, error)
}

// Config contains paths and non-secret process arguments. Credentials remain in
// existing environment files and are never copied into updater state or logs.
type Config struct {
	// ServiceConfig is the single fixed file eligible for configuration maintenance.
	ServiceConfig string   `json:"service_config,omitempty"`
	InstanceID    string   `json:"instance_id"`
	Role          string   `json:"role"`
	StateDir      string   `json:"state_dir"`
	InitialBinary string   `json:"initial_binary"`
	Args          []string `json:"args"`
	Dir           string   `json:"dir,omitempty"`
	EnvFiles      []string `json:"env_files,omitempty"`
	// DataFiles are explicitly selected service data files. Sidecars of SQLite
	// files are included. They must not name configuration or credential files.
	DataFiles          []string `json:"data_files,omitempty"`
	LogPath            string   `json:"log_path"`
	AuthorizeURL       string   `json:"authorize_url"`
	HealthURL          string   `json:"health_url"`
	AuthorityTokenFile string   `json:"authority_token_file,omitempty"`
	AuthoritySocket    string   `json:"authority_socket,omitempty"`
	CoordinatorSocket  string   `json:"coordinator_socket,omitempty"`
	// Protocol and StorageVersion describe this installed service's readable data.
	// Upgrade compatibility must include them before the current process stops.
	Protocol             int64 `json:"protocol"`
	StorageVersion       int64 `json:"storage_version"`
	HealthTimeoutSeconds int64 `json:"health_timeout_seconds,omitempty"`
	StopTimeoutSeconds   int64 `json:"stop_timeout_seconds,omitempty"`
}

type Identity = protocol.RecoveryIdentity
type Version struct {
	Protocol       int64            `json:"protocol"`
	StorageVersion int64            `json:"storage_version"`
	ReleaseID      string           `json:"release_id"`
	ManifestSHA256 string           `json:"manifest_sha256"`
	Artifact       release.Artifact `json:"artifact"`
	Binary         string           `json:"binary"`
}
type BackupFile = protocol.BackupFile
type Operation struct {
	HealthWindow          *HealthWindow        `json:"health_window,omitempty"`
	LaunchExit            *LaunchExitProof     `json:"launch_exit,omitempty"`
	Configuration         *StagedConfiguration `json:"configuration,omitempty"`
	RollbackAuthorization *Health              `json:"rollback_authorization,omitempty"`
	CandidateNotStarted   bool                 `json:"candidate_not_started,omitempty"`
	BoundedSourceOwned    bool                 `json:"bounded_source_supervised,omitempty"`
	ID                    string               `json:"id"`
	Kind                  string               `json:"kind"`
	Binding               Binding              `json:"binding"`
	Previous              Version              `json:"previous"`
	Candidate             Version              `json:"candidate"`
	PreviousProcess       Identity             `json:"previous_process"`
	CandidateProcess      Identity             `json:"candidate_process"`
	LaunchID              string               `json:"launch_id"`
	Phase                 string               `json:"phase"`
	Grant                 *Grant               `json:"grant,omitempty"`
	GrantBootID           string               `json:"grant_boot_id,omitempty"`
	GrantDeadlineMillis   int64                `json:"grant_deadline_millis,omitempty"`
	Backups               []BackupFile         `json:"backups,omitempty"`
	BackupDir             string               `json:"backup_dir,omitempty"`
	HealthCount           int                  `json:"health_count"`
	LastHealthSequence    uint64               `json:"last_health_sequence"`
	Irreversible          bool                 `json:"irreversible"`
	CancelRequested       bool                 `json:"cancel_requested"`
	StartedAt             time.Time            `json:"started_at"`
	HealthStartedAt       time.Time            `json:"health_started_at"`
	LastError             string               `json:"last_error,omitempty"`
}

// TerminalReceipt fences delayed requests after the single active operation moves on.
type TerminalReceipt struct {
	Binding Binding `json:"binding"`
	Phase   string  `json:"phase"`
}

// RollbackProof survives replacement of the active operation by crash recovery.
type RollbackProof struct {
	Binding Binding  `json:"binding"`
	Version Version  `json:"version"`
	Process Identity `json:"process"`
}

// CommittedProof retains the original upgrade across same-version restarts.
type CommittedProof struct {
	Binding Binding  `json:"binding"`
	Version Version  `json:"version"`
	Process Identity `json:"process"`
}
type State struct {
	DiagnosticLogProtections map[string]string                                 `json:"diagnostic_log_protections,omitempty"`
	BackupPrune              *protocol.BackupPruneState                        `json:"backup_prune,omitempty"`
	OperationHistory         map[string]Operation                              `json:"operation_history,omitempty"`
	BoundedRunnerGrants      map[string]protocol.BoundedRunnerMaintenanceGrant `json:"bounded_runner_grants,omitempty"`
	BoundedHistory           map[string]protocol.BoundedMaintenanceState       `json:"bounded_history,omitempty"`
	BoundedRunnerGrant       *protocol.BoundedRunnerMaintenanceGrant           `json:"bounded_runner_grant,omitempty"`
	Bounded                  *protocol.BoundedMaintenanceState                 `json:"bounded,omitempty"`
	Maintenance              *CommittedMaintenance                             `json:"committed_maintenance,omitempty"`
	MaintenanceHistory       map[string]CommittedMaintenance                   `json:"committed_maintenance_history,omitempty"`
	UpdaterBootID            string                                            `json:"updater_boot_id,omitempty"`
	Generation               uint64                                            `json:"generation,omitempty"`
	UpdaterEpoch             string                                            `json:"updater_epoch,omitempty"`
	Recovery                 *ForwardRecovery                                  `json:"recovery,omitempty"`
	Recoveries               map[string]ForwardRecovery                        `json:"recoveries,omitempty"`
	ProofHistory             []CommittedProof                                  `json:"proof_history,omitempty"`
	Configurations           map[string]StagedConfiguration                    `json:"configurations,omitempty"`
	CommittedProof           *CommittedProof                                   `json:"committed_proof,omitempty"`
	RollbackProof            *RollbackProof                                    `json:"rollback_proof,omitempty"`
	Terminals                map[string]TerminalReceipt                        `json:"terminals,omitempty"`
	Format                   int                                               `json:"format"`
	InstanceID               string                                            `json:"instance_id"`
	Current                  Version                                           `json:"current"`
	Process                  Identity                                          `json:"process"`
	Operation                *Operation                                        `json:"operation,omitempty"`
	LastError                string                                            `json:"last_error,omitempty"`
}

type Process interface {
	Inspect(context.Context, int) (Identity, error)
	Find(context.Context, string, string) ([]Identity, error)
	Start(context.Context, string, Config, map[string]string) (Identity, error)
	Stop(context.Context, Identity, time.Duration) error
}

var ErrNotStarted = errors.New("updater: worker process was definitely not started")
var ErrNotRunning = errors.New("updater: process is not running")
var ErrUnknown = errors.New("updater: outcome is unknown; reconcile before retrying")
var ErrUnsupported = errors.New("updater: automatic supervision is supported only on Linux")

// ProcessObservation is an ephemeral liveness observation, never a durable proof.
type ProcessObservation struct {
	Identity   Identity
	Executable string
}

// ProcessObserver separates cheap supervision from complete artifact validation.
type ProcessObserver interface {
	Observe(context.Context, int) (ProcessObservation, error)
}
