package updater

import (
	"context"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
)

// LaunchExitProof records an owned child wait result, not a process identity.
// It can only authorize a new maintenance after an independently empty launch scan.
type LaunchExitProof = protocol.MaintenanceLaunchExitProof

type launchExitedError struct{ proof LaunchExitProof }

func (e *launchExitedError) Error() string {
	return "updater: owned maintenance child exited before identity observation"
}
func (e *launchExitedError) Unwrap() error { return ErrUnknown }

type maintenanceLauncher interface {
	StartMaintenance(context.Context, string, Config, map[string]string) (Identity, error)
}

// ValidateMaintenanceLaunchExit verifies the distinct persisted exit provenance.
// An empty /proc search alone never establishes this fact.
func ValidateMaintenanceLaunchExit(m CommittedMaintenance) error {
	p := m.LaunchExit
	if m.Phase != "launch_exited" || !m.Irreversible || m.Process != (Identity{}) || p == nil || p.PID <= 1 || p.LaunchID != m.Plan.Successor.ProcessInstanceID || p.ArtifactSHA256 != m.Plan.Successor.ArtifactSHA256 || !release.ValidSHA(p.ArtifactSHA256) || p.ExitCode < -1 || p.ExitCode > 255 || p.Signal < 0 || p.Signal > 64 || (p.Signal == 0 && p.ExitCode < 0) || (p.Signal != 0 && p.ExitCode != -1) {
		return errors.New("updater: owned maintenance launch exit proof is incomplete")
	}
	return nil
}

func (s *Supervisor) maintenanceLaunchGone(ctx context.Context, parent *CommittedMaintenance) error {
	if err := ValidateMaintenanceLaunchExit(*parent); err != nil {
		return err
	}
	found, err := s.opts.Process.Find(ctx, parent.LaunchExit.LaunchID, parent.LaunchExit.ArtifactSHA256)
	if err != nil {
		return err
	}
	if len(found) != 0 {
		return ErrUnknown
	}
	return nil
}
