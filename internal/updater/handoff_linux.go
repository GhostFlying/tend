//go:build linux

package updater

import (
	"bytes"
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
	"syscall"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
)

// HandoffPlan fixes the only supported adapter. No unit or service arguments
// are rewritten: installation atomically replaces exactly its regular ExecStart
// executable. A symlink, wrapper, alternate manager or changed policy is refused.
type HandoffPlan struct {
	OldUpdaterEvidence       string `json:"old_updater_evidence"`
	OldUpdaterEvidenceSHA256 string `json:"old_updater_evidence_sha256"`

	Recovery         protocol.ForwardRecoveryPlan `json:"recovery"`
	Unit             string                       `json:"unit"`
	UnitSHA256       string                       `json:"unit_sha256"`
	ManagerSHA256    string                       `json:"manager_sha256"`
	Updater          Identity                     `json:"updater"`
	UpdaterBinary    string                       `json:"updater_binary"`
	ConfigFile       string                       `json:"config_file"`
	ConfigFileSHA256 string                       `json:"config_file_sha256"`
	Candidate        string                       `json:"candidate"`
	CandidateSHA256  string                       `json:"candidate_sha256"`
	LockDevice       uint64                       `json:"lock_device"`
	LockInode        uint64                       `json:"lock_inode"`
}

type HandoffObservation struct {
	ObservedAtMillis int64    `json:"observed_at_millis"`
	InstalledSHA256  string   `json:"installed_sha256,omitempty"`
	ManagerState     string   `json:"manager_state,omitempty"`
	Updater          Identity `json:"updater"`
	Child            Identity `json:"child"`
	LockDevice       uint64   `json:"lock_device"`
	LockInode        uint64   `json:"lock_inode"`
	LockOwnerPID     int      `json:"lock_owner_pid"`
	LockOwnerKnown   bool     `json:"lock_owner_known"`
}

type HandoffJournal struct {
	Generation    uint64                         `json:"generation"`
	Continuations map[string]HandoffContinuation `json:"continuations,omitempty"`

	OriginalState json.RawMessage     `json:"original_state,omitempty"`
	Observation   *HandoffObservation `json:"observation,omitempty"`

	Plan      HandoffPlan `json:"plan"`
	Phase     string      `json:"phase"`
	LastError string      `json:"last_error,omitempty"`
}

type systemdUser struct{}

func (systemdUser) call(ctx context.Context, args ...string) ([]byte, error) {
	// The fixed executable bypasses PATH; arguments are never shell interpreted.
	cmd := exec.CommandContext(ctx, "/usr/bin/systemctl", append([]string{"--user", "--no-pager"}, args...)...)
	cmd.WaitDelay = 2 * time.Second
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("updater: systemd-user %s failed: %w", args[0], err)
	}
	return raw, nil
}

var managerProperties = []string{"Id", "LoadState", "ActiveState", "SubState", "MainPID", "ControlGroup", "KillMode", "KillSignal", "FinalKillSignal", "SendSIGKILL", "Restart", "ExecStart", "FragmentPath", "DropInPaths", "Type", "RemainAfterExit", "Delegate", "RefuseManualStop", "RefuseManualStart", "TimeoutStopUSec", "Job"}

func managerSnapshot(ctx context.Context, unit string) (map[string]string, string, error) {
	args := []string{"show", unit}
	for _, p := range managerProperties {
		args = append(args, "--property="+p)
	}
	raw, err := (systemdUser{}).call(ctx, args...)
	if err != nil {
		return nil, "", err
	}
	m := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, "", ErrUnknown
		}
		m[k] = v
	}
	return m, managerPolicyDigest(m), nil
}

