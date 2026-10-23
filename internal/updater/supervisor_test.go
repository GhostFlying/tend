//go:build linux

package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/release"
)

type fakeProcess struct {
	next           int
	live           map[int]Identity
	starts, stops  int
	ambiguousStart bool
	rejectStart    bool
	releaseIDs     []string
}

func (p *fakeProcess) Inspect(_ context.Context, pid int) (Identity, error) {
	v, ok := p.live[pid]
	if !ok {
		return Identity{}, ErrNotRunning
	}
	return v, nil
}
func (p *fakeProcess) Find(_ context.Context, launch, sha string) ([]Identity, error) {
	var a []Identity
	for _, v := range p.live {
		if v.LaunchID == launch && v.SHA256 == sha {
			a = append(a, v)
		}
	}
	return a, nil
}
func (p *fakeProcess) Start(_ context.Context, binary string, _ Config, env map[string]string) (Identity, error) {
	if p.rejectStart {
		p.rejectStart = false
		return Identity{}, ErrNotStarted
	}
	p.next++
	p.starts++
	p.releaseIDs = append(p.releaseIDs, env["TEND_UPDATER_RELEASE_ID"])
	sum, err := hashFile(binary)
	if err != nil {
		return Identity{}, err
	}
	v := Identity{PID: p.next, StartTicks: uint64(p.next), SHA256: sum, LaunchID: env["TEND_UPDATER_LAUNCH_ID"]}
	p.live[v.PID] = v
	if p.ambiguousStart {
		p.ambiguousStart = false
		return Identity{}, ErrUnknown
	}
	return v, nil
}
func (p *fakeProcess) Stop(_ context.Context, id Identity, _ time.Duration) error {
	v, ok := p.live[id.PID]
	if !ok {
		return nil
	}
	if v.StartTicks != id.StartTicks || v.SHA256 != id.SHA256 {
		return ErrUnknown
	}
	p.stops++
	delete(p.live, id.PID)
	return nil
}

type fakeAuthority struct {
	now                                                        *int64
	mismatch, deny, unconfirmed, accepted, rollback, unhealthy bool
	sequence                                                   uint64
	delay                                                      int64
	failHealth                                                 bool
}

func (a *fakeAuthority) Authorize(_ context.Context, b Binding) (Grant, error) {
	*a.now += a.delay
	if a.mismatch {
		b.ArtifactSHA256 = "wrong"
	}
	return Grant{Binding: b, Allowed: !a.deny, RemainingMillis: 10000}, nil
}
func (a *fakeAuthority) Health(_ context.Context, b Binding) (Health, error) {
	if a.failHealth {
		return Health{}, errors.New("authority unavailable")
	}
	a.sequence++
	return Health{Binding: b, ServerConfirmed: !a.unconfirmed, ServerRevision: 2, Healthy: !a.unhealthy, HeartbeatSequence: a.sequence, NewWorkAccepted: a.accepted, RollbackSafe: a.rollback}, nil
}

