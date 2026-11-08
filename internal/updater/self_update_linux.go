//go:build linux

package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
)

func selfCandidate(request SelfUpdateRequest) (string, string, error) {
	if !protocol.ValidID(protocol.PrefixRequest, request.Target.RequestID) || !release.ValidID(request.UpgradeID) || !filepath.IsAbs(request.Release) || !release.ValidSHA(request.ReleaseSetSHA256) {
		return "", "", errors.New("updater: invalid fixed self-update request")
	}
	b, err := release.Load(request.Release)
	if err != nil {
		return "", "", err
	}
	if b.Set == nil || b.Set.Format != 2 || b.SetSHA256 != request.ReleaseSetSHA256 {
		return "", "", errors.New("updater: supervisor-first release set required")
	}
	a, err := b.SetArtifact("updater", runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", "", err
	}
	path, err := release.RegularPath(b.Dir, a.Path)
	if err != nil {
		return "", "", err
	}
	source, err := release.ExecutableSource(path)
	if err != nil || source.Modified || source.Commit != b.Set.Commit {
		return "", "", errors.New("updater: candidate source identity differs")
	}
	return path, a.SHA256, nil
}

func selfPlanStatic(cfg Config, p SelfUpdatePlan, now time.Time, checkExpiry bool) error {
	legacy, committed, err := selfOtherOwners(cfg)
	if err != nil {
		return err
	}
	if legacy != p.HandoffSHA256 || committed != p.CommittedHeadSHA256 {
		return errors.New("updater: another installer changed startup ownership")
	}
	if checkExpiry && now.UnixMilli() >= p.Request.Target.ExpiresAtMillis {
		return errors.New("updater: self-update authority expired")
	}
	if cfg.Role != "server" && cfg.Role != "runner" || !filepath.IsAbs(p.ConfigFile) || !filepath.IsAbs(p.UpdaterBinary) || !strings.HasSuffix(p.Unit, ".service") || strings.ContainsAny(p.Unit, "/ \t\n") || strings.HasPrefix(p.Unit, "-") || !validIdentity(p.Child) || p.Updater.PID <= 1 || p.Updater.StartTicks == 0 || !release.ValidSHA(p.Updater.SHA256) || !release.ValidSHA(p.ConfigSHA256) || !release.ValidSHA(p.ManagerSHA256) || !release.ValidSHA(p.UnitSHA256) || p.LockInode == 0 || p.LockDevice == 0 {
		return errors.New("updater: incomplete self-update plan")
	}
	raw, err := privateFile(p.ConfigFile)
	if err != nil {
		return err
	}
	var fixed Config
	if err = strictJSON(raw, &fixed); err != nil {
		return err
	}
	normalizeConfig(&fixed)
	normalizeConfig(&cfg)
	if bytesDigest(raw) != p.ConfigSHA256 || !reflect.DeepEqual(fixed, cfg) {
		return errors.New("updater: fixed configuration changed")
	}
	candidate, sha, err := selfCandidate(p.Request)
	if err != nil {
		return err
	}
	if candidate != p.Candidate || sha != p.CandidateSHA256 {
		return errors.New("updater: self-update candidate changed")
	}
	dev, ino, err := lockIdentity(cfg.StateDir)
	if err != nil {
		return err
	}
	if dev != p.LockDevice || ino != p.LockInode {
		return errors.New("updater: original supervision lock replaced")
	}
	return nil
}

func selfChild(ctx context.Context, p SelfUpdatePlan) error {
	id, err := (LinuxProcess{}).Inspect(ctx, p.Child.PID)
	if err != nil {
		return err
	}
	if id != p.Child {
		return ErrUnknown
	}
	return nil
}