func managerPolicyDigest(m map[string]string) string {
	// Process liveness changes during install. The immutable policy digest excludes
	// only runtime state; a changed unit, command, stop policy or cgroup is fatal.
	stable := map[string]string{}
	for k, v := range m {
		if k != "MainPID" && k != "ActiveState" && k != "SubState" && k != "Job" {
			if k == "ExecStart" {
				if before, _, ok := strings.Cut(v, " ; start_time="); ok {
					v = before + " }"
				}
			}
			stable[k] = v
		}
	}
	return digest(stable)
}
func unitDigest(ctx context.Context, unit string) (string, error) {
	raw, err := (systemdUser{}).call(ctx, "cat", unit)
	if err != nil {
		return "", err
	}
	return bytesDigest(raw), nil
}
func lockIdentity(dir string) (uint64, uint64, error) {
	info, err := os.Lstat(filepath.Join(dir, "supervisor.lock"))
	if err != nil {
		return 0, 0, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || st.Nlink != 1 || int(st.Uid) != os.Getuid() {
		return 0, 0, errors.New("updater: unsafe supervisor lock")
	}
	return uint64(st.Dev), st.Ino, nil
}
func handoffPath(cfg Config) string { return filepath.Join(cfg.StateDir, "handoff.json") }
func installerLock(cfg Config) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(cfg.StateDir, "installer.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("updater: another installer owns this window")
	}
	return f, nil
}

// UpdaterExitEvidence binds the reviewed shutdown behavior to the actual old
// artifact. KillMode alone cannot prove that an updater's own exit code retains
// its child; an unknown legacy artifact must never be stopped by this adapter.
type UpdaterExitEvidence struct {
	OperatorUID     int    `json:"operator_uid"`
	ArtifactSHA256  string `json:"artifact_sha256"`
	Source          string `json:"source"`
	ReviewArtifact  string `json:"review_artifact"`
	ExitPolicy      string `json:"exit_policy"`
	ExpiresAtMillis int64  `json:"expires_at_millis"`
}

func validateHandoffStatic(cfg Config, p HandoffPlan) error {
	return validateHandoffStaticIdentity(cfg, p, true)
}

func validateHandoffStaticIdentity(cfg Config, p HandoffPlan, retainedChild bool) error {
	originalValid := validIdentity(p.Recovery.OriginalProcess)
	if !retainedChild && p.Recovery.OriginalProcess == p.Updater {
		originalValid = p.Updater.PID > 1 && p.Updater.StartTicks > 0 && release.ValidSHA(p.Updater.SHA256)
	}
	if cfg.Role != "server" || !filepath.IsAbs(cfg.StateDir) || !strings.HasSuffix(p.Unit, ".service") || strings.ContainsAny(p.Unit, "/ \t\n") || strings.HasPrefix(p.Unit, "-") || !filepath.IsAbs(p.UpdaterBinary) || !filepath.IsAbs(p.Candidate) || !originalValid || p.Updater.PID <= 1 || p.Updater.StartTicks == 0 || !release.ValidSHA(p.Updater.SHA256) || !release.ValidSHA(p.CandidateSHA256) || !release.ValidSHA(p.UnitSHA256) || !release.ValidSHA(p.ManagerSHA256) || p.LockInode == 0 || p.LockDevice == 0 {
		return errors.New("updater: unsupported or incomplete systemd-user handoff plan")
	}
	if p.Recovery.ExpiresAtMillis <= time.Now().UnixMilli() {
		return errors.New("updater: installer authorization expired; no continuation permitted")
	}
	evidenceRaw, err := privateFile(p.OldUpdaterEvidence)
	if err != nil {
		return err
	}
	var evidence UpdaterExitEvidence
	if err := strictJSON(evidenceRaw, &evidence); err != nil {
		return err
	}
	if bytesDigest(evidenceRaw) != p.OldUpdaterEvidenceSHA256 || evidence.OperatorUID != os.Getuid() || evidence.ArtifactSHA256 != p.Updater.SHA256 || evidence.Source == "" || evidence.ReviewArtifact == "" || evidence.ExitPolicy != "retains-managed-child" || evidence.ExpiresAtMillis < p.Recovery.ExpiresAtMillis {
		return errors.New("updater: old artifact shutdown provenance has not been reviewed; child retention unknown")
	}
	raw, err := privateFile(p.ConfigFile)
	if err != nil {
		return err
	}
	if bytesDigest(raw) != p.ConfigFileSHA256 {
		return errors.New("updater: fixed configuration changed")
	}
	var actual Config
	if err := strictJSON(raw, &actual); err != nil {
		return err
	}
	// Open supplies default timeouts. Compare canonical resolved configuration.
	normalizeConfig(&actual)
	normalizeConfig(&cfg)
	if digest(actual) != digest(cfg) || digest(cfg) != p.Recovery.ConfigSHA256 {
		return errors.New("updater: handoff must preserve fixed service configuration")
	}
	if err := verifyResourceEvidence(p.Recovery, time.Now()); err != nil {
		return err
	}
	for _, path := range []string{p.UpdaterBinary, p.Candidate} {
		st, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !st.Mode().IsRegular() || !ownedFile(st) || st.Mode().Perm()&0o022 != 0 {
			return errors.New("updater: handoff requires operator-owned regular executable paths")
		}
	}
	sum, err := hashFile(p.Candidate)
	if err != nil {
		return err
	}
	if sum != p.CandidateSHA256 {
		return errors.New("updater: updater candidate digest changed")
	}
	dev, ino, err := lockIdentity(cfg.StateDir)
	if err != nil {
		return err
	}
	if dev != p.LockDevice || ino != p.LockInode {
		return errors.New("updater: supervisor lock inode changed")
	}
	return nil
}
func normalizeConfig(cfg *Config) {
	if cfg.StopTimeoutSeconds <= 0 {
		cfg.StopTimeoutSeconds = 30
	}
	if cfg.HealthTimeoutSeconds <= 0 {
		cfg.HealthTimeoutSeconds = 120
	}
}
func validateManager(ctx context.Context, p HandoffPlan, active bool) error {
	m, sum, err := managerSnapshot(ctx, p.Unit)
	if err != nil {
		return err
	}
	return validateManagerSnapshot(ctx, p, active, m, sum, "")
}

