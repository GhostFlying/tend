//go:build linux

package updater

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
)

type CommittedHandoffPlan struct {
	Maintenance               protocol.CommittedMaintenancePlan `json:"maintenance"`
	Unit                      string                            `json:"unit"`
	UnitSHA256                string                            `json:"unit_sha256"`
	ManagerSHA256             string                            `json:"manager_sha256"`
	Updater                   Identity                          `json:"updater"`
	UpdaterBinary             string                            `json:"updater_binary"`
	ConfigFile                string                            `json:"config_file"`
	ConfigFileSHA256          string                            `json:"config_file_sha256"`
	Candidate                 string                            `json:"candidate"`
	CandidateSHA256           string                            `json:"candidate_sha256"`
	LockDevice                uint64                            `json:"lock_device"`
	LockInode                 uint64                            `json:"lock_inode"`
	OldUpdaterEvidence        string                            `json:"old_updater_evidence"`
	OldUpdaterEvidenceSHA256  string                            `json:"old_updater_evidence_sha256"`
	ParentHandoffSHA256       string                            `json:"parent_handoff_sha256,omitempty"`
	ParentCommittedHeadSHA256 string                            `json:"parent_committed_head_sha256,omitempty"`
}

type CommittedHandoffJournal struct {
	Plan          CommittedHandoffPlan `json:"plan"`
	Generation    uint64               `json:"generation"`
	Phase         string               `json:"phase"`
	OriginalState json.RawMessage      `json:"original_state"`
	PreviousHead  []byte               `json:"previous_head,omitempty"`
	Continuations map[string]uint64    `json:"continuations,omitempty"`
	LastError     string               `json:"last_error,omitempty"`
	State         *State               `json:"state,omitempty"`
	Observation   *HandoffObservation  `json:"observation,omitempty"`
}

type committedHandoffHead struct {
	MaintenanceID string `json:"maintenance_id"`
	PlanSHA256    string `json:"plan_sha256"`
}

func committedHeadPath(cfg Config) string {
	return filepath.Join(cfg.StateDir, "committed-handoff.json")
}
func committedJournalPath(cfg Config, id string) string {
	return filepath.Join(cfg.StateDir, "committed-handoffs", id+".json")
}

func committedAdapter(p CommittedHandoffPlan) HandoffPlan {
	resources := maintenanceResources(p.Maintenance)
	resources.OriginalProcess = p.Maintenance.PreviousProcess
	resources.ConfigSHA256 = p.Maintenance.ConfigSHA256
	return HandoffPlan{Recovery: resources, Unit: p.Unit, UnitSHA256: p.UnitSHA256, ManagerSHA256: p.ManagerSHA256, Updater: p.Updater, UpdaterBinary: p.UpdaterBinary, ConfigFile: p.ConfigFile, ConfigFileSHA256: p.ConfigFileSHA256, Candidate: p.Candidate, CandidateSHA256: p.CandidateSHA256, LockDevice: p.LockDevice, LockInode: p.LockInode, OldUpdaterEvidence: p.OldUpdaterEvidence, OldUpdaterEvidenceSHA256: p.OldUpdaterEvidenceSHA256}
}

func ReadCommittedHandoffPlan(path string) (CommittedHandoffPlan, error) {
	var p CommittedHandoffPlan
	raw, err := privateFile(path)
	if err != nil {
		return p, err
	}
	err = strictJSON(raw, &p)
	return p, err
}

func optionalPrivate(path string) ([]byte, error) {
	raw, err := privateFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return raw, err
}

func verifyCommittedParent(cfg Config, p CommittedHandoffPlan) error {
	raw, err := optionalPrivate(handoffPath(cfg))
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		if p.ParentHandoffSHA256 != "" {
			return errors.New("updater: previous handoff history disappeared")
		}
	} else if bytesDigest(raw) != p.ParentHandoffSHA256 {
		return errors.New("updater: previous handoff history changed")
	}
	return nil
}