func selfManager(ctx context.Context, p SelfUpdatePlan, active, outside bool) error {
	m, policy, err := managerSnapshot(ctx, p.Unit)
	if err != nil {
		return err
	}
	unit, err := unitDigest(ctx, p.Unit)
	if err != nil {
		return err
	}
	if unit != p.UnitSHA256 || policy != p.ManagerSHA256 || m["Id"] != p.Unit || m["LoadState"] != "loaded" || m["KillMode"] != "process" || m["KillSignal"] != "15" || m["FinalKillSignal"] != "9" || m["SendSIGKILL"] != "yes" || m["Restart"] != "on-failure" || m["Type"] != "simple" || m["RemainAfterExit"] != "no" || m["RefuseManualStop"] != "no" || m["RefuseManualStart"] != "no" || !strings.Contains(m["ExecStart"], "path="+p.UpdaterBinary+" ;") {
		return errors.New("updater: unsupported or changed systemd-user policy")
	}
	if m["Job"] != "" && m["Job"] != "0" && m["Job"] != "0 /" {
		return ErrUnknown
	}
	if outside {
		raw, e := os.ReadFile("/proc/self/cgroup")
		if e != nil {
			return e
		}
		if m["ControlGroup"] != "" && strings.Contains(string(raw), ":"+m["ControlGroup"]+"\n") {
			return errors.New("updater: installer must run outside the supervised unit")
		}
	}
	if !active {
		if m["MainPID"] != "0" || m["ActiveState"] != "inactive" || m["SubState"] != "dead" {
			return ErrUnknown
		}
		observed, e := (LinuxProcess{}).Observe(ctx, p.Updater.PID)
		if !errors.Is(e, ErrNotRunning) && (e != nil || observed.Identity.StartTicks == p.Updater.StartTicks) {
			return ErrUnknown
		}
		return nil
	}
	if m["MainPID"] != strconv.Itoa(p.Updater.PID) || m["ActiveState"] != "active" || m["SubState"] != "running" || m["ControlGroup"] == "" {
		return ErrUnknown
	}
	actual, err := (LinuxProcess{}).Inspect(ctx, p.Updater.PID)
	if err != nil {
		return err
	}
	if actual != p.Updater {
		return ErrUnknown
	}
	if err = verifyUpdaterCommand(p.Updater.PID, HandoffPlan{UpdaterBinary: p.UpdaterBinary, ConfigFile: p.ConfigFile}); err != nil {
		return err
	}
	for _, pid := range []int{p.Updater.PID, p.Child.PID} {
		raw, e := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cgroup"))
		if e != nil {
			return e
		}
		if !strings.Contains(string(raw), ":"+m["ControlGroup"]+"\n") {
			return errors.New("updater: supervised process outside the fixed unit")
		}
	}
	return nil
}

func selfExitEvidence(cfg Config, p SelfUpdatePlan) error {
	// Only a durable journal prepared by the exact currently locked modern
	// supervisor can attest its own child-retaining shutdown implementation.
	if p.PreparedBy == p.Updater {
		j, err := readSelf(cfg)
		if err != nil {
			return err
		}
		owner, known := flockOwner(p.LockDevice, p.LockInode)
		if digest(j.Plan) != digest(p) || !known || owner != p.Updater.PID {
			return ErrUnknown
		}
		return nil
	}
	raw, err := privateFile(p.ExitEvidence)
	if err != nil {
		return err
	}
	var e UpdaterExitEvidence
	if err = strictJSON(raw, &e); err != nil {
		return err
	}
	if bytesDigest(raw) != p.ExitEvidenceSHA256 || e.ArtifactSHA256 != p.Updater.SHA256 || e.OperatorUID != os.Getuid() || e.ExitPolicy != "retains-managed-child" || e.StateWritePolicy != "atomic-intent-before-effects" || e.Source == "" || e.ReviewArtifact == "" || e.ExpiresAtMillis < p.Request.Target.ExpiresAtMillis {
		return errors.New("updater: legacy shutdown behavior needs artifact-bound reviewed evidence")
	}
	return nil
}

func discoverSelfUnit(pid int) (string, error) {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return "", err
	}
	return selfUnitFromCgroup(string(raw))
}
func selfUnitFromCgroup(raw string) (string, error) {
	for _, line := range strings.Split(raw, "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 || !(parts[1] == "name=systemd" || parts[0] == "0" && parts[1] == "") {
			continue
		}
		segments := strings.Split(parts[2], "/")
		for i := len(segments) - 1; i >= 0; i-- {
			if strings.HasSuffix(segments[i], ".service") {
				return segments[i], nil
			}
		}
	}
	return "", errors.New("updater: fixed systemd-user service could not be identified")
}