func validateManagerSnapshot(ctx context.Context, p HandoffPlan, active bool, m map[string]string, sum, expectedEmptyGroup string) error {
	unit, err := unitDigest(ctx, p.Unit)
	if err != nil {
		return err
	}
	if unit != p.UnitSHA256 || sum != p.ManagerSHA256 || m["Id"] != p.Unit || m["LoadState"] != "loaded" || m["KillMode"] != "process" || m["KillSignal"] != "15" || m["FinalKillSignal"] != "9" || m["SendSIGKILL"] != "yes" || m["Restart"] != "on-failure" || m["Type"] != "simple" || m["RemainAfterExit"] != "no" || m["RefuseManualStop"] != "no" || m["RefuseManualStart"] != "no" || (m["ControlGroup"] == "" && (active || expectedEmptyGroup == "")) {
		return errors.New("updater: unreviewed systemd-user policy; handoff refused")
	}
	if !strings.Contains(m["ExecStart"], "path="+p.UpdaterBinary+" ;") {
		return errors.New("updater: ExecStart is not the fixed updater executable")
	}
	selfGroup, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return err
	}
	group := m["ControlGroup"]
	if group == "" {
		group = expectedEmptyGroup
	}
	if strings.Contains(string(selfGroup), ":"+group+"\n") {
		return errors.New("updater: installer must run outside the managed service cgroup")
	}
	if active {
		pid, err := strconv.Atoi(m["MainPID"])
		if err != nil || pid != p.Updater.PID || m["ActiveState"] != "active" || m["SubState"] != "running" {
			return errors.New("updater: manager main identity changed")
		}
		id, err := (LinuxProcess{}).Inspect(ctx, pid)
		if err != nil {
			return err
		}
		if id != p.Updater {
			return ErrUnknown
		}
		if err := verifyUpdaterCommand(pid, p); err != nil {
			return err
		}
		for _, id := range []Identity{p.Updater, p.Recovery.OriginalProcess} {
			raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(id.PID), "cgroup"))
			if err != nil {
				return err
			}
			if !strings.Contains(string(raw), ":"+m["ControlGroup"]+"\n") {
				return errors.New("updater: retained child is outside the reviewed service cgroup")
			}
		}
	} else if m["MainPID"] != "0" || m["ActiveState"] != "inactive" || m["SubState"] != "dead" {
		return errors.New("updater: old manager has not stopped; do not install or start")
	}
	return nil
}
func verifyUpdaterCommand(pid int, p HandoffPlan) error {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return err
	}
	args := bytes.Split(bytes.TrimSuffix(raw, []byte{0}), []byte{0})
	expected := [][]byte{[]byte(p.UpdaterBinary), []byte("serve"), []byte("--config"), []byte(p.ConfigFile)}
	alternate := [][]byte{[]byte(p.UpdaterBinary), []byte("serve"), []byte("-config"), []byte(p.ConfigFile)}
	if !reflect.DeepEqual(args, expected) && !reflect.DeepEqual(args, alternate) {
		return errors.New("updater: unsupported updater command; no unit argument rewrite allowed")
	}
	return nil
}
func retainedChild(ctx context.Context, p HandoffPlan) error {
	id, err := (LinuxProcess{}).Inspect(ctx, p.Recovery.OriginalProcess.PID)
	if err != nil {
		return err
	}
	if id != p.Recovery.OriginalProcess {
		return ErrUnknown
	}
	return nil
}

