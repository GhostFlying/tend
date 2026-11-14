package updater

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
)

// SelfUpdateRequest preserves one identity and payload throughout reconciliation.
type SelfUpdateRequest struct {
	Target           protocol.UpdaterSelfTarget `json:"target"`
	UpgradeID        string                     `json:"upgrade_id"`
	Release          string                     `json:"release"`
	ReleaseSetSHA256 string                     `json:"release_set_sha256"`
}

type SelfUpdatePlan struct {
	StateDirectoryDevice uint64            `json:"state_directory_device,omitempty"`
	StateDirectoryInode  uint64            `json:"state_directory_inode,omitempty"`
	StateDirectoryMode   uint32            `json:"state_directory_mode,omitempty"`
	HandoffSHA256        string            `json:"handoff_sha256,omitempty"`
	CommittedHeadSHA256  string            `json:"committed_head_sha256,omitempty"`
	Request              SelfUpdateRequest `json:"request"`
	Unit                 string            `json:"unit"`
	UnitSHA256           string            `json:"unit_sha256"`
	ManagerSHA256        string            `json:"manager_sha256"`
	Updater              Identity          `json:"updater"`
	UpdaterBinary        string            `json:"updater_binary"`
	Child                Identity          `json:"child"`
	ConfigFile           string            `json:"config_file"`
	ConfigSHA256         string            `json:"config_sha256"`
	Candidate            string            `json:"candidate"`
	CandidateSHA256      string            `json:"candidate_sha256"`
	LockDevice           uint64            `json:"lock_device"`
	LockInode            uint64            `json:"lock_inode"`
	// Standalone bootstrap cannot assert modern shutdown behavior. It supplies
	// reviewed evidence bound to the actual old artifact instead.
	PreparedBy         Identity `json:"prepared_by"`
	ExitEvidence       string   `json:"exit_evidence,omitempty"`
	ExitEvidenceSHA256 string   `json:"exit_evidence_sha256,omitempty"`
}

type SelfUpdateJournal struct {
	Format              int                          `json:"format"`
	Plan                SelfUpdatePlan               `json:"plan"`
	Phase               string                       `json:"phase"`
	InstallerDispatched bool                         `json:"installer_dispatched,omitempty"`
	OriginalState       json.RawMessage              `json:"original_state,omitempty"`
	Receipt             *protocol.UpdaterSelfReceipt `json:"receipt,omitempty"`
	LastError           string                       `json:"last_error,omitempty"`
}

type SelfUpdateStatus struct {
	Supported   bool                            `json:"supported"`
	Observation protocol.UpdaterSelfObservation `json:"observation"`
	Journal     *SelfUpdateJournal              `json:"journal,omitempty"`
	Error       string                          `json:"error,omitempty"`
}

func selfPath(cfg Config) string { return filepath.Join(cfg.StateDir, "self-update", "journal.json") }

// SelfUpdateJournalPath names the private journal shared with the installer.
func SelfUpdateJournalPath(cfg Config) string { return selfPath(cfg) }
func readSelf(cfg Config) (SelfUpdateJournal, error) {
	var j SelfUpdateJournal
	raw, err := privateFile(selfPath(cfg))
	if missingSelf(err) {
		raw, err = privateFile(filepath.Join(cfg.StateDir, "self-update.json"))
	}
	if err != nil {
		return j, err
	}
	if err = strictJSON(raw, &j); err != nil {
		return j, err
	}
	if j.Format != 1 {
		return j, errors.New("updater: unsupported self-update journal")
	}
	return j, nil
}
func ReadSelfUpdatePlan(path string) (SelfUpdatePlan, error) {
	var p SelfUpdatePlan
	raw, err := privateFile(path)
	if err != nil {
		return p, err
	}
	err = strictJSON(raw, &p)
	return p, err
}