// PrepareSelfUpdatePlan is read-only. Legacy bootstrap supplies a unit and
// reviewed shutdown evidence; modern preparation runs inside the locked owner.
func PrepareSelfUpdatePlan(ctx context.Context, cfg Config, p SelfUpdatePlan) (SelfUpdatePlan, error) {
	// Standalone callers cannot self-assert modern shutdown capability.
	p.PreparedBy = Identity{}
	return prepareSelf(ctx, cfg, p, false)
}
func prepareSelf(ctx context.Context, cfg Config, p SelfUpdatePlan, modern bool) (SelfUpdatePlan, error) {
	raw, err := privateFile(p.ConfigFile)
	if err != nil {
		return p, err
	}
	p.ConfigSHA256 = bytesDigest(raw)
	if p.Unit == "" {
		p.Unit, err = discoverSelfUnit(os.Getpid())
		if err != nil {
			return p, err
		}
	}
	m, policy, err := managerSnapshot(ctx, p.Unit)
	if err != nil {
		return p, err
	}
	p.ManagerSHA256 = policy
	pid, err := strconv.Atoi(m["MainPID"])
	if err != nil || pid <= 1 {
		return p, ErrUnknown
	}
	p.Updater, err = (LinuxProcess{}).Inspect(ctx, pid)
	if err != nil {
		return p, err
	}
	if modern && pid != os.Getpid() {
		return p, ErrUnknown
	}
	if p.UpdaterBinary == "" {
		_, tail, ok := strings.Cut(m["ExecStart"], "path=")
		fixed, _, found := strings.Cut(tail, " ;")
		if !ok || !found || !filepath.IsAbs(fixed) {
			return p, errors.New("updater: fixed ExecStart executable unavailable")
		}
		p.UpdaterBinary = fixed
	}
	p.UnitSHA256, err = unitDigest(ctx, p.Unit)
	if err != nil {
		return p, err
	}
	st, err := readState(filepath.Join(cfg.StateDir, "state.json"))
	if err != nil {
		return p, err
	}
	if st.InstanceID != cfg.InstanceID || !done(st.Operation) || !maintenanceTerminal(st.Maintenance) || st.Recovery != nil && st.Recovery.Phase != "ledger_linked" {
		return p, errors.New("updater: reconcile the existing service operation before self-update")
	}
	p.Child = st.Process
	if err = selfChild(ctx, p); err != nil {
		return p, err
	}
	p.HandoffSHA256, p.CommittedHeadSHA256, err = selfOtherOwners(cfg)
	if err != nil {
		return p, err
	}
	p.LockDevice, p.LockInode, err = lockIdentity(cfg.StateDir)
	if err != nil {
		return p, err
	}
	p.Candidate, p.CandidateSHA256, err = selfCandidate(p.Request)
	if err != nil {
		return p, err
	}
	if modern {
		p.PreparedBy = p.Updater
	}
	if err = selfPlanStatic(cfg, p, time.Now(), true); err != nil {
		return p, err
	}
	if err = selfManager(ctx, p, true, !modern); err != nil {
		return p, err
	}
	if !modern {
		if err := captureSelfStateDirectory(cfg, &p); err != nil {
			return p, err
		}
		if p.ExitEvidence != "" && p.ExitEvidenceSHA256 == "" {
			raw, e := privateFile(p.ExitEvidence)
			if e != nil {
				return p, e
			}
			p.ExitEvidenceSHA256 = bytesDigest(raw)
		}
		if err = selfExitEvidence(cfg, p); err != nil {
			return p, err
		}
	}
	return p, nil
}

type systemSelfActions struct {
	cfg   Config
	fence *selfStateDirectoryFence
}