// PlanHandoff returns current guard evidence without stopping a unit or opening
// the supervisor lock. Unknown policies fail closed. No old RPC is used.
func PlanHandoff(ctx context.Context, cfg Config, p HandoffPlan) error {
	if err := validateHandoffStatic(cfg, p); err != nil {
		return err
	}
	if err := validateManager(ctx, p, true); err != nil {
		return err
	}
	if err := retainedChild(ctx, p); err != nil {
		return err
	}
	st, err := readState(filepath.Join(cfg.StateDir, "state.json"))
	if err != nil {
		return err
	}
	normalizeConfig(&cfg)
	s := &Supervisor{cfg: cfg, state: st, opts: Options{Process: LinuxProcess{}, Now: time.Now}}
	if err := s.validateRecovery(ctx, p.Recovery); err != nil {
		return err
	}
	sum, err := hashFile(p.UpdaterBinary)
	if err != nil {
		return err
	}
	if sum != p.Updater.SHA256 {
		return errors.New("updater: original updater executable changed")
	}
	return nil
}

// ApplyHandoff journals every external action before issuing it. An interrupted
// stop/install/start is reconciled by status; it is never blindly reissued.
func ApplyHandoff(ctx context.Context, cfg Config, p HandoffPlan) (result HandoffJournal, err error) {
	lock, err := installerLock(cfg)
	if err != nil {
		return result, err
	}
	defer lock.Close()
	path := handoffPath(cfg)
	if raw, e := privateFile(path); e == nil {
		if e := strictJSON(raw, &result); e != nil {
			return result, e
		}
		if digest(result.Plan) != digest(p) {
			return result, errors.New("updater: installer request conflicts with existing journal")
		}
		return HandoffStatus(ctx, cfg)
	} else if !os.IsNotExist(e) {
		return result, e
	}
	if err := PlanHandoff(ctx, cfg, p); err != nil {
		return result, err
	}
	original, err := readState(filepath.Join(cfg.StateDir, "state.json"))
	if err != nil {
		return result, err
	}
	originalRaw, err := json.Marshal(original)
	if err != nil {
		return result, err
	}
	if digest(original) != p.Recovery.StateSHA256 {
		return result, errors.New("updater: state changed before planned journal")
	}
	result = HandoffJournal{Plan: p, Phase: "planned", OriginalState: originalRaw}
	save := func(phase string) error { result.Phase = phase; result.Generation++; return writeJSON(path, result) }
	if err := save(result.Phase); err != nil {
		return result, err
	}
	defer func() {
		if err != nil {
			result.LastError = err.Error()
			_ = writeJSON(path, result)
		}
	}()
	if err = save("stopping_updater"); err != nil {
		return result, err
	}
	if _, err = (systemdUser{}).call(ctx, "stop", p.Unit); err != nil {
		return result, err
	}
	if err = validateManager(ctx, p, false); err != nil {
		return result, err
	}
	if err = retainedChild(ctx, p); err != nil {
		return result, err
	}
	if _, e := (LinuxProcess{}).Inspect(ctx, p.Updater.PID); !errors.Is(e, ErrNotRunning) {
		return result, errors.New("updater: old updater exit unknown")
	}
	// Probe the original inode with nonblocking flock, retaining ownership across
	// installation. The replacement cannot run until this file descriptor closes.
	owner, e := lockDir(cfg.StateDir)
	if e != nil {
		return result, e
	}
	defer owner.Close()
	if e = validateHandoffStatic(cfg, p); e != nil {
		return result, e
	}
	if err := validateStoppedSnapshot(cfg, p); err != nil {
		return result, err
	}
	if err = save("installing_updater"); err != nil {
		return result, err
	}
	if err = copyPinnedAtomic(p.Candidate, p.UpdaterBinary, 0o755, p.CandidateSHA256); err != nil {
		return result, err
	}
	if err = save("starting_adopt"); err != nil {
		return result, err
	}
	if err = owner.Close(); err != nil {
		return result, err
	}
	if _, err = (systemdUser{}).call(ctx, "start", p.Unit); err != nil {
		return result, err
	}
	return HandoffStatus(ctx, cfg)
}

