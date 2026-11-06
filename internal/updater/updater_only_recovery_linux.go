//go:build linux

package updater

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
)

// This local installer journal has its own version. It does not extend the
// updater state or the server/runner protocol. The original handoff stays exact.
type updaterOnlyScope struct {
	Authorization         protocol.BoundedMaintenanceAuthorization `json:"authorization"`
	ConfigSHA256          string                                   `json:"config_sha256"`
	OriginalHeadSHA256    string                                   `json:"original_head_sha256"`
	OriginalJournalSHA256 string                                   `json:"original_journal_sha256"`
	OriginalState         State                                    `json:"original_state"`
	Retained              Identity                                 `json:"retained"`
}

type updaterOnlyJournal struct {
	Format         int              `json:"format"`
	Scope          updaterOnlyScope `json:"scope"`
	Phase          string           `json:"phase"`
	AdoptedUpdater Identity         `json:"adopted_updater,omitempty"`
}

type updaterOnlyHead struct {
	Format      int    `json:"format"`
	RequestID   string `json:"request_id"`
	ScopeSHA256 string `json:"scope_sha256"`
}

func updaterOnlyHeadPath(cfg Config) string {
	return filepath.Join(cfg.StateDir, "updater-only-handoff.json")
}
func updaterOnlyJournalPath(cfg Config, id string) string {
	return filepath.Join(cfg.StateDir, "updater-only-handoffs", id+".json")
}

func readUpdaterOnlyJournal(cfg Config, id string) (updaterOnlyJournal, error) {
	var j updaterOnlyJournal
	if !protocol.ValidID(protocol.PrefixRequest, id) {
		return j, errors.New("updater: invalid updater-only request")
	}
	raw, err := privateFile(updaterOnlyJournalPath(cfg, id))
	if err != nil {
		return j, err
	}
	if err := strictJSON(raw, &j); err != nil {
		return j, err
	}
	if j.Format != 1 || j.Scope.Authorization.RequestID != id {
		return j, errors.New("updater: unsupported or foreign updater-only journal")
	}
	return j, nil
}

func updaterOnlyOriginalFiles(cfg Config, p CommittedHandoffPlan) (string, string, error) {
	if err := verifyCommittedParent(cfg, p); err != nil {
		return "", "", err
	}
	head, err := privateFile(committedHeadPath(cfg))
	if err != nil {
		return "", "", err
	}
	journal, err := privateFile(committedJournalPath(cfg, p.Maintenance.MaintenanceID))
	if err != nil {
		return "", "", err
	}
	j, err := readCommittedJournal(cfg)
	if err != nil {
		return "", "", err
	}
	if digest(j.Plan) != digest(p) {
		return "", "", errors.New("updater: updater-only original handoff changed")
	}
	return bytesDigest(head), bytesDigest(journal), nil
}

func validateUpdaterOnlyTarget(p CommittedHandoffPlan, a protocol.BoundedMaintenanceAuthorization) error {
	if a.UpdaterTarget == nil {
		return errors.New("updater: updater-only target required")
	}
	actual := *a.UpdaterTarget
	actual.Candidate, actual.CandidateSHA256 = p.Candidate, p.CandidateSHA256
	original := protocol.BoundedUpdaterTarget{Unit: p.Unit, UnitSHA256: p.UnitSHA256, ManagerSHA256: p.ManagerSHA256, UpdaterBinary: p.UpdaterBinary, Candidate: p.Candidate, CandidateSHA256: p.CandidateSHA256, ConfigFile: p.ConfigFile, ConfigFileSHA256: p.ConfigFileSHA256}
	if actual != original || !release.ValidSHA(a.UpdaterTarget.CandidateSHA256) || a.UpdaterTarget.CandidateSHA256 == p.CandidateSHA256 {
		return errors.New("updater: updater-only authority changed fixed manager or omitted a distinct candidate")
	}
	return nil
}

