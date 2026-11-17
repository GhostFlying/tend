package upgradectl

import (
	"context"
	"errors"
	"github.com/GhostFlying/tend/internal/protocol"
)

func (c *Controller) installationReadiness(ctx context.Context, binding protocol.UpgradeBinding) (protocol.InstallationReadiness, error) {
	ready := protocol.InstallationReadiness{Binding: binding, Protocol: protocol.CurrentRunnerProtocol}
	if binding.InstanceID != c.cfg.Instance || binding.ProcessInstanceID != c.cfg.ProcessInstance || binding.ArtifactSHA256 != c.cfg.BinarySHA256 {
		return ready, errors.New("upgradectl: readiness belongs to another process")
	}
	backend, ok := c.app.(interface {
		InstallationVersion(context.Context) (int64, error)
	})
	if !ok {
		return ready, errors.New("upgradectl: installation storage unavailable")
	}
	version, err := backend.InstallationVersion(ctx)
	ready.StorageVersion, ready.Ready = version, err == nil && version > 0
	if err == nil {
		if backend, ok := c.app.(interface {
			InstallationSupersededUpgrade(context.Context) (string, error)
		}); ok {
			ready.SupersededUpgradeID, err = backend.InstallationSupersededUpgrade(ctx)
			ready.Ready = ready.Ready && err == nil
		}
	}
	return ready, err
}