// PrepareCommittedHandoffPlan fills observations without touching the running
// service or requiring new APIs in the old updater/server.
func PrepareCommittedHandoffPlan(ctx context.Context, cfg Config, p CommittedHandoffPlan) (CommittedHandoffPlan, error) {
	normalizeConfig(&cfg)
	m, policy, err := managerSnapshot(ctx, p.Unit)
	if err != nil {
		return p, err
	}
	pid, err := strconv.Atoi(m["MainPID"])
	if err != nil || pid < 2 {
		return p, ErrUnknown
	}
	if p.Updater.PID == 0 {
		p.Updater, err = (LinuxProcess{}).Inspect(ctx, pid)
		if err != nil {
			return p, err
		}
	}
	if p.ManagerSHA256 == "" {
		p.ManagerSHA256 = policy
	}
	if p.UnitSHA256 == "" {
		p.UnitSHA256, err = unitDigest(ctx, p.Unit)
		if err != nil {
			return p, err
		}
	}
	if p.ConfigFileSHA256 == "" {
		raw, e := privateFile(p.ConfigFile)
		if e != nil {
			return p, e
		}
		p.ConfigFileSHA256 = bytesDigest(raw)
	}
	if p.CandidateSHA256 == "" {
		p.CandidateSHA256, err = hashFile(p.Candidate)
		if err != nil {
			return p, err
		}
	}
	if p.LockInode == 0 {
		p.LockDevice, p.LockInode, err = lockIdentity(cfg.StateDir)
		if err != nil {
			return p, err
		}
	}
	state, err := readState(filepath.Join(cfg.StateDir, "state.json"))
	if err != nil {
		return p, err
	}
	if state.CommittedProof == nil {
		return p, errors.New("updater: no committed service proof")
	}
	q := &p.Maintenance
	if q.ParentMaintenanceID != "" {
		parent := state.Maintenance
		if parent == nil || parent.Plan.MaintenanceID != q.ParentMaintenanceID {
			return p, errors.New("updater: parent maintenance is not current")
		}
		if q.ParentPlan == nil {
			copy := parent.Plan
			q.ParentPlan = &copy
		}
		if q.ParentReceipt == nil && parent.Receipt != nil {
			copy := *parent.Receipt
			q.ParentReceipt = &copy
		}
		if q.ParentPlanSHA256 == "" {
			q.ParentPlanSHA256 = digest(parent.Plan)
		}
		if q.ParentArchiveSHA256 == "" {
			q.ParentArchiveSHA256 = digest(parent.ArchiveAuthorization)
		}
		if q.ParentGeneration == 0 {
			q.ParentGeneration = state.Generation
		}
		if q.Previous.InstanceID == "" {
			q.Previous = parent.Plan.Successor
		}
		if q.PreviousProcess.PID == 0 {
			q.PreviousProcess = parent.Process
		}
	}
	if q.ConfigSHA256 == "" {
		q.ConfigSHA256 = digest(cfg)
	}
	if q.StateSHA256 == "" {
		q.StateSHA256 = digest(state)
		q.ExpectedGeneration = state.Generation
	}
	if q.PreviousProcess.PID == 0 {
		q.PreviousProcess = state.Process
	}
	if q.Previous.InstanceID == "" {
		q.Previous = state.CommittedProof.Binding
	}
	if q.Successor.InstanceID == "" {
		q.Successor.InstanceID = cfg.InstanceID
	}
	if q.Successor.UpgradeID == "" {
		q.Successor.UpgradeID = q.MaintenanceID
	}
	b, err := release.Load(q.ReleaseDir)
	if err != nil {
		return p, err
	}
	a, err := b.Artifact("server", "linux", runtime.GOARCH)
	if err != nil {
		return p, err
	}
	if q.Successor.ReleaseID == "" {
		q.Successor.ReleaseID = b.Manifest.ReleaseID
	}
	if q.Successor.ManifestSHA256 == "" {
		q.Successor.ManifestSHA256 = b.ManifestSHA256
	}
	if q.Successor.ArtifactSHA256 == "" {
		q.Successor.ArtifactSHA256 = a.SHA256
	}
	if p.ParentHandoffSHA256 == "" {
		raw, e := optionalPrivate(handoffPath(cfg))
		if e != nil {
			return p, e
		}
		if len(raw) > 0 {
			p.ParentHandoffSHA256 = bytesDigest(raw)
		}
	}
	if p.ParentCommittedHeadSHA256 == "" {
		raw, e := optionalPrivate(committedHeadPath(cfg))
		if e != nil {
			return p, e
		}
		if len(raw) > 0 {
			p.ParentCommittedHeadSHA256 = bytesDigest(raw)
		}
	}
	return p, PlanCommittedHandoff(ctx, cfg, p)
}