func validateUpdaterOnlyState(st State, p CommittedHandoffPlan, a protocol.BoundedMaintenanceAuthorization) error {
	m := st.Maintenance
	if st.Format != 4 || m == nil || digest(m.Plan) != digest(p.Maintenance) || m.Phase != a.ExpectedPhase || a.PlanSHA256 != digest(p.Maintenance) || a.Source != p.Maintenance.PreviousProcess || a.Target != p.Maintenance.Successor {
		return errors.New("updater: updater-only original maintenance scope changed")
	}
	if m.Phase != "ledger_linked" && m.Phase != "completed" {
		return errors.New("updater: updater-only recovery requires a linked committed service")
	}
	if !m.Irreversible || m.HealthCount < 2 || !validIdentity(st.Process) || st.Process.LaunchID != p.Maintenance.Successor.ProcessInstanceID || st.Process.SHA256 != p.Maintenance.Successor.ArtifactSHA256 || st.Process != m.Process || st.Current.ReleaseID != p.Maintenance.Successor.ReleaseID || st.Current.ManifestSHA256 != p.Maintenance.Successor.ManifestSHA256 || st.Current.Artifact.SHA256 != st.Process.SHA256 || digest(st.Current) != digest(m.Target) || st.CommittedProof == nil || st.CommittedProof.Process != st.Process || digest(st.CommittedProof.Version) != digest(st.Current) || st.CommittedProof.Binding != p.Maintenance.Successor || m.Receipt == nil || m.Receipt.Process != st.Process || m.Receipt.Successor != p.Maintenance.Successor || m.Receipt.MaintenanceID != p.Maintenance.MaintenanceID || m.Receipt.RequestID != p.Maintenance.RequestID || m.Receipt.Previous != p.Maintenance.Previous || m.Receipt.Generation == 0 || m.Receipt.Generation > st.Generation || m.Receipt.Epoch == "" || m.Receipt.Epoch != m.Epoch || m.Receipt.FirstSequence == 0 || m.Receipt.FirstSequence != m.FirstSequence || m.Receipt.SecondSequence <= m.Receipt.FirstSequence || m.Receipt.SecondSequence != m.LastSequence {
		return errors.New("updater: updater-only retained service lacks exact committed proof")
	}
	if st.Bounded == nil || st.Bounded.SourceExit == nil || st.Bounded.SourceExit.Process != p.Maintenance.PreviousProcess || st.Bounded.SourceExit.ConfirmedAtMillis <= 0 {
		return errors.New("updater: updater-only original predecessor exit is unproven")
	}
	return nil
}

func verifyUpdaterOnlyRetained(ctx context.Context, id Identity) error {
	actual, err := (LinuxProcess{}).Inspect(ctx, id.PID)
	if err != nil || actual != id {
		return errors.New("updater: updater-only retained service identity changed")
	}
	matches, err := (LinuxProcess{}).Find(ctx, id.LaunchID, id.SHA256)
	if err != nil || len(matches) != 1 || matches[0] != id {
		return errors.New("updater: updater-only retained service is not unique")
	}
	return nil
}

func updaterOnlyLock(cfg Config, lock *os.File, a protocol.BoundedMaintenanceAuthorization) error {
	if a.UpdaterLock == nil {
		return errors.New("updater: updater-only lock authority missing")
	}
	dev, ino, err := lockIdentity(cfg.StateDir)
	if err != nil {
		return err
	}
	if dev != a.UpdaterLock.Device || ino != a.UpdaterLock.Inode {
		return errors.New("updater: updater-only ownership inode changed")
	}
	if lock != nil {
		var stat syscall.Stat_t
		if err := syscall.Fstat(int(lock.Fd()), &stat); err != nil {
			return err
		}
		if uint64(stat.Dev) != dev || stat.Ino != ino {
			return errors.New("updater: updater-only held ownership differs from its path")
		}
	}
	return nil
}

// A new CAS may recover a definitely pre-start interruption. A recorded start
// intent is never converted into permission to issue another native Start.
func updaterOnlyManagerIdle(manager map[string]string) bool {
	job, known := manager["Job"]
	return known && (job == "0" || job == "") && manager["MainPID"] == "0" && manager["ActiveState"] == "inactive" && manager["SubState"] == "dead"
}