// HandoffStatus is read-only. It reports runtime facts separately from intent.
// A partial action never turns into another start as a side effect of status.
func HandoffStatus(ctx context.Context, cfg Config) (HandoffJournal, error) {
	var j HandoffJournal
	raw, err := privateFile(handoffPath(cfg))
	if err != nil {
		return j, err
	}
	if err := strictJSON(raw, &j); err != nil {
		return j, err
	}
	observation := &HandoffObservation{ObservedAtMillis: time.Now().UnixMilli()}
	j.Observation = observation
	observation.InstalledSHA256, _ = hashFile(j.Plan.UpdaterBinary)
	observation.LockDevice, observation.LockInode, _ = lockIdentity(cfg.StateDir)
	observation.LockOwnerPID, observation.LockOwnerKnown = flockOwner(observation.LockDevice, observation.LockInode)
	if m, _, e := managerSnapshot(ctx, j.Plan.Unit); e == nil {
		observation.ManagerState = m["ActiveState"] + "/" + m["SubState"]
		if pid, e := strconv.Atoi(m["MainPID"]); e == nil && pid > 1 {
			observation.Updater, _ = (LinuxProcess{}).Inspect(ctx, pid)
		}
	}
	observation.Child, _ = (LinuxProcess{}).Inspect(ctx, j.Plan.Recovery.OriginalProcess.PID)
	state, err := readState(filepath.Join(cfg.StateDir, "state.json"))
	if err != nil {
		return j, err
	}
	if state.Recovery != nil && digest(state.Recovery.Plan) == digest(j.Plan.Recovery) && state.Format == 2 && state.UpdaterEpoch != "" {
		if err := retainedChild(ctx, j.Plan); err != nil && state.Recovery.Phase == "updater_adopted" {
			return j, err
		}
		m, _, err := managerSnapshot(ctx, j.Plan.Unit)
		if err != nil {
			return j, err
		}
		pid, err := strconv.Atoi(m["MainPID"])
		if err != nil {
			return j, err
		}
		id, err := (LinuxProcess{}).Inspect(ctx, pid)
		if err != nil {
			return j, err
		}
		if id.SHA256 != j.Plan.CandidateSHA256 {
			return j, errors.New("updater: manager has not adopted the installed candidate")
		}
		j.Phase = "updater_adopted"
		return j, nil
	}
	return j, errors.New("updater: handoff outcome requires reconciliation; child and manager must be inspected before further action")
}

// AdoptPendingHandoff runs before Serve can poll. The journal is mandatory for
// migration and pins the exact candidate, config, lock and original child.
func AdoptPendingHandoff(ctx context.Context, s *Supervisor) error {
	if handled, err := AdoptCommittedHandoff(ctx, s); handled {
		return err
	}
	raw, err := privateFile(handoffPath(s.cfg))
	if os.IsNotExist(err) {
		if s.state.Format == 2 {
			return ErrUnknown
		}
		return nil
	}
	if err != nil {
		return err
	}
	var j HandoffJournal
	if err := strictJSON(raw, &j); err != nil {
		return err
	}
	// Every startup revalidates this installed updater and its lock ownership;
	// old resource grant expiry is not renewed or required for ordinary restarts.
	exe, err := hashFile("/proc/self/exe")
	if err != nil {
		return err
	}
	if exe != j.Plan.CandidateSHA256 {
		return errors.New("updater: adopt binary is not the reviewed installed artifact")
	}
	dev, ino, err := lockIdentity(s.cfg.StateDir)
	if err != nil {
		return err
	}
	if dev != j.Plan.LockDevice || ino != j.Plan.LockInode || digest(s.cfg) != j.Plan.Recovery.ConfigSHA256 {
		return errors.New("updater: adopted configuration or lock ownership changed")
	}
	m, policy, err := managerSnapshot(ctx, j.Plan.Unit)
	if err != nil {
		return err
	}
	unit, err := unitDigest(ctx, j.Plan.Unit)
	if err != nil {
		return err
	}
	if policy != j.Plan.ManagerSHA256 || unit != j.Plan.UnitSHA256 || m["MainPID"] != strconv.Itoa(os.Getpid()) {
		return errors.New("updater: adopt startup does not belong to the reviewed fixed manager")
	}
	if err := verifyUpdaterCommand(os.Getpid(), j.Plan); err != nil {
		return err
	}
	if s.state.Recovery != nil {
		if digest(s.state.Recovery.Plan) != digest(j.Plan.Recovery) {
			return ErrUnknown
		}
		return nil
	}
	if j.Phase != "starting_adopt" {
		return errors.New("updater: installer did not authorize adopt startup")
	}
	if err := validateHandoffStatic(s.cfg, j.Plan); err != nil {
		return err
	}
	if err := retainedChild(ctx, j.Plan); err != nil {
		return err
	}
	return s.AdoptRecovery(ctx, j.Plan.Recovery)
}