func (a systemSelfActions) Check(ctx context.Context, p SelfUpdatePlan, phase string) error {
	if err := selfPlanStatic(a.cfg, p, time.Now(), true); err != nil {
		return err
	}
	if err := selfChild(ctx, p); err != nil {
		return err
	}
	if err := selfManager(ctx, p, phase == "active", true); err != nil {
		return err
	}
	if phase == "active" {
		if _, err := selfServiceState(a.cfg, p); err != nil {
			return err
		}
		return selfExitEvidence(a.cfg, p)
	}
	return nil
}
func (a systemSelfActions) Stop(ctx context.Context, p SelfUpdatePlan) error {
	_, err := (systemdUser{}).call(ctx, "stop", p.Unit)
	return err
}
func (a systemSelfActions) Install(ctx context.Context, p SelfUpdatePlan) (json.RawMessage, func(), error) {
	if err := a.Check(ctx, p, "stopped"); err != nil {
		return nil, nil, err
	}
	lock, err := lockDir(a.cfg.StateDir)
	if err != nil {
		return nil, nil, err
	}
	var once sync.Once
	closeLock := func() { once.Do(func() { _ = unlock(lock) }) }
	fail := func(e error) (json.RawMessage, func(), error) { closeLock(); return nil, nil, e }
	if err = selfPlanStatic(a.cfg, p, time.Now(), true); err != nil {
		return fail(err)
	}
	state, err := selfServiceState(a.cfg, p)
	if err != nil {
		return fail(err)
	}
	if state.InstanceID != a.cfg.InstanceID || state.Process != p.Child || !done(state.Operation) {
		return fail(ErrUnknown)
	}
	if a.fence != nil {
		if err := a.fence.restore(); err != nil {
			return fail(err)
		}
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return fail(err)
	}
	// Different instances can share one fixed executable. Serialize replacement,
	// but require each instance's own old PID to stop and its own receipt to adopt.
	f, err := os.OpenFile(p.UpdaterBinary+".install.lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fail(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return fail(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || stat.Nlink != 1 || int(stat.Uid) != os.Getuid() {
		return fail(errors.New("updater: unsafe shared installation lock"))
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fail(errors.New("updater: shared binary installation is busy"))
	}
	installed, err := hashFile(p.UpdaterBinary)
	if err != nil {
		return fail(err)
	}
	if installed != p.CandidateSHA256 {
		if installed != p.Updater.SHA256 {
			return fail(errors.New("updater: fixed executable changed to an unrelated artifact"))
		}
		if err = copyPinnedAtomic(p.Candidate, p.UpdaterBinary, 0o755, p.CandidateSHA256); err != nil {
			return fail(err)
		}
	}
	return raw, closeLock, nil
}
func (a systemSelfActions) Start(ctx context.Context, p SelfUpdatePlan) error {
	_, err := (systemdUser{}).call(ctx, "start", p.Unit)
	return err
}

