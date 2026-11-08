package updater

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
)

type selfRejection struct {
	Format  int                           `json:"format"`
	Request SelfUpdateRequest             `json:"request"`
	Proof   protocol.UpdaterSelfRejection `json:"proof"`
}

func selfRejectionPath(cfg Config, requestID string) string {
	return filepath.Join(cfg.StateDir, "self-rejected-"+bytesDigest([]byte(requestID))+".json")
}

func readSelfRejection(cfg Config, requestID string) (*selfRejection, error) {
	raw, err := privateFile(selfRejectionPath(cfg, requestID))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r selfRejection
	if err := strictJSON(raw, &r); err != nil {
		return nil, err
	}
	if r.Format != 1 || r.Request.Target.RequestID != requestID || r.Proof.RequestID != requestID || r.Proof.InstanceID != cfg.InstanceID || r.Proof.UpgradeID != r.Request.UpgradeID || r.Proof.ReleaseSetSHA256 != r.Request.ReleaseSetSHA256 || r.Proof.RejectedAtMillis <= 0 {
		return nil, errors.New("updater: invalid preparation rejection receipt")
	}
	return &r, nil
}

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
	if s.opts.Now == nil {
		return out, errors.Join(cause, errors.New("updater: rejection clock unavailable"))
	}
	now := s.opts.Now()
	proof := protocol.UpdaterSelfRejection{RequestID: r.Target.RequestID, UpgradeID: r.UpgradeID, ReleaseSetSHA256: r.ReleaseSetSHA256, InstanceID: s.cfg.InstanceID, RejectedAtMillis: now.UnixMilli()}
	if proof.RejectedAtMillis <= 0 {
		return out, errors.Join(cause, errors.New("updater: rejection clock unavailable"))
	}
	if err := writeJSON(selfRejectionPath(s.cfg, r.Target.RequestID), selfRejection{Format: 1, Request: r, Proof: proof}); err != nil {
		return out, errors.Join(cause, err)
	}
	out.Observation.Rejection = &proof
	return out, cause
}