// PrepareHandoffPlan fills only missing factual fields for review. Apply never
// refreshes them: any changed fact invalidates the exact reviewed plan.
func PrepareHandoffPlan(ctx context.Context, cfg Config, p HandoffPlan) (HandoffPlan, error) {
	if !strings.HasSuffix(p.Unit, ".service") || strings.ContainsAny(p.Unit, "/ \t\n") || strings.HasPrefix(p.Unit, "-") {
		return p, errors.New("updater: explicit supported unit identity required before manager query")
	}

	normalizeConfig(&cfg)
	state, err := readState(filepath.Join(cfg.StateDir, "state.json"))
	if err != nil {
		return p, err
	}
	if p.Recovery.StateSHA256 == "" {
		p.Recovery.StateSHA256 = digest(state)
		p.Recovery.ExpectedGeneration = state.Generation
	}
	if p.Recovery.ConfigSHA256 == "" {
		p.Recovery.ConfigSHA256 = digest(cfg)
	}
	if p.Recovery.Original == (Binding{}) && state.Operation != nil {
		p.Recovery.Original = state.Operation.Binding
	}
	if p.Recovery.OriginalProcess == (Identity{}) && state.Operation != nil {
		p.Recovery.OriginalProcess = state.Operation.CandidateProcess
	}
	if p.ConfigFileSHA256 == "" {
		raw, err := privateFile(p.ConfigFile)
		if err != nil {
			return p, err
		}
		p.ConfigFileSHA256 = bytesDigest(raw)
	}
	if p.CandidateSHA256 == "" {
		p.CandidateSHA256, err = hashFile(p.Candidate)
		if err != nil {
			return p, err
		}
	}
	if p.UnitSHA256 == "" {
		p.UnitSHA256, err = unitDigest(ctx, p.Unit)
		if err != nil {
			return p, err
		}
	}
	m, manager, err := managerSnapshot(ctx, p.Unit)
	if err != nil {
		return p, err
	}
	if p.ManagerSHA256 == "" {
		p.ManagerSHA256 = manager
	}
	if p.Updater.PID == 0 {
		pid, err := strconv.Atoi(m["MainPID"])
		if err != nil {
			return p, err
		}
		p.Updater, err = (LinuxProcess{}).Inspect(ctx, pid)
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
	if p.Recovery.Successor == (Binding{}) {
		b, err := release.Load(p.Recovery.ReleaseDir)
		if err != nil {
			return p, err
		}
		a, err := b.Artifact("server", runtime.GOOS, runtime.GOARCH)
		if err != nil {
			return p, err
		}
		launch, err := newID()
		if err != nil {
			return p, err
		}
		p.Recovery.Successor = Binding{UpgradeID: p.Recovery.RecoveryID, InstanceID: cfg.InstanceID, ProcessInstanceID: launch, ReleaseID: b.Manifest.ReleaseID, ManifestSHA256: b.ManifestSHA256, ArtifactSHA256: a.SHA256}
	}
	return p, PlanHandoff(ctx, cfg, p)
}

func flockOwner(device, inode uint64) (int, bool) {
	raw, err := os.ReadFile("/proc/locks")
	if err != nil {
		return 0, false
	}
	major := ((device >> 8) & 0xfff) | ((device >> 32) & 0xfffff000)
	minor := (device & 0xff) | ((device >> 12) & 0xffffff00)
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 || fields[1] != "FLOCK" || fields[3] != "WRITE" {
			continue
		}
		parts := strings.Split(fields[5], ":")
		if len(parts) != 3 {
			continue
		}
		maj, e1 := strconv.ParseUint(parts[0], 16, 64)
		min, e2 := strconv.ParseUint(parts[1], 16, 64)
		ino, e3 := strconv.ParseUint(parts[2], 10, 64)
		if e1 == nil && e2 == nil && e3 == nil && maj == major && min == minor && ino == inode {
			pid, err := strconv.Atoi(fields[4])
			return pid, err == nil && pid > 1
		}
	}
	return 0, true
}

