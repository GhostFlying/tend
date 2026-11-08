//go:build linux

package updater

import (
	"errors"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
)

// rejectSelf is called under the supervisor mutex only before a dispatch intent
// exists. The immutable request receipt survives a lost response and restart.
func (s *Supervisor) rejectSelf(r SelfUpdateRequest, cause error) (SelfUpdateStatus, error) {
	out := SelfUpdateStatus{Supported: true, Observation: protocol.UpdaterSelfObservation{InstanceID: s.cfg.InstanceID}}
	if r.Target.RequestID == "" || r.UpgradeID == "" || !release.ValidSHA(r.ReleaseSetSHA256) {
		return out, cause
	}
	prior, err := readSelfRejection(s.cfg, r.Target.RequestID)
	if err != nil {
		return out, errors.Join(cause, err)
	}
	if prior != nil {
		if digest(prior.Request) != digest(r) {
			return out, errors.New("updater: rejected request payload conflicts with history")
		}
		out.Observation.Rejection = &prior.Proof
		return out, cause
	}
	// Standalone apply uses this same lock. A concurrent installer may already
	// have crossed the stop boundary; only the lock holder can prove no dispatch.
	lock, err := installerLock(s.cfg)
	if err != nil {
		return out, errors.Join(cause, ErrUnknown)
	}
	defer lock.Close()
	current, readErr := readSelf(s.cfg)
	if readErr != nil && !missingSelf(readErr) {
		return out, errors.Join(cause, readErr)
	}
	if readErr == nil && current.Phase != "verified" {
		return out, errors.Join(cause, ErrUnknown)
	}
	if s.opts.Now == nil {
		return out, errors.Join(cause, errors.New("updater: rejection clock unavailable"))
	}
	return persistSelfRejection(s.cfg, r, s.opts.Now(), cause)
}