func PlanCommittedHandoff(ctx context.Context, cfg Config, p CommittedHandoffPlan) error {
	normalizeConfig(&cfg)
	if err := validateHandoffStatic(cfg, committedAdapter(p)); err != nil {
		return err
	}
	if err := verifyCommittedParent(cfg, p); err != nil {
		return err
	}
	head, err := optionalPrivate(committedHeadPath(cfg))
	if err != nil {
		return err
	}
	if len(head) == 0 && p.ParentCommittedHeadSHA256 != "" || len(head) > 0 && bytesDigest(head) != p.ParentCommittedHeadSHA256 {
		return errors.New("updater: committed handoff chain changed")
	}
	state, err := readState(filepath.Join(cfg.StateDir, "state.json"))
	if err != nil {
		return err
	}
	s := &Supervisor{cfg: cfg, state: state, opts: Options{Now: time.Now, Process: LinuxProcess{}}}
	if err := s.validateCommittedMaintenance(ctx, p.Maintenance, true); err != nil {
		return err
	}
	if err := validateCommittedManager(ctx, p, true); err != nil {
		return err
	}
	id, err := (LinuxProcess{}).Inspect(ctx, p.Updater.PID)
	if err != nil {
		return err
	}
	if id != p.Updater {
		return ErrUnknown
	}
	owner, known := flockOwner(p.LockDevice, p.LockInode)
	if !known || owner != p.Updater.PID {
		return errors.New("updater: old supervisor does not own the original lock")
	}
	return retainedCommittedChild(ctx, p)
}

func readCommittedJournal(cfg Config) (CommittedHandoffJournal, error) {
	var h committedHandoffHead
	var j CommittedHandoffJournal
	raw, err := privateFile(committedHeadPath(cfg))
	if err != nil {
		return j, err
	}
	if err := strictJSON(raw, &h); err != nil {
		return j, err
	}
	if !release.ValidID(h.MaintenanceID) {
		return j, errors.New("updater: invalid committed handoff identity")
	}
	raw, err = privateFile(committedJournalPath(cfg, h.MaintenanceID))
	if err != nil {
		return j, err
	}
	if err := strictJSON(raw, &j); err != nil {
		return j, err
	}
	if (len(j.PreviousHead) == 0 && j.Plan.ParentCommittedHeadSHA256 != "") || (len(j.PreviousHead) > 0 && bytesDigest(j.PreviousHead) != j.Plan.ParentCommittedHeadSHA256) {
		return j, errors.New("updater: previous committed handoff link changed")
	}
	if j.Plan.Maintenance.MaintenanceID != h.MaintenanceID || digest(j.Plan) != h.PlanSHA256 {
		return j, errors.New("updater: committed handoff journal binding changed")
	}
	return j, nil
}

func committedInstallerLock(cfg Config) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(cfg.StateDir, "installer.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, errors.New("updater: another installer owns the window")
	}
	return f, nil
}

// ApplyCommittedHandoff changes only the updater. The old server stays alive;
// archive/host preparation and real drain authorization occur afterwards.
func ApplyCommittedHandoff(ctx context.Context, cfg Config, p CommittedHandoffPlan) (j CommittedHandoffJournal, err error) {
	return applyCommittedHandoff(ctx, cfg, p, nil)
}