// HandoffContinuation is a fresh, bounded local authorization to continue an
// installer after observing its real state. Replaying this request never repeats
// a stop/start, including when its previous result is unknown.
type HandoffContinuation struct {
	ExpectedGeneration uint64 `json:"expected_generation"`
	Action             string `json:"action"`
	AuthorizedAtMillis int64  `json:"authorized_at_millis"`
}

func handoffResumeGuard(ctx context.Context, cfg Config, j HandoffJournal) (string, error) {
	if err := validateHandoffStatic(cfg, j.Plan); err != nil {
		return "", err
	}
	if j.Phase != "planned" && j.Phase != "stopping_updater" && j.Phase != "installing_updater" && j.Phase != "starting_adopt" {
		return "", errors.New("updater: installer phase cannot be continued")
	}
	state, err := readState(filepath.Join(cfg.StateDir, "state.json"))
	if err != nil {
		return "", err
	}
	if state.Format != 1 || digest(state) != j.Plan.Recovery.StateSHA256 {
		return "", errors.New("updater: original installer snapshot changed; use status to reconcile")
	}
	var snapshot State
	if err := json.Unmarshal(j.OriginalState, &snapshot); err != nil || digest(snapshot) != j.Plan.Recovery.StateSHA256 {
		return "", errors.New("updater: retained planned snapshot mismatch")
	}
	if err := retainedChild(ctx, j.Plan); err != nil {
		return "", err
	}
	m, _, err := managerSnapshot(ctx, j.Plan.Unit)
	if err != nil {
		return "", err
	}
	if job, known := m["Job"]; !known || job != "0" && job != "" {
		return "", errors.New("updater: manager action still pending; continuation refused")
	}
	if m["ActiveState"] == "active" && m["MainPID"] == strconv.Itoa(j.Plan.Updater.PID) && (j.Phase == "planned" || j.Phase == "stopping_updater") {
		if err := validateManager(ctx, j.Plan, true); err != nil {
			return "", err
		}
		return "stop_install_start", nil
	}
	if err := validateManager(ctx, j.Plan, false); err != nil {
		return "", err
	}
	if _, err := (LinuxProcess{}).Inspect(ctx, j.Plan.Updater.PID); !errors.Is(err, ErrNotRunning) {
		return "", errors.New("updater: old updater exit is not confirmed")
	}
	sum, err := hashFile(j.Plan.UpdaterBinary)
	if err != nil {
		return "", err
	}
	if sum != j.Plan.Updater.SHA256 && sum != j.Plan.CandidateSHA256 {
		return "", errors.New("updater: installed updater is neither the original nor the reviewed candidate")
	}
	return "install_start", nil
}
func loadHandoff(cfg Config, p HandoffPlan) (HandoffJournal, error) {
	var j HandoffJournal
	raw, err := privateFile(handoffPath(cfg))
	if err != nil {
		return j, err
	}
	if err := strictJSON(raw, &j); err != nil {
		return j, err
	}
	if digest(j.Plan) != digest(p) {
		return j, errors.New("updater: conflicting installer continuation plan")
	}
	return j, nil
}
func validateContinuation(p HandoffPlan, id string, generation uint64) error {
	if !protocol.ValidID(protocol.PrefixRequest, id) || id == p.Recovery.RequestID || id == p.Recovery.LinkRequestID || id == p.Recovery.OriginalRequestID || generation == 0 {
		return errors.New("updater: continuation requires a distinct stable request ID and journal generation")
	}
	return nil
}