type harness struct {
	s      *Supervisor
	cfg    Config
	opts   Options
	p      *fakeProcess
	a      *fakeAuthority
	boot   int64
	now    time.Time
	bundle string
	data   string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	root := t.TempDir()
	old := filepath.Join(root, "old")
	if err := os.WriteFile(old, []byte("old executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(root, "ledger.db")
	if err := os.WriteFile(data, []byte("old ledger bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &harness{boot: 1000, now: time.Unix(1000, 0), data: data}
	h.cfg = Config{InstanceID: "runner-test", Role: "runner", StateDir: filepath.Join(root, "supervisor"), InitialBinary: old, LogPath: filepath.Join(root, "service.log"), Protocol: 1, StorageVersion: 8, DataFiles: []string{data}, HealthTimeoutSeconds: 10}
	sum, _ := hashFile(old)
	h.p = &fakeProcess{next: 10, live: map[int]Identity{10: {PID: 10, StartTicks: 10, SHA256: sum, LaunchID: "original-runtime"}}}
	h.a = &fakeAuthority{now: &h.boot}
	ids := 0
	h.opts = Options{Process: h.p, Authority: h.a, Now: func() time.Time { return h.now }, NewID: func() (string, error) { ids++; return "process-" + time.Unix(int64(ids), 0).Format("150405"), nil }, BootClock: func() (string, int64, error) { return "boot-A", h.boot, nil }}
	var err error
	h.s, err = Open(h.cfg, h.opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.s.Close() })
	if err := h.s.Bootstrap(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	h.bundle = makeBundle(t, []byte("new executable"))
	return h
}
func makeBundle(t *testing.T, body []byte) string {
	t.Helper()
	dir := t.TempDir()
	sum := sha256.Sum256(body)
	m := release.Manifest{Format: 1, ReleaseID: "new-release", Protocol: release.Range{Min: 1, Max: 2}, Storage: release.Storage{ServerSchema: release.Range{Min: 26, Max: 27}, RunnerJournal: release.Range{Min: 8, Max: 9}}, Artifacts: []release.Artifact{{Role: "runner", OS: runtime.GOOS, Arch: runtime.GOARCH, Path: "binary", Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:])}}}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string][]byte{"manifest.json": raw, "binary": body} {
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
func (h *harness) upgrade(t *testing.T) {
	t.Helper()
	if err := h.s.Upgrade(context.Background(), "upgrade-1", h.bundle); err != nil {
		t.Fatal(err)
	}
}
func (h *harness) step(t *testing.T) {
	t.Helper()
	if err := h.s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func (h *harness) until(t *testing.T, phase string) {
	t.Helper()
	for i := 0; i < 20; i++ {
		if h.s.Status().Operation.Phase == phase {
			return
		}
		h.step(t)
	}
	t.Fatalf("phase %s not reached: %+v", phase, h.s.Status().Operation)
}
func (h *harness) reopen(t *testing.T) {
	t.Helper()
	if err := h.s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(h.cfg, h.opts)
	if err != nil {
		t.Fatal(err)
	}
	h.s = s
}
func TestUpgradeRecoversEveryRecordedBoundary(t *testing.T) {
	h := newHarness(t)
	h.upgrade(t)
	for i := 0; i < 12; i++ {
		if h.s.Status().Operation.Phase == "committed" {
			break
		}
		h.step(t)
		h.reopen(t)
	}
	s := h.s.Status()
	if s.Operation.Phase != "committed" || h.p.starts != 1 || h.p.stops != 1 {
		t.Fatalf("not exactly one replacement: %+v starts=%d stops=%d", s, h.p.starts, h.p.stops)
	}
	if h.s.Status().Current.ReleaseID != "new-release" {
		t.Fatal("new version not current")
	}
	if raw, _ := os.ReadFile(h.data); string(raw) != "old ledger bytes" {
		t.Fatal("successful upgrade modified data")
	}
}
func TestGrantBindingAndMonotonicExpiryBeforeStop(t *testing.T) {
	for _, kind := range []string{"mismatch", "expired", "denied"} {
		t.Run(kind, func(t *testing.T) {
			h := newHarness(t)
			h.upgrade(t)
			switch kind {
			case "mismatch":
				h.a.mismatch = true
			case "denied":
				h.a.deny = true
			case "expired":
				h.a.delay = 10001
			}
			if err := h.s.Step(context.Background()); err == nil {
				t.Fatal("invalid grant accepted")
			}
			if h.p.stops != 0 || h.p.starts != 0 {
				t.Fatal("invalid grant disturbed old process")
			}
		})
	}
}
func TestUnknownStartIsReconciledWithoutDuplicateProcess(t *testing.T) {
	h := newHarness(t)
	h.upgrade(t)
	h.until(t, "backed_up")
	h.p.ambiguousStart = true
	if err := h.s.Step(context.Background()); !errors.Is(err, ErrUnknown) {
		t.Fatalf("expected unknown, got %v", err)
	}
	h.reopen(t)
	h.until(t, "committed")
	if h.p.starts != 1 {
		t.Fatal("ambiguous start duplicated")
	}
}
func TestMissingAmbiguousProcessNeverBlindlyStartsAgain(t *testing.T) {
	h := newHarness(t)
	h.upgrade(t)
	h.until(t, "backed_up")
	h.p.ambiguousStart = true
	_ = h.s.Step(context.Background())
	h.p.live = map[int]Identity{}
	h.reopen(t)
	if err := h.s.Step(context.Background()); !errors.Is(err, ErrUnknown) {
		t.Fatalf("expected unknown: %v", err)
	}
	if h.p.starts != 1 {
		t.Fatal("unknown launch replayed")
	}
}
func TestFailedCandidateRestoresOnlyWithAuthoritativeProof(t *testing.T) {
	h := newHarness(t)
	h.upgrade(t)
	h.until(t, "checking")
	if err := os.WriteFile(h.data, []byte("candidate migrated data"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.a.unhealthy = true
	h.a.rollback = true
	h.now = h.now.Add(11 * time.Second)
	h.until(t, "rolled_back")
	raw, _ := os.ReadFile(h.data)
	if string(raw) != "old ledger bytes" {
		t.Fatal("matching backup not restored")
	}
	if h.p.starts != 2 {
		t.Fatal("old process not restored")
	}
}
func TestAcceptedBusinessPermanentlyForbidsDataRollback(t *testing.T) {
	h := newHarness(t)
	h.upgrade(t)
	h.until(t, "checking")
	h.a.accepted = true
	h.a.unhealthy = true
	h.a.rollback = true
	h.now = h.now.Add(11 * time.Second)
	if err := h.s.Step(context.Background()); err == nil {
		t.Fatal("unhealthy candidate unexpectedly accepted")
	}
	h.reopen(t)
	if !h.s.Status().Operation.Irreversible {
		t.Fatal("new work evidence was not durable")
	}
	if h.p.stops != 1 {
		t.Fatal("candidate stopped despite irreversible work")
	}
	if _, err := os.Stat(filepath.Join(h.s.Status().Operation.BackupDir, "files.json")); err != nil {
		t.Fatal(err)
	}
}
func TestUnconfirmedAndUnavailableHealthCannotCommitOrRollback(t *testing.T) {
	for _, kind := range []string{"unconfirmed", "unavailable"} {
		t.Run(kind, func(t *testing.T) {
			h := newHarness(t)
			h.upgrade(t)
			h.until(t, "checking")
			if kind == "unconfirmed" {
				h.a.unconfirmed = true
			} else {
				h.a.failHealth = true
			}
			h.now = h.now.Add(time.Hour)
			if err := h.s.Step(context.Background()); err == nil {
				t.Fatal("unknown health accepted")
			}
			if h.s.Status().Operation.Phase != "checking" || h.p.stops != 1 {
				t.Fatal("unknown health changed candidate")
			}
		})
	}
}
func TestCancelBeforeStopAndRejectUncoordinatedRestart(t *testing.T) {
	h := newHarness(t)
	h.upgrade(t)
	if err := h.s.Cancel("upgrade-1"); err != nil {
		t.Fatal(err)
	}
	h.step(t)
	if h.p.stops != 0 {
		t.Fatal("cancelled update stopped service")
	}
	if err := h.s.Restart("normal-restart"); err == nil {
		t.Fatal("restart bypassed idle maintenance")
	}
	if h.p.starts != 0 || h.p.stops != 0 {
		t.Fatal("rejected restart disturbed service")
	}
}
func TestExclusiveSupervisorAndImmutableRequestIdentity(t *testing.T) {
	h := newHarness(t)
	if other, err := Open(h.cfg, h.opts); err == nil {
		_ = other.Close()
		t.Fatal("two supervisors acquired same instance")
	}
	h.upgrade(t)
	if err := h.s.Upgrade(context.Background(), "upgrade-1", makeBundle(t, []byte("different binary"))); err == nil {
		t.Fatal("same operation accepted different bundle")
	}
}

func TestHealthNeedsTwoDistinctServerHeartbeats(t *testing.T) {
	h := newHarness(t)
	h.upgrade(t)
	h.until(t, "checking")
	h.step(t)
	if h.s.Status().Operation.HealthCount != 1 {
		t.Fatal("first heartbeat was not counted")
	}
	h.a.sequence-- // The bridge returns the same accepted heartbeat again.
	h.step(t)
	if h.s.Status().Operation.HealthCount != 1 || h.s.Status().Operation.Phase != "checking" {
		t.Fatal("repeated heartbeat committed the candidate")
	}
	h.step(t)
	if h.s.Status().Operation.Phase != "committed" {
		t.Fatal("distinct second heartbeat did not commit")
	}
}
func TestGrantCannotSurviveDifferentBoot(t *testing.T) {
	h := newHarness(t)
	h.upgrade(t)
	h.step(t)
	h.s.opts.BootClock = func() (string, int64, error) { return "different-boot", 1000, nil }
	if err := h.s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.s.Status().Operation.Phase != "rolled_back" || h.p.starts != 0 {
		t.Fatal("stale grant authorized replacement")
	}
	if h.p.stops != 0 {
		t.Fatal("stale grant stopped the service")
	}
}

func TestDefinitePrelaunchFailureRecoversOldVersionWithoutGuessing(t *testing.T) {
	h := newHarness(t)
	h.upgrade(t)
	h.until(t, "backed_up")
	h.p.rejectStart = true
	if err := h.s.Step(context.Background()); !errors.Is(err, ErrNotStarted) {
		t.Fatalf("missing no-launch proof: %v", err)
	}
	h.reopen(t)
	h.until(t, "rolled_back")
	if h.p.starts != 1 || h.s.Status().Current.ReleaseID == "new-release" {
		t.Fatal("did not safely recover original version")
	}
}

func TestSequentialUpgradesUsePersistedCompatibilityAfterReopen(t *testing.T) {
	h := newHarness(t)
	h.upgrade(t)
	h.until(t, "committed")
	h.reopen(t)
	if h.s.Status().Current.Protocol != 2 || h.s.Status().Current.StorageVersion != 9 {
		t.Fatal("first release compatibility not durable")
	}
	dir := makeBundle(t, []byte("third executable"))
	path := filepath.Join(dir, "manifest.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest release.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.ReleaseID = "third-release"
	manifest.Protocol = release.Range{Min: 2, Max: 3}
	manifest.Storage.RunnerJournal = release.Range{Min: 9, Max: 10}
	raw, _ = json.Marshal(manifest)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.s.Upgrade(context.Background(), "upgrade-2", dir); err != nil {
		t.Fatal("static bootstrap compatibility rejected next release", err)
	}
	h.until(t, "committed")
	h.reopen(t)
	if h.s.Status().Current.Protocol != 3 || h.s.Status().Current.StorageVersion != 10 {
		t.Fatal("latest compatibility lost after reopen")
	}
	if len(h.p.releaseIDs) != 2 || h.p.releaseIDs[0] != "new-release" || h.p.releaseIDs[1] != "third-release" {
		t.Fatal("candidate release metadata did not follow selected version")
	}
	delete(h.p.live, h.s.Status().Process.PID)
	h.step(t)
	h.until(t, "committed")
	if h.p.releaseIDs[len(h.p.releaseIDs)-1] != "third-release" {
		t.Fatal("automatic recovery reused stale release metadata")
	}
}
func TestRollbackRestoresMatchingVersionCompatibility(t *testing.T) {
	h := newHarness(t)
	h.upgrade(t)
	h.until(t, "checking")
	h.a.unhealthy = true
	h.a.rollback = true
	h.now = h.now.Add(11 * time.Second)
	h.until(t, "rolled_back")
	h.reopen(t)
	if h.s.Status().Current.Protocol != 1 || h.s.Status().Current.StorageVersion != 8 {
		t.Fatal("rollback retained incompatible candidate metadata")
	}
	if h.p.releaseIDs[len(h.p.releaseIDs)-1] != h.s.Status().Current.ReleaseID {
		t.Fatal("rollback did not restore prior release metadata")
	}
}