func applyCommittedHandoff(ctx context.Context, cfg Config, p CommittedHandoffPlan, beforeStart func() error) (j CommittedHandoffJournal, err error) {
	normalizeConfig(&cfg)
	lock, err := committedInstallerLock(cfg)
	if err != nil {
		return j, err
	}
	defer lock.Close()
	if old, e := readCommittedJournal(cfg); e == nil && old.Plan.Maintenance.MaintenanceID == p.Maintenance.MaintenanceID {
		if digest(old.Plan) != digest(p) {
			return old, errors.New("updater: conflicting committed handoff replay")
		}
		return CommittedHandoffStatus(ctx, cfg)
	} else if e != nil && !os.IsNotExist(e) {
		return j, e
	}
	if err := PlanCommittedHandoff(ctx, cfg, p); err != nil {
		return j, err
	}
	state, err := readState(filepath.Join(cfg.StateDir, "state.json"))
	if err != nil {
		return j, err
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return j, err
	}
	previous, err := optionalPrivate(committedHeadPath(cfg))
	if err != nil {
		return j, err
	}
	dir := filepath.Dir(committedJournalPath(cfg, p.Maintenance.MaintenanceID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return j, err
	}
	if _, e := os.Lstat(committedJournalPath(cfg, p.Maintenance.MaintenanceID)); e == nil {
		return j, errors.New("updater: maintenance journal identity already exists; reconcile before publication")
	} else if !os.IsNotExist(e) {
		return j, e
	}
	j = CommittedHandoffJournal{Plan: p, OriginalState: raw, PreviousHead: previous, Phase: "planned"}
	save := func(phase string) error {
		j.Phase = phase
		j.Generation++
		return writeJSON(committedJournalPath(cfg, p.Maintenance.MaintenanceID), j)
	}
	if err := save("planned"); err != nil {
		return j, err
	}
	if err := writeJSON(committedHeadPath(cfg), committedHandoffHead{MaintenanceID: p.Maintenance.MaintenanceID, PlanSHA256: digest(p)}); err != nil {
		return j, err
	}
	return continueCommittedHandoff(ctx, cfg, j, beforeStart)
}

func continueCommittedHandoff(ctx context.Context, cfg Config, j CommittedHandoffJournal, beforeStart func() error) (out CommittedHandoffJournal, err error) {
	p := j.Plan
	save := func(phase string) error {
		j.Phase = phase
		j.Generation++
		return writeJSON(committedJournalPath(cfg, p.Maintenance.MaintenanceID), j)
	}
	defer func() {
		if err != nil {
			j.LastError = err.Error()
			_ = writeJSON(committedJournalPath(cfg, p.Maintenance.MaintenanceID), j)
		}
	}()
	if j.Phase == "planned" {
		if err := save("stopping_updater"); err != nil {
			return j, err
		}
	}
	if j.Phase == "stopping_updater" {
		old, inspectErr := (LinuxProcess{}).Inspect(ctx, p.Updater.PID)
		if inspectErr == nil && old == p.Updater {
			if err = validateCommittedManager(ctx, p, true); err != nil {
				return j, err
			}
			if _, err = (systemdUser{}).call(ctx, "stop", p.Unit); err != nil {
				return j, err
			}
		} else if inspectErr == nil || !errors.Is(inspectErr, ErrNotRunning) {
			return j, ErrUnknown
		}
	}
	if err = validateCommittedManager(ctx, p, false); err != nil {
		return j, err
	}
	if err = retainedCommittedChild(ctx, p); err != nil {
		return j, err
	}
	if _, e := (LinuxProcess{}).Inspect(ctx, p.Updater.PID); !errors.Is(e, ErrNotRunning) {
		return j, errors.New("updater: original updater exit is unknown")
	}
	owner, err := lockDir(cfg.StateDir)
	if err != nil {
		return j, err
	}
	defer owner.Close()
	if err = validateHandoffStatic(cfg, committedAdapter(p)); err != nil {
		return j, err
	}
	if err = verifyCommittedParent(cfg, p); err != nil {
		return j, err
	}
	stopped, err := readState(filepath.Join(cfg.StateDir, "state.json"))
	if err != nil {
		return j, err
	}
	if digest(stopped) != p.Maintenance.StateSHA256 {
		return j, errors.New("updater: committed snapshot changed while stopping updater")
	}
	if j.Phase != "starting_adopt" {
		if err = save("installing_updater"); err != nil {
			return j, err
		}
		if err = copyPinnedAtomic(p.Candidate, p.UpdaterBinary, 0o755, p.CandidateSHA256); err != nil {
			return j, err
		}
		if err = save("starting_adopt"); err != nil {
			return j, err
		}
	} else {
		if sum, e := hashFile(p.UpdaterBinary); e != nil || sum != p.CandidateSHA256 {
			return j, errors.New("updater: installed maintenance updater pin changed")
		}
	}
	if err = owner.Close(); err != nil {
		return j, err
	}
	if beforeStart != nil {
		if err := beforeStart(); err != nil {
			return j, err
		}
	}
	if _, err = (systemdUser{}).call(ctx, "start", p.Unit); err != nil {
		return j, err
	}
	return CommittedHandoffStatus(ctx, cfg)
}

func CommittedHandoffStatus(ctx context.Context, cfg Config) (CommittedHandoffJournal, error) {
	j, err := readCommittedJournal(cfg)
	if err != nil {
		return j, err
	}
	o := &HandoffObservation{ObservedAtMillis: time.Now().UnixMilli()}
	j.Observation = o
	o.InstalledSHA256, _ = hashFile(j.Plan.UpdaterBinary)
	m, _, err := managerSnapshot(ctx, j.Plan.Unit)
	if err != nil {
		return j, err
	}
	o.ManagerState = m["ActiveState"] + "/" + m["SubState"]
	if pid, e := strconv.Atoi(m["MainPID"]); e == nil && pid > 1 {
		o.Updater, _ = (LinuxProcess{}).Inspect(ctx, pid)
	}
	o.Child, _ = (LinuxProcess{}).Inspect(ctx, j.Plan.Maintenance.PreviousProcess.PID)
	s, err := readState(filepath.Join(cfg.StateDir, "state.json"))
	if err != nil {
		return j, err
	}
	if s.Maintenance != nil && digest(s.Maintenance.Plan) == digest(j.Plan.Maintenance) && o.Updater.SHA256 == j.Plan.CandidateSHA256 {
		j.Phase = s.Maintenance.Phase
		j.State = &s
		return j, nil
	}
	return j, ErrUnknown
}

func AdoptCommittedHandoff(ctx context.Context, s *Supervisor) (bool, error) {
	j, err := readCommittedJournal(s.cfg)
	if os.IsNotExist(err) {
		if s.state.Format == 3 {
			return true, ErrUnknown
		}
		return false, nil
	}
	if err != nil {
		return true, err
	}
	p := j.Plan
	exe, err := hashFile("/proc/self/exe")
	if err != nil {
		return true, err
	}
	if exe != p.CandidateSHA256 {
		return true, errors.New("updater: maintenance startup artifact changed")
	}
	if err := verifyCommittedParent(s.cfg, p); err != nil {
		return true, err
	}
	dev, ino, err := lockIdentity(s.cfg.StateDir)
	if err != nil {
		return true, err
	}
	if dev != p.LockDevice || ino != p.LockInode || digest(s.cfg) != p.Maintenance.ConfigSHA256 {
		return true, errors.New("updater: maintenance lock/configuration changed")
	}
	m, policy, err := managerSnapshot(ctx, p.Unit)
	if err != nil {
		return true, err
	}
	unit, err := unitDigest(ctx, p.Unit)
	if err != nil {
		return true, err
	}
	if policy != p.ManagerSHA256 || unit != p.UnitSHA256 || m["MainPID"] != strconv.Itoa(os.Getpid()) {
		return true, errors.New("updater: maintenance startup manager changed")
	}
	if err := verifyUpdaterCommand(os.Getpid(), committedAdapter(p)); err != nil {
		return true, err
	}
	if s.state.Maintenance != nil && digest(s.state.Maintenance.Plan) == digest(p.Maintenance) {
		return true, nil
	}
	if j.Phase != "starting_adopt" {
		return true, errors.New("updater: committed installer did not authorize startup")
	}
	if err := validateHandoffStatic(s.cfg, committedAdapter(p)); err != nil {
		return true, err
	}
	if err := retainedCommittedChild(ctx, p); err != nil {
		return true, err
	}
	var original State
	if err := strictJSON(j.OriginalState, &original); err != nil {
		return true, err
	}
	current := cloneState(s.state)
	current.Generation = original.Generation
	current.UpdaterEpoch = original.UpdaterEpoch
	current.UpdaterBootID = original.UpdaterBootID
	if digest(original) != p.Maintenance.StateSHA256 || digest(current) != digest(original) {
		return true, errors.New("updater: committed adoption state drifted")
	}
	return true, s.AdoptCommittedMaintenance(ctx, p.Maintenance)
}

// ResumeCommittedHandoff is a separate, revisioned administrative decision.
// Replaying a continuation returns observations and never repeats its effects.
func ResumeCommittedHandoff(ctx context.Context, cfg Config, p CommittedHandoffPlan, request string, generation uint64) (CommittedHandoffJournal, error) {
	normalizeConfig(&cfg)
	lock, err := committedInstallerLock(cfg)
	if err != nil {
		return CommittedHandoffJournal{}, err
	}
	defer lock.Close()
	j, err := readCommittedJournal(cfg)
	if err != nil {
		return j, err
	}
	if digest(j.Plan) != digest(p) || !protocol.ValidID(protocol.PrefixRequest, request) || request == p.Maintenance.RequestID || request == p.Maintenance.LinkRequestID || request == p.Maintenance.PreparationRequestID || request == p.Maintenance.PreparationCancelRequestID {
		return j, errors.New("updater: invalid maintenance continuation identity")
	}
	if _, seen := j.Continuations[request]; seen {
		return CommittedHandoffStatus(ctx, cfg)
	}
	if generation != j.Generation {
		return j, errors.New("updater: stale maintenance installer generation")
	}
	if j.Phase != "planned" && j.Phase != "stopping_updater" && j.Phase != "installing_updater" && j.Phase != "starting_adopt" {
		return j, errors.New("updater: maintenance installer phase cannot continue")
	}
	if err := validateHandoffStatic(cfg, committedAdapter(p)); err != nil {
		return j, err
	}
	if err := verifyCommittedParent(cfg, p); err != nil {
		return j, err
	}
	if err := retainedCommittedChild(ctx, p); err != nil {
		return j, err
	}
	state, err := readState(filepath.Join(cfg.StateDir, "state.json"))
	if err != nil {
		return j, err
	}
	if state.Maintenance != nil && digest(state.Maintenance.Plan) == digest(p.Maintenance) {
		return CommittedHandoffStatus(ctx, cfg)
	}
	if digest(state) != p.Maintenance.StateSHA256 {
		return j, errors.New("updater: maintenance original snapshot changed")
	}
	if j.Continuations == nil {
		j.Continuations = map[string]uint64{}
	}
	j.Continuations[request] = generation
	j.Generation++
	if err := writeJSON(committedJournalPath(cfg, p.Maintenance.MaintenanceID), j); err != nil {
		return j, err
	}
	return continueCommittedHandoff(ctx, cfg, j, nil)
}

func retainedCommittedChild(ctx context.Context, p CommittedHandoffPlan) error {
	if !p.Maintenance.FailedProcessExited {
		return retainedChild(ctx, committedAdapter(p))
	}
	if p.Maintenance.ParentMaintenanceID == "" {
		return errors.New("updater: exited writer is limited to failed maintenance")
	}
	s := &Supervisor{opts: Options{Process: LinuxProcess{}}}
	return s.maintenanceSourceGone(ctx, p.Maintenance.PreviousProcess)
}
func validateCommittedManager(ctx context.Context, p CommittedHandoffPlan, active bool) error {
	adapter := committedAdapter(p)
	if p.Maintenance.FailedProcessExited {
		// A gone failed writer has no current cgroup. Validate the actual manager
		// process with the existing guard, then separately prove that writer gone.
		adapter.Recovery.OriginalProcess = p.Updater
	}
	if err := validateManager(ctx, adapter, active); err != nil {
		return err
	}
	if p.Maintenance.FailedProcessExited {
		return retainedCommittedChild(ctx, p)
	}
	return nil
}