func ApplySelfUpdate(ctx context.Context, cfg Config, p SelfUpdatePlan) (j SelfUpdateJournal, err error) {
	lock, err := installerLock(cfg)
	if err != nil {
		return j, err
	}
	defer lock.Close()
	if err := rejectRecordedSelfRequest(cfg, p.Request); err != nil {
		return j, err
	}
	j, err = readSelf(cfg)
	if err == nil && digest(j.Plan.Request) != digest(p.Request) {
		terminal, terminalErr := terminalSelf(cfg, j)
		if terminalErr != nil {
			return j, terminalErr
		}
		if terminal {
			if err := archiveSelf(cfg, j); err != nil {
				return j, err
			}
			err = os.ErrNotExist
		}
	}
	if missingSelf(err) {
		// Bootstrap does not contact any old control protocol.
		if p.PreparedBy != (Identity{}) {
			return j, errors.New("updater: modern preparation journal missing")
		}
		if err = selfPlanStatic(cfg, p, time.Now(), true); err != nil {
			return j, err
		}
		j = SelfUpdateJournal{Format: 1, Plan: p, Phase: "prepared"}
		if err = writeSelf(cfg, j); err != nil {
			return j, err
		}
	} else if err != nil {
		return j, err
	}
	if digest(j.Plan) != digest(p) {
		return j, errors.New("updater: self-update request conflicts with journal; retain original attempt")
	}
	var fence *selfStateDirectoryFence
	if j.Phase == "prepared" && p.Updater.SHA256 == p.CandidateSHA256 {
		err = (systemSelfActions{cfg: cfg}).Check(ctx, p, "active")
		if err == nil {
			j.Phase = "verified"
			j.Receipt = &protocol.UpdaterSelfReceipt{RequestID: p.Request.Target.RequestID, UpgradeID: p.Request.UpgradeID, ReleaseSetSHA256: p.Request.ReleaseSetSHA256, InstanceID: cfg.InstanceID, Previous: p.Updater, Installed: p.Updater, RetainedProcess: p.Child, CompletedAtMillis: time.Now().UnixMilli()}
			err = writeSelf(cfg, j)
		}
	} else {
		if p.PreparedBy == (Identity{}) && (j.Phase == "prepared" || j.Phase == "stopping" || j.Phase == "installing") {
			// Keep the private journal writable while fencing legacy state commits.
			if err := writeSelf(cfg, j); err != nil {
				return j, err
			}
			fence, err = fenceSelfStateDirectory(cfg, p)
			if err != nil {
				return j, err
			}
			defer func() {
				if j.Phase == "prepared" {
					err = errors.Join(err, fence.restore())
				}
			}()
		}
		err = advanceSelf(ctx, &j, func() error { return writeSelf(cfg, j) }, systemSelfActions{cfg: cfg, fence: fence}, time.Now)
	}
	if err != nil {
		j.LastError = err.Error()
		_ = writeSelf(cfg, j)
		if j.Phase == "prepared" {
			if fence != nil {
				if restoreErr := fence.restore(); restoreErr != nil {
					return j, errors.Join(err, restoreErr)
				}
			}
			_, err = persistSelfRejection(cfg, p.Request, time.Now(), err)
		}
	}
	return j, err
}

func SelfUpdateJournalStatus(cfg Config) (SelfUpdateJournal, error) {
	j, err := readSelf(cfg)
	if err != nil {
		return j, err
	}
	r, err := rejectedSelfBeforeStop(cfg, j)
	if r != nil {
		j.Phase = "rejected"
	}
	return j, err
}