// PlanHandoffResume observes guards only. It authorizes no manager action.
func PlanHandoffResume(ctx context.Context, cfg Config, p HandoffPlan, id string, generation uint64) (string, error) {
	if err := validateContinuation(p, id, generation); err != nil {
		return "", err
	}
	j, err := loadHandoff(cfg, p)
	if err != nil {
		return "", err
	}
	if j.Generation != generation {
		return "", errors.New("updater: stale installer continuation generation")
	}
	return handoffResumeGuard(ctx, cfg, j)
}

// ResumeHandoff is an explicit new maintenance intent, not an automatic retry of
// an uncertain start. It never changes the original recovery target or expiry.
// Systemd serializes the same fixed unit; the original supervisor flock fences
// all state writers. Only updater installation/start is continued here, never a
// second server start. Server replacement still needs its separate authorization.
func ResumeHandoff(ctx context.Context, cfg Config, p HandoffPlan, id string, generation uint64) (j HandoffJournal, err error) {
	if err := validateContinuation(p, id, generation); err != nil {
		return j, err
	}
	installer, err := installerLock(cfg)
	if err != nil {
		return j, err
	}
	defer installer.Close()
	j, err = loadHandoff(cfg, p)
	if err != nil {
		return j, err
	}
	if recorded, ok := j.Continuations[id]; ok {
		if recorded.ExpectedGeneration != generation {
			return j, errors.New("updater: continuation request payload conflict")
		}
		return HandoffStatus(ctx, cfg)
	}
	if j.Generation != generation {
		return j, errors.New("updater: stale installer continuation generation")
	}
	action, err := handoffResumeGuard(ctx, cfg, j)
	if err != nil {
		return j, err
	}
	if j.Continuations == nil {
		j.Continuations = map[string]HandoffContinuation{}
	}
	j.Continuations[id] = HandoffContinuation{ExpectedGeneration: generation, Action: action, AuthorizedAtMillis: time.Now().UnixMilli()}
	save := func(phase string) error { j.Phase = phase; j.Generation++; return writeJSON(handoffPath(cfg), j) }
	if err := save(j.Phase); err != nil {
		return j, err
	}
	if action == "stop_install_start" {
		if err := save("stopping_updater"); err != nil {
			return j, err
		}
		if _, err := (systemdUser{}).call(ctx, "stop", p.Unit); err != nil {
			return j, err
		}
	}
	if err := validateManager(ctx, p, false); err != nil {
		return j, err
	}
	if err := retainedChild(ctx, p); err != nil {
		return j, err
	}
	if _, err := (LinuxProcess{}).Inspect(ctx, p.Updater.PID); !errors.Is(err, ErrNotRunning) {
		return j, errors.New("updater: old updater has not exited")
	}
	owner, err := lockDir(cfg.StateDir)
	if err != nil {
		return j, err
	}
	defer owner.Close()
	// Repeat all mutable guards while owning the original inode.
	if _, err := handoffResumeGuard(ctx, cfg, j); err != nil {
		return j, err
	}
	if err := save("installing_updater"); err != nil {
		return j, err
	}
	current, err := hashFile(p.UpdaterBinary)
	if err != nil {
		return j, err
	}
	if current != p.CandidateSHA256 {
		if err := copyPinnedAtomic(p.Candidate, p.UpdaterBinary, 0o755, p.CandidateSHA256); err != nil {
			return j, err
		}
	} else if err := syncDir(filepath.Dir(p.UpdaterBinary)); err != nil {
		return j, err
	}
	if err := save("starting_adopt"); err != nil {
		return j, err
	}
	if err := owner.Close(); err != nil {
		return j, err
	}
	if _, err := (systemdUser{}).call(ctx, "start", p.Unit); err != nil {
		return j, err
	}
	return HandoffStatus(ctx, cfg)
}

// The old poller may finish a write while systemd is stopping it. Installation
// must compare the original full snapshot again while holding that same flock.
func validateStoppedSnapshot(cfg Config, p HandoffPlan) error {
	state, err := readState(filepath.Join(cfg.StateDir, "state.json"))
	if err != nil {
		return err
	}
	if state.Generation != p.Recovery.ExpectedGeneration || digest(state) != p.Recovery.StateSHA256 {
		return errors.New("updater: original state changed before installation; fixed updater was preserved")
	}
	return nil
}