func validateUpdaterOnlyPreStartRecovery(cfg Config, p CommittedHandoffPlan, a protocol.BoundedMaintenanceAuthorization, before State, manager map[string]string) error {
	if !updaterOnlyManagerIdle(manager) {
		return errors.New("updater: updater-only prior controller exit has pending manager work")
	}
	raw, err := privateFile(updaterOnlyHeadPath(cfg))
	if err != nil {
		return err
	}
	var head updaterOnlyHead
	if err := strictJSON(raw, &head); err != nil {
		return err
	}
	j, err := readUpdaterOnlyJournal(cfg, head.RequestID)
	if err != nil {
		return err
	}
	old := j.Scope.Authorization
	if head.Format != 1 || head.ScopeSHA256 != digest(j.Scope) || head.RequestID == a.RequestID || j.Phase != "planned" && j.Phase != "stopping_updater" && j.Phase != "installing_updater" || j.Scope.ConfigSHA256 != digest(cfg) || old.UpdaterSource == nil || *old.UpdaterSource != *a.UpdaterSource || old.PlanSHA256 != a.PlanSHA256 || old.Target != a.Target || old.Source != a.Source || j.Scope.Retained != before.Process || digest(j.Scope.OriginalState) != digest(before) {
		return errors.New("updater: updater-only prior attempt is not a definite pre-start interruption")
	}
	h, journal, err := updaterOnlyOriginalFiles(cfg, p)
	if err != nil || h != j.Scope.OriginalHeadSHA256 || journal != j.Scope.OriginalJournalSHA256 {
		return errors.New("updater: updater-only pre-start handoff history changed")
	}
	return nil
}

func validateUpdaterOnlyFreshRequest(st State, id string) error {
	usedPlan := func(p protocol.CommittedMaintenancePlan) bool {
		if id == p.RequestID || id == p.LinkRequestID || id == p.PreparationRequestID || id == p.PreparationCancelRequestID {
			return true
		}
		for _, host := range p.HostControls {
			if id == host.RequestID || id == host.RestoreRequestID {
				return true
			}
		}
		return false
	}
	usedWindow := func(b *protocol.BoundedMaintenanceState) bool {
		if b == nil {
			return false
		}
		if id == b.Authorization.RequestID || id == b.RecoveryAuthorizationID {
			return true
		}
		_, exists := b.Authorizations[id]
		return exists
	}
	if st.Maintenance != nil && usedPlan(st.Maintenance.Plan) || usedWindow(st.Bounded) {
		return errors.New("updater: updater-only replacement needs an independent request ID")
	}
	for _, m := range st.MaintenanceHistory {
		if usedPlan(m.Plan) {
			return errors.New("updater: updater-only request belongs to maintenance history")
		}
	}
	for key, b := range st.BoundedHistory {
		if key == id || usedWindow(&b) {
			return errors.New("updater: updater-only request belongs to bounded history")
		}
	}
	return nil
}