// AdoptSelfUpdate runs after Open has acquired the original flock and before
// ordinary supervision can launch or stop a service.
func AdoptSelfUpdate(ctx context.Context, s *Supervisor) (bool, error) {
	j, err := readSelf(s.cfg)
	if missingSelf(err) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	p := j.Plan
	rejected, rejectedErr := rejectedSelfBeforeStop(s.cfg, j)
	if rejectedErr != nil {
		return true, rejectedErr
	}
	if j.Phase == "verified" || rejected != nil {
		legacy, committed, e := selfOtherOwners(s.cfg)
		if e != nil {
			return true, e
		}
		if legacy != p.HandoffSHA256 || committed != p.CommittedHeadSHA256 {
			// Completed ordinary history cannot adopt a newer independent
			// maintenance attempt. Its existing adopter validates that authority.
			if legacy != "" && legacy != p.HandoffSHA256 || committed != "" && committed != p.CommittedHeadSHA256 {
				return false, nil
			}
			return true, ErrUnknown
		}
		id, e := (LinuxProcess{}).Inspect(ctx, os.Getpid())
		if e != nil {
			return true, e
		}
		dev, ino, e := lockIdentity(s.cfg.StateDir)
		if e != nil {
			return true, e
		}
		raw, e := privateFile(p.ConfigFile)
		if e != nil {
			return true, e
		}
		if (id.SHA256 != p.CandidateSHA256 && (rejected == nil || id.SHA256 != p.Updater.SHA256)) || dev != p.LockDevice || ino != p.LockInode || bytesDigest(raw) != p.ConfigSHA256 {
			return true, ErrUnknown
		}
		m, policy, e := managerSnapshot(ctx, p.Unit)
		if e != nil {
			return true, e
		}
		unit, e := unitDigest(ctx, p.Unit)
		if e != nil {
			return true, e
		}
		if m["MainPID"] != strconv.Itoa(os.Getpid()) || policy != p.ManagerSHA256 || unit != p.UnitSHA256 {
			return true, ErrUnknown
		}
		if e = verifyUpdaterCommand(os.Getpid(), HandoffPlan{UpdaterBinary: p.UpdaterBinary, ConfigFile: p.ConfigFile}); e != nil {
			return true, e
		}
		s.selfIdentity = id
		return true, nil
	}
	if j.Phase != "starting" {
		return true, errors.New("updater: self-update pending; original attempt needs reconciliation")
	}
	if err = selfPlanStatic(s.cfg, p, time.Now(), false); err != nil {
		return true, err
	}
	current, err := (LinuxProcess{}).Inspect(ctx, os.Getpid())
	if err != nil {
		return true, err
	}
	if current.SHA256 != p.CandidateSHA256 {
		return true, errors.New("updater: running supervisor differs from target")
	}
	if err = selfChild(ctx, p); err != nil {
		return true, err
	}
	m, policy, err := managerSnapshot(ctx, p.Unit)
	if err != nil {
		return true, err
	}
	unit, err := unitDigest(ctx, p.Unit)
	if err != nil {
		return true, err
	}
	owner, known := flockOwner(p.LockDevice, p.LockInode)
	if policy != p.ManagerSHA256 || unit != p.UnitSHA256 || m["MainPID"] != strconv.Itoa(os.Getpid()) || !known || owner != os.Getpid() {
		return true, ErrUnknown
	}
	if err = verifyUpdaterCommand(os.Getpid(), HandoffPlan{UpdaterBinary: p.UpdaterBinary, ConfigFile: p.ConfigFile}); err != nil {
		return true, err
	}
	var original State
	if err = strictJSON(j.OriginalState, &original); err != nil {
		return true, err
	}
	// Open rotates only its own incarnation on formats >=2. Business state and
	// the supervised process must survive the handoff byte-for-byte canonically.
	original.Generation = s.state.Generation
	original.UpdaterEpoch = s.state.UpdaterEpoch
	original.UpdaterBootID = s.state.UpdaterBootID
	if digest(original) != digest(s.state) || s.state.Process != p.Child {
		return true, ErrUnknown
	}
	observed, e := (LinuxProcess{}).Observe(ctx, p.Updater.PID)
	if !errors.Is(e, ErrNotRunning) && (e != nil || observed.Identity.StartTicks == p.Updater.StartTicks) {
		return true, ErrUnknown
	}
	j.Phase = "verified"
	j.LastError = ""
	j.Receipt = &protocol.UpdaterSelfReceipt{RequestID: p.Request.Target.RequestID, UpgradeID: p.Request.UpgradeID, ReleaseSetSHA256: p.Request.ReleaseSetSHA256, InstanceID: s.cfg.InstanceID, Previous: p.Updater, Installed: current, RetainedProcess: p.Child, CompletedAtMillis: s.opts.Now().UnixMilli()}
	if err = writeSelf(s.cfg, j); err != nil {
		return true, err
	}
	s.selfIdentity = current
	return true, nil
}

func (s *Supervisor) selfStatus(ctx context.Context) (SelfUpdateStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := SelfUpdateStatus{Supported: s.opts.ConfigFile != ""}
	if !out.Supported {
		return out, errors.New("updater: fixed configuration path unavailable; bootstrap required")
	}
	if s.selfIdentity.PID == 0 {
		id, err := (LinuxProcess{}).Inspect(ctx, os.Getpid())
		if err != nil {
			return out, err
		}
		s.selfIdentity = id
	}
	out.Observation = protocol.UpdaterSelfObservation{InstanceID: s.cfg.InstanceID, Process: s.selfIdentity}
	child, err := s.observeCurrent(ctx, s.state.Process)
	if err != nil {
		return out, err
	}
	out.Observation.SupervisedProcess = child
	var j SelfUpdateJournal
	var readErr error
	if s.selfJournal != nil && s.selfJournal.Phase == "verified" {
		j = *s.selfJournal
	} else {
		j, readErr = readSelf(s.cfg)
	}
	err = readErr
	if missingSelf(err) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	j.OriginalState = nil
	if j.Phase == "verified" {
		cached := j
		s.selfJournal = &cached
	}
	if rejected, err := rejectedSelfBeforeStop(s.cfg, j); err != nil {
		return out, err
	} else if rejected != nil {
		j.Phase = "rejected"
		out.Observation.Rejection = &rejected.Proof
	}
	out.Journal = &j
	out.Observation.Receipt = j.Receipt
	return out, nil
}

