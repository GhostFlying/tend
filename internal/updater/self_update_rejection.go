package updater

import (
	"errors"
	"os"
	"path/filepath"
	"time"

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

// persistSelfRejection requires the installer lock and proof that no stop intent
// has been committed. Both preparation and pre-stop validation can establish it.
func persistSelfRejection(cfg Config, r SelfUpdateRequest, now time.Time, cause error) (SelfUpdateStatus, error) {
	out := SelfUpdateStatus{Supported: true, Observation: protocol.UpdaterSelfObservation{InstanceID: cfg.InstanceID}}
	if r.Target.RequestID == "" || r.UpgradeID == "" || !release.ValidSHA(r.ReleaseSetSHA256) {
		return out, cause
	}
	if prior, err := readSelfRejection(cfg, r.Target.RequestID); err != nil {
		return out, errors.Join(cause, err)
	} else if prior != nil {
		if digest(prior.Request) != digest(r) {
			return out, errors.New("updater: rejected request payload conflicts with history")
		}
		out.Observation.Rejection = &prior.Proof
		return out, cause
	}
	proof := protocol.UpdaterSelfRejection{RequestID: r.Target.RequestID, UpgradeID: r.UpgradeID, ReleaseSetSHA256: r.ReleaseSetSHA256, InstanceID: cfg.InstanceID, RejectedAtMillis: now.UnixMilli()}
	if proof.RejectedAtMillis <= 0 {
		return out, errors.Join(cause, errors.New("updater: rejection clock unavailable"))
	}
	if err := writeJSON(selfRejectionPath(cfg, r.Target.RequestID), selfRejection{Format: 1, Request: r, Proof: proof}); err != nil {
		return out, errors.Join(cause, err)
	}
	out.Observation.Rejection = &proof
	return out, cause
}

func rejectRecordedSelfRequest(cfg Config, request SelfUpdateRequest) error {
	prior, err := readSelfRejection(cfg, request.Target.RequestID)
	if err != nil {
		return err
	}
	if prior != nil {
		return errors.New("updater: original request was rejected before supervisor stop; use a new upgrade")
	}
	return nil
}

func rejectedSelfBeforeStop(cfg Config, j SelfUpdateJournal) (*selfRejection, error) {
	if j.Phase != "prepared" {
		return nil, nil
	}
	r, err := readSelfRejection(cfg, j.Plan.Request.Target.RequestID)
	if err != nil {
		return nil, err
	}
	if r != nil && digest(r.Request) != digest(j.Plan.Request) {
		return nil, errors.New("updater: rejection does not match the current attempt")
	}
	return r, nil
}

func terminalSelf(cfg Config, j SelfUpdateJournal) (bool, error) {
	if j.Phase == "verified" && j.Receipt != nil {
		return true, nil
	}
	r, err := rejectedSelfBeforeStop(cfg, j)
	return r != nil, err
}