func takeoverCommittedUpdaterOnly(ctx context.Context, cfg Config, p CommittedHandoffPlan, a protocol.BoundedMaintenanceAuthorization) (any, error) {
	if a.UpdaterSource == nil || !validIdentity(*a.UpdaterSource) || a.UpdaterLock == nil || !protocol.ValidID(protocol.PrefixRequest, a.RequestID) || a.Role != "server" || cfg.Role != "server" || a.Target.InstanceID != cfg.InstanceID || a.Reason == "" {
		return nil, errors.New("updater: updater-only recovery requires exact local authority")
	}
	if err := validateUpdaterOnlyTarget(p, a); err != nil {
		return nil, err
	}
	install, err := committedInstallerLock(cfg)
	if err != nil {
		return nil, err
	}
	defer install.Close()
	// Once a native attempt is recorded, replay only observes that attempt.
	// MainPID=0 cannot establish whether a previous systemctl start was queued.
	if old, e := readUpdaterOnlyJournal(cfg, a.RequestID); e == nil {
		if digest(old.Scope.Authorization) != digest(a) || old.Scope.ConfigSHA256 != digest(cfg) {
			return old, errors.New("updater: updater-only original request changed")
		}
		return old, nil
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	before, err := readState(filepath.Join(cfg.StateDir, "state.json"))
	if err != nil {
		return nil, err
	}
	if before.Generation != a.ExpectedGeneration {
		return before, errors.New("updater: updater-only generation changed")
	}
	if err := validateUpdaterOnlyState(before, p, a); err != nil {
		return before, err
	}
	if err := validateUpdaterOnlyFreshRequest(before, a.RequestID); err != nil {
		return before, err
	}
	window, err := beginBoundedInstaller(cfg, a, "updater_only_takeover")
	if err != nil {
		return before, err
	}
	phaseCtx, cancel, err := boundedInstallerContext(ctx, window)
	if err != nil {
		return before, err
	}
	defer cancel()
	ctx = phaseCtx
	if err := validateBoundedServerData(cfg, a.DataIdentity); err != nil {
		return before, err
	}
	if err := validateBoundedUpdaterTarget(ctx, cfg, *a.UpdaterTarget); err != nil {
		return before, err
	}
	if err := updaterOnlyLock(cfg, nil, a); err != nil {
		return before, err
	}
	if err := verifyUpdaterOnlyRetained(ctx, before.Process); err != nil {
		return before, err
	}
	actual, inspectErr := (LinuxProcess{}).Inspect(ctx, a.UpdaterSource.PID)
	preStartRecovery := errors.Is(inspectErr, ErrNotRunning)
	if inspectErr != nil && !preStartRecovery || inspectErr == nil && actual != *a.UpdaterSource {
		return before, errors.New("updater: updater-only source controller identity changed")
	}
	manager, _, err := managerSnapshot(ctx, p.Unit)
	if err != nil {
		return before, err
	}
	var preheld *os.File
	if preStartRecovery {
		actual = *a.UpdaterSource
		if err := validateUpdaterOnlyPreStartRecovery(cfg, p, a, before, manager); err != nil {
			return before, err
		}
		preheld, err = lockDir(cfg.StateDir)
		if err != nil {
			return before, err
		}
		defer preheld.Close()
		if err := updaterOnlyLock(cfg, preheld, a); err != nil {
			return before, err
		}
	} else {
		if err := verifyUpdaterCommand(actual.PID, HandoffPlan{UpdaterBinary: p.UpdaterBinary, ConfigFile: p.ConfigFile}); err != nil {
			return before, err
		}
		owns, err := processOwnsFlock(ctx, "/proc", actual.PID, a.UpdaterLock.Device, a.UpdaterLock.Inode)
		if err != nil || !owns {
			return before, errors.New("updater: updater-only source does not own original flock")
		}
		if manager["MainPID"] != strconv.Itoa(actual.PID) {
			return before, errors.New("updater: updater-only manager source changed")
		}
	}
	headSHA, journalSHA, err := updaterOnlyOriginalFiles(cfg, p)
	if err != nil {
		return before, err
	}
	j := updaterOnlyJournal{Format: 1, Scope: updaterOnlyScope{Authorization: a, ConfigSHA256: digest(cfg), OriginalHeadSHA256: headSHA, OriginalJournalSHA256: journalSHA, OriginalState: before, Retained: before.Process}, Phase: "planned"}
	path := updaterOnlyJournalPath(cfg, a.RequestID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return before, err
	}
	if err := writeJSON(path, j); err != nil {
		return before, err
	}
	if err := writeJSON(updaterOnlyHeadPath(cfg), updaterOnlyHead{Format: 1, RequestID: a.RequestID, ScopeSHA256: digest(j.Scope)}); err != nil {
		return j, err
	}
	// Recheck the real child and fixed manager at the destructive boundary.
	if err := verifyUpdaterOnlyRetained(ctx, j.Scope.Retained); err != nil {
		return j, err
	}
	if err := validateBoundedUpdaterTarget(ctx, cfg, *a.UpdaterTarget); err != nil {
		return j, err
	}
	latest, err := readState(filepath.Join(cfg.StateDir, "state.json"))
	if err != nil || digest(latest) != digest(before) {
		return j, errors.New("updater: updater-only generation changed before stop")
	}
	if err := updaterOnlyLock(cfg, nil, a); err != nil {
		return j, err
	}
	var owner *os.File
	if preStartRecovery {
		if _, err := (LinuxProcess{}).Inspect(ctx, actual.PID); !errors.Is(err, ErrNotRunning) {
			return j, errors.New("updater: updater-only exited source changed")
		}
		manager, _, err = managerSnapshot(ctx, p.Unit)
		if err != nil || !updaterOnlyManagerIdle(manager) {
			return j, errors.New("updater: updater-only pre-start manager changed")
		}
		owner = preheld
	} else {
		currentUpdater, err := (LinuxProcess{}).Inspect(ctx, actual.PID)
		if err != nil || currentUpdater != actual {
			return j, errors.New("updater: updater-only source changed before stop")
		}
		owns, err := processOwnsFlock(ctx, "/proc", actual.PID, a.UpdaterLock.Device, a.UpdaterLock.Inode)
		if err != nil || !owns {
			return j, errors.New("updater: updater-only source lost ownership before stop")
		}
		manager, _, err = managerSnapshot(ctx, p.Unit)
		if err != nil || manager["MainPID"] != strconv.Itoa(actual.PID) {
			return j, errors.New("updater: updater-only manager changed before stop")
		}
		j.Phase = "stopping_updater"
		if err := writeJSON(path, j); err != nil {
			return j, err
		}
		killCtx, killCancel := context.WithTimeout(ctx, time.Duration(a.Timeouts.KillMillis)*time.Millisecond)
		_, err = (systemdUser{}).call(killCtx, "--no-block", "stop", p.Unit)
		if err == nil {
			err = (LinuxProcess{}).Crash(killCtx, actual, time.Duration(a.Timeouts.KillMillis)*time.Millisecond)
		}
		killCancel()
		if err != nil {
			return j, err
		}
		lockCtx, lockCancel := context.WithTimeout(ctx, time.Duration(a.Timeouts.KillMillis)*time.Millisecond)
		owner, err = boundedAcquireOwnership(lockCtx, cfg, actual)
		lockCancel()
		if err != nil {
			return j, err
		}
	}
	defer owner.Close()
	if _, err := (LinuxProcess{}).Inspect(ctx, actual.PID); !errors.Is(err, ErrNotRunning) {
		return j, errors.New("updater: updater-only source controller exit is unconfirmed")
	}
	if err := updaterOnlyLock(cfg, owner, a); err != nil {
		return j, err
	}
	stopped, err := readState(filepath.Join(cfg.StateDir, "state.json"))
	if err != nil {
		return j, err
	}
	if digest(stopped) != digest(before) {
		return j, errors.New("updater: updater-only durable source changed; fresh CAS required")
	}
	if err := verifyUpdaterOnlyRetained(ctx, j.Scope.Retained); err != nil {
		return j, err
	}
	if err := validateBoundedServerData(cfg, a.DataIdentity); err != nil {
		return j, err
	}
	if err := validateBoundedUpdaterTarget(ctx, cfg, *a.UpdaterTarget); err != nil {
		return j, err
	}
	j.Phase = "installing_updater"
	if err := writeJSON(path, j); err != nil {
		return j, err
	}
	if err := copyPinnedAtomic(a.UpdaterTarget.Candidate, p.UpdaterBinary, 0o755, a.UpdaterTarget.CandidateSHA256); err != nil {
		return j, err
	}
	if err := ctx.Err(); err != nil {
		return j, err
	}
	j.Phase = "starting_updater"
	if err := writeJSON(path, j); err != nil {
		return j, err
	}
	if err := verifyUpdaterOnlyRetained(ctx, j.Scope.Retained); err != nil {
		return j, err
	}
	if err := owner.Close(); err != nil {
		return j, err
	}
	if _, err := (systemdUser{}).call(ctx, "start", p.Unit); err != nil {
		return j, fmt.Errorf("updater: updater-only start response unconfirmed; reconcile original request: %w", err)
	}
	return readUpdaterOnlyJournal(cfg, a.RequestID)
}

// adoptUpdaterOnlyHandoff is reachable only when the current immutable handoff
// pins different controller bytes. Initial adoption proves the retained writer;
// an adopted receipt subsequently permits ordinary supervision to resume.
func adoptUpdaterOnlyHandoff(ctx context.Context, s *Supervisor, p CommittedHandoffPlan, exe string) error {
	raw, err := privateFile(updaterOnlyHeadPath(s.cfg))
	if err != nil {
		return errors.New("updater: maintenance startup artifact has no updater-only authority")
	}
	var head updaterOnlyHead
	if err := strictJSON(raw, &head); err != nil {
		return err
	}
	if head.Format != 1 {
		return errors.New("updater: unsupported updater-only head")
	}
	j, err := readUpdaterOnlyJournal(s.cfg, head.RequestID)
	if err != nil {
		return err
	}
	a := j.Scope.Authorization
	if digest(j.Scope) != head.ScopeSHA256 || j.Scope.ConfigSHA256 != digest(s.cfg) || a.UpdaterTarget == nil || a.UpdaterTarget.CandidateSHA256 != exe {
		return errors.New("updater: updater-only startup scope or artifact changed")
	}
	if err := validateUpdaterOnlyTarget(p, a); err != nil {
		return err
	}
	if j.Phase != "adopted" {
		if j.Phase != "starting_updater" {
			return errors.New("updater: updater-only installer did not authorize startup")
		}
		windowRaw, err := privateFile(boundedInstallerPath(s.cfg, a.RequestID))
		if err != nil {
			return err
		}
		var window boundedInstallerJournal
		if err := strictJSON(windowRaw, &window); err != nil {
			return err
		}
		if window.Action != "updater_only_takeover" || window.ConfigSHA256 != digest(s.cfg) || digest(window.Window.Authorization) != digest(a) {
			return errors.New("updater: updater-only original window changed")
		}
		startupCtx, cancel, err := boundedInstallerContext(ctx, window)
		if err != nil {
			return err
		}
		defer cancel()
		ctx = startupCtx
	}
	h, journal, err := updaterOnlyOriginalFiles(s.cfg, p)
	if err != nil || h != j.Scope.OriginalHeadSHA256 || journal != j.Scope.OriginalJournalSHA256 {
		return errors.New("updater: updater-only original handoff history changed")
	}
	if err := validateUpdaterOnlyState(j.Scope.OriginalState, p, a); err != nil {
		return err
	}
	if err := updaterOnlyLock(s.cfg, s.lock, a); err != nil {
		return err
	}
	startupTarget := *a.UpdaterTarget
	startupTarget.Candidate = startupTarget.UpdaterBinary
	if err := validateBoundedUpdaterTarget(ctx, s.cfg, startupTarget); err != nil {
		return err
	}
	manager, _, err := managerSnapshot(ctx, p.Unit)
	if err != nil || manager["MainPID"] != strconv.Itoa(os.Getpid()) {
		return errors.New("updater: updater-only startup manager changed")
	}
	if err := verifyUpdaterCommand(os.Getpid(), HandoffPlan{UpdaterBinary: p.UpdaterBinary, ConfigFile: p.ConfigFile}); err != nil {
		return err
	}
	owns, err := processOwnsFlock(ctx, "/proc", os.Getpid(), a.UpdaterLock.Device, a.UpdaterLock.Inode)
	if err != nil || !owns {
		return errors.New("updater: updater-only startup does not own the original flock")
	}
	self, err := (LinuxProcess{}).Inspect(ctx, os.Getpid())
	if err != nil || self.SHA256 != exe {
		return errors.New("updater: updater-only startup kernel identity changed")
	}
	if s.state.Maintenance == nil || digest(s.state.Maintenance.Plan) != digest(p.Maintenance) {
		return errors.New("updater: updater-only startup current maintenance changed")
	}
	if j.Phase == "adopted" {
		if !validIdentity(j.AdoptedUpdater) || j.AdoptedUpdater.SHA256 != exe {
			return errors.New("updater: updater-only adopted receipt is incomplete")
		}
		return nil
	}
	current := cloneState(s.state)
	current.Generation = j.Scope.OriginalState.Generation
	current.UpdaterEpoch = j.Scope.OriginalState.UpdaterEpoch
	current.UpdaterBootID = j.Scope.OriginalState.UpdaterBootID
	if digest(current) != digest(j.Scope.OriginalState) {
		return errors.New("updater: updater-only startup source state changed")
	}
	if err := verifyUpdaterOnlyRetained(ctx, j.Scope.Retained); err != nil {
		return err
	}
	if s.state.Process != j.Scope.Retained {
		return errors.New("updater: updater-only retained process changed in state")
	}
	if err := validateBoundedServerData(s.cfg, a.DataIdentity); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	j.Phase, j.AdoptedUpdater = "adopted", self
	return writeJSON(updaterOnlyJournalPath(s.cfg, a.RequestID), j)
}