func (s *Supervisor) requestSelf(ctx context.Context, r SelfUpdateRequest) (SelfUpdateStatus, error) {
	s.mu.Lock()
	if prior, err := readSelfRejection(s.cfg, r.Target.RequestID); err != nil {
		s.mu.Unlock()
		return SelfUpdateStatus{}, err
	} else if prior != nil {
		out, err := s.rejectSelf(r, errors.New("updater: original preparation was rejected before supervisor stop"))
		s.mu.Unlock()
		return out, err
	}
	j, err := readSelf(s.cfg)
	historyPath := filepath.Join(s.cfg.StateDir, "self-update-"+bytesDigest([]byte(r.Target.RequestID))+".json")
	if raw, e := privateFile(historyPath); e == nil {
		var old SelfUpdateJournal
		if e = strictJSON(raw, &old); e != nil {
			s.mu.Unlock()
			return SelfUpdateStatus{}, e
		}
		if digest(old.Plan.Request) != digest(r) {
			s.mu.Unlock()
			return SelfUpdateStatus{}, errors.New("updater: original request payload conflicts with history")
		}
		s.mu.Unlock()
		status, e := s.selfStatus(ctx)
		status.Observation.Receipt = old.Receipt
		return status, e
	} else if !os.IsNotExist(e) {
		s.mu.Unlock()
		return SelfUpdateStatus{}, e
	}
	if err == nil && j.Plan.Request.Target.RequestID == r.Target.RequestID && digest(j.Plan.Request) != digest(r) {
		s.mu.Unlock()
		return SelfUpdateStatus{}, errors.New("updater: original request payload conflicts with journal")
	}
	if err == nil && digest(j.Plan.Request) == digest(r) {
		s.mu.Unlock()
		return s.selfStatus(ctx)
	}
	if err == nil {
		terminal, terminalErr := terminalSelf(s.cfg, j)
		if terminalErr != nil || !terminal {
			s.mu.Unlock()
			return SelfUpdateStatus{}, errors.Join(errors.New("updater: original self-update attempt remains unresolved"), terminalErr)
		}
		if err = archiveSelf(s.cfg, j); err != nil {
			s.mu.Unlock()
			return SelfUpdateStatus{}, err
		}
	} else if !missingSelf(err) {
		s.mu.Unlock()
		return SelfUpdateStatus{}, err
	}
	s.selfJournal = nil
	p, err := prepareSelf(ctx, s.cfg, SelfUpdatePlan{Request: r, ConfigFile: s.opts.ConfigFile}, true)
	if err != nil {
		out, rejectedErr := s.rejectSelf(r, err)
		s.mu.Unlock()
		return out, rejectedErr
	}
	j = SelfUpdateJournal{Format: 1, Plan: p, Phase: "prepared"}
	if p.Updater.SHA256 == p.CandidateSHA256 {
		// Runtime equality, current lock ownership and child identity were verified
		// above. A shared path alone never reaches this branch.
		owner, known := flockOwner(p.LockDevice, p.LockInode)
		if !known || owner != os.Getpid() {
			s.mu.Unlock()
			return SelfUpdateStatus{}, ErrUnknown
		}
		j.Phase = "verified"
		s.selfIdentity = p.Updater
		j.Receipt = &protocol.UpdaterSelfReceipt{RequestID: r.Target.RequestID, UpgradeID: r.UpgradeID, ReleaseSetSHA256: r.ReleaseSetSHA256, InstanceID: s.cfg.InstanceID, Previous: p.Updater, Installed: p.Updater, RetainedProcess: p.Child, CompletedAtMillis: s.opts.Now().UnixMilli()}
		err = writeSelf(s.cfg, j)
		s.mu.Unlock()
		if err != nil {
			return SelfUpdateStatus{}, err
		}
		return s.selfStatus(ctx)
	}
	// Pin an independent installer before recording the one dispatch. It lives
	// outside the replaced executable path and survives the old supervisor's exit.
	installer := filepath.Join(s.cfg.StateDir, "self-installer-"+p.CandidateSHA256)
	if err = copyPinnedAtomic(p.Candidate, installer, 0o700, p.CandidateSHA256); err != nil {
		out, rejectedErr := s.rejectSelf(r, err)
		s.mu.Unlock()
		return out, rejectedErr
	}
	j.InstallerDispatched = true
	if err = writeSelf(s.cfg, j); err != nil {
		s.mu.Unlock()
		return SelfUpdateStatus{}, err
	}
	s.selfIdentity = p.Updater
	s.selfUpdating = true
	s.mu.Unlock()
	cmd := exec.CommandContext(ctx, "/usr/bin/systemd-run", "--user", "--quiet", "--collect", "--unit=tend-self-"+bytesDigest([]byte(s.cfg.StateDir + r.Target.RequestID))[:24], "--property=Type=exec", installer, "self-update", "apply", "--config", p.ConfigFile, "--plan", selfPath(s.cfg))
	cmd.WaitDelay = 2 * time.Second
	if err = cmd.Run(); err != nil {
		return SelfUpdateStatus{}, fmt.Errorf("updater: installer dispatch result unknown; reconcile original attempt: %w", err)
	}
	return s.selfStatus(ctx)
}

