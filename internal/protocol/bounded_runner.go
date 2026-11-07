package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

const OpAuthorizeBoundedRunnerMaintenance = "hosts.authorize_bounded_maintenance"
const OpReleaseBoundedRunnerAdmission = "hosts.release_bounded_admission"

// RunnerBoundedMaintenancePlan fixes the owner-authorized target. Local process
// and journal facts are independently rechecked by the trusted installer.
type RunnerBoundedMaintenancePlan struct {
	HostID string `json:"host_id"`
	// SourceOperation identifies an original force-authorized launch whose owned
	// Wait receipt proves its root child exited before any accepted Hello.
	SourceOperation *MaintenanceOperationSource `json:"source_operation,omitempty"`
	// NodeSelector is a configured host credential selector, not proof of a
	// physical tailnet node. The installer verifies local process/data identity.
	NodeSelector            string                          `json:"node_selector"`
	RunnerInstanceID        string                          `json:"runner_instance_id"`
	RunnerGeneration        int64                           `json:"runner_generation"`
	Source                  RecoveryIdentity                `json:"source"`
	Journal                 BoundedDataIdentity             `json:"journal"`
	ReleaseDir              string                          `json:"release_dir"`
	Target                  UpgradeBinding                  `json:"target"`
	ExpectedControlRevision int64                           `json:"expected_control_revision"`
	Authorization           BoundedMaintenanceAuthorization `json:"authorization"`
}

// BoundedRunnerMaintenanceGrant retains complete frozen Run observations. It
// authorizes replacement of this runner, never replay of its business work.
type BoundedRunnerMaintenanceGrant struct {
	RecoveryAuthorizationID string                       `json:"recovery_authorization_id"`
	Plan                    RunnerBoundedMaintenancePlan `json:"plan"`
	ControlRevision         int64                        `json:"control_revision"`
	Runs                    []CommittedRetainedRun       `json:"runs"`
	ProtectionSetSHA256     string                       `json:"protection_set_sha256"`
}

// BoundedRunnerAdmissionRelease changes only global admission. Every original
// run, lease, native identity and pending request retains its own quarantine.
type BoundedRunnerAdmissionRelease struct {
	RequestID               string              `json:"request_id"`
	HostID                  string              `json:"host_id"`
	RecoveryAuthorizationID string              `json:"recovery_authorization_id"`
	Successor               UpgradeBinding      `json:"successor"`
	RunnerGeneration        int64               `json:"runner_generation"`
	Journal                 BoundedDataIdentity `json:"journal"`
	ExpectedControlRevision int64               `json:"expected_control_revision"`
	Reason                  string              `json:"reason"`
}

// RunnerBoundedMaintenancePlanSHA256 binds the source and target independently
// of the authorization which refers to that digest. Force, extension and
// replay therefore cannot silently change the approved source plan.
func RunnerBoundedMaintenancePlanSHA256(p RunnerBoundedMaintenancePlan) (string, error) {
	p.Authorization = BoundedMaintenanceAuthorization{}
	p.ExpectedControlRevision = 0
	raw, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