// EnsureSelfUpdate submits once, then only observes the original attempt. A
// disappearing socket during supervisor replacement grants no new dispatch.
func EnsureSelfUpdate(ctx context.Context, socket string, request SelfUpdateRequest) (protocol.UpdaterSelfObservation, error) {
	status, err := SelfUpdateCall(ctx, socket, &request)
	if err != nil && status.Error != "" {
		return status.Observation, err
	}
	for {
		if rejected := status.Observation.Rejection; rejected != nil && rejected.RequestID == request.Target.RequestID && rejected.UpgradeID == request.UpgradeID && rejected.ReleaseSetSHA256 == request.ReleaseSetSHA256 && rejected.InstanceID == status.Observation.InstanceID {
			return status.Observation, errors.Join(errors.New("updater: preparation rejected before installer dispatch"), err)
		}
		r := status.Observation.Receipt
		if err == nil && r != nil && r.RequestID == request.Target.RequestID && r.UpgradeID == request.UpgradeID && r.ReleaseSetSHA256 == request.ReleaseSetSHA256 && r.InstanceID == status.Observation.InstanceID && status.Observation.Process.SHA256 == r.Installed.SHA256 && status.Observation.SupervisedProcess.PID > 1 {
			return status.Observation, nil
		}
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return status.Observation, ErrUnknown
		case <-timer.C:
		}
		status, err = ObserveSelfUpdate(ctx, socket, request.Target.RequestID)
		if err == nil && status.Journal != nil && digest(status.Journal.Plan.Request) != digest(request) {
			return status.Observation, errors.New("updater: observed another handoff attempt")
		}
	}
}

// selfActions separates the durable phase machine from process and manager IO.
type selfActions interface {
	Check(context.Context, SelfUpdatePlan, string) error
	Stop(context.Context, SelfUpdatePlan) error
	Install(context.Context, SelfUpdatePlan) (json.RawMessage, func(), error)
	Start(context.Context, SelfUpdatePlan) error
}

func advanceSelf(ctx context.Context, j *SelfUpdateJournal, save func() error, a selfActions, now func() time.Time) error {
	persist := func(phase string) error { j.Phase = phase; j.LastError = ""; return save() }
	if j.Phase == "verified" {
		return nil
	}
	if j.Phase == "prepared" {
		if now().UnixMilli() >= j.Plan.Request.Target.ExpiresAtMillis {
			return errors.New("updater: self-update authority expired")
		}
		if err := a.Check(ctx, j.Plan, "active"); err != nil {
			return err
		}
		if err := persist("stopping"); err != nil {
			return err
		}
		if err := a.Stop(ctx, j.Plan); err != nil {
			return err
		}
	}
	if j.Phase == "stopping" {
		if err := a.Check(ctx, j.Plan, "stopped"); err != nil {
			return err
		}
		if err := persist("installing"); err != nil {
			return err
		}
	}
	if j.Phase == "installing" {
		if now().UnixMilli() >= j.Plan.Request.Target.ExpiresAtMillis {
			return errors.New("updater: self-update authority expired before install")
		}
		raw, releaseLock, err := a.Install(ctx, j.Plan)
		if err != nil {
			return err
		}
		defer releaseLock()
		j.OriginalState = raw
		if err := persist("starting"); err != nil {
			return err
		}
		releaseLock()
		if err := a.Start(ctx, j.Plan); err != nil {
			return err
		}
	}
	// The new lock holder writes adoption; StartUnit alone proves no completion.
	return nil
}
func archiveSelf(cfg Config, j SelfUpdateJournal) error {
	terminal, err := terminalSelf(cfg, j)
	if err != nil {
		return err
	}
	if !terminal {
		return ErrUnknown
	}
	path := filepath.Join(cfg.StateDir, "self-update-"+bytesDigest([]byte(j.Plan.Request.Target.RequestID))+".json")
	raw, err := privateFile(path)
	if err == nil {
		var prior SelfUpdateJournal
		if err = strictJSON(raw, &prior); err != nil {
			return err
		}
		if digest(prior) != digest(j) {
			return errors.New("updater: historical self-update receipt conflicts")
		}
		return nil
	}
	if !missingSelf(err) {
		return err
	}
	return writeJSON(path, j)
}
func missingSelf(err error) bool { return os.IsNotExist(err) }

func (s *Supervisor) requireSelfUpdate(ctx context.Context, id, dir string) error {
	b, err := release.LoadService(dir, s.cfg.Role)
	if err != nil {
		return err
	}
	if b.Set == nil || b.Set.Format < 2 {
		return nil
	}
	j, err := readSelf(s.cfg)
	if err != nil {
		return errors.New("updater: complete upgrade requires verified supervisor handoff")
	}
	if j.Phase != "verified" || j.Receipt == nil || j.Plan.Request.UpgradeID != id || j.Plan.Request.ReleaseSetSHA256 != b.SetSHA256 || j.Receipt.InstanceID != s.cfg.InstanceID || j.Receipt.Installed.SHA256 != j.Plan.CandidateSHA256 {
		return errors.New("updater: supervisor handoff does not belong to service upgrade")
	}
	if s.selfIdentity.SHA256 != j.Plan.CandidateSHA256 || s.selfIdentity.PID != os.Getpid() {
		return ErrUnknown
	}
	_, err = s.observeCurrent(ctx, s.state.Process)
	return err
}