// writeSelf serializes journal writers across the installer and the adopting
// supervisor. A late StopUnit/StartUnit error cannot erase a durable receipt.
func writeSelf(cfg Config, next SelfUpdateJournal) error {
	if err := ensureSelfJournalDirectory(cfg); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(cfg.StateDir, "self-update", "journal.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	st, err := lock.Stat()
	if err != nil {
		return err
	}
	stat, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.Mode().IsRegular() || stat.Nlink != 1 || int(stat.Uid) != os.Getuid() {
		return errors.New("updater: unsafe self-update journal lock")
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	current, err := readSelf(cfg)
	if err == nil {
		if digest(current.Plan.Request) != digest(next.Plan.Request) {
			terminal, terminalErr := terminalSelf(cfg, current)
			if terminalErr != nil {
				return terminalErr
			}
			if !terminal || (next.Phase != "prepared" && next.Phase != "verified") {
				return errors.New("updater: stale self-update journal writer")
			}
			incomingHistory := filepath.Join(cfg.StateDir, "self-update-"+bytesDigest([]byte(next.Plan.Request.Target.RequestID))+".json")
			if _, e := privateFile(incomingHistory); !missingSelf(e) {
				return errors.New("updater: archived attempt cannot become current again")
			}
			archivedRaw, e := privateFile(filepath.Join(cfg.StateDir, "self-update-"+bytesDigest([]byte(current.Plan.Request.Target.RequestID))+".json"))
			if e != nil {
				return e
			}
			var archived SelfUpdateJournal
			if e = strictJSON(archivedRaw, &archived); e != nil {
				return e
			}
			if digest(archived) != digest(current) {
				return errors.New("updater: previous attempt was not durably archived")
			}
			if next.Phase == "verified" && (next.Receipt == nil || next.Receipt.RequestID != next.Plan.Request.Target.RequestID) {
				return ErrUnknown
			}
		} else {
			if digest(current.Plan) != digest(next.Plan) {
				return errors.New("updater: self-update plan changed")
			}
			ranks := map[string]int{"prepared": 1, "stopping": 2, "installing": 3, "starting": 4, "verified": 5}
			if ranks[current.Phase] > ranks[next.Phase] || current.Receipt != nil {
				return nil
			}
		}
	} else if !missingSelf(err) {
		return err
	}
	return writeJSON(selfPath(cfg), next)
}

func selfOtherOwners(cfg Config) (string, string, error) {
	values := []string{"", ""}
	for i, path := range []string{handoffPath(cfg), committedHeadPath(cfg)} {
		raw, err := optionalPrivate(path)
		if err != nil {
			return "", "", err
		}
		if len(raw) > 0 {
			values[i] = bytesDigest(raw)
		}
	}
	return values[0], values[1], nil
}
