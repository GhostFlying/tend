//go:build linux

package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
)

type recoveryAuthority struct {
	fakeAuthority
	p             *fakeProcess
	sequence      uint64
	fail, unready bool
	epoch         string
}

func (a *recoveryAuthority) RecoveryHealth(_ context.Context, p protocol.ForwardRecoveryPlan) (protocol.RecoveryHealth, error) {
	if a.fail {
		return protocol.RecoveryHealth{}, errors.New("transport failed")
	}
	a.sequence++
	var process Identity
	for _, id := range a.p.live {
		if id.LaunchID == p.Successor.ProcessInstanceID {
			process = id
		}
	}
	return protocol.RecoveryHealth{RecoveryID: p.RecoveryID, Binding: p.Successor, Process: process, Epoch: a.epoch, Sequence: a.sequence, Protocol: 2, Schema: 27, Registered: true, Fenced: true, Ready: !a.unready}, nil
}
func forwardFixture(t *testing.T) (*harness, protocol.ForwardRecoveryPlan, *recoveryAuthority) {
	t.Helper()
	h := newHarness(t)
	h.cfg.Role = "server"
	h.cfg.InstanceID = "server"
	h.cfg.StorageVersion = 26
	h.cfg.AuthoritySocket = filepath.Join(h.cfg.StateDir, "authority.sock")
	h.cfg.CoordinatorSocket = h.cfg.AuthoritySocket
	h.now = time.Now()
	if err := h.s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(h.cfg.StateDir, "state.json")); err != nil {
		t.Fatal(err)
	}
	a := &recoveryAuthority{p: h.p, epoch: "new-server-boot"}
	h.opts.Authority = a
	var err error
	h.s, err = Open(h.cfg, h.opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.s.Bootstrap(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	dir := makeBundle(t, []byte("successor executable"))
	raw, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
	var m release.Manifest
	_ = json.Unmarshal(raw, &m)
	m.Artifacts[0].Role = "server"
	raw, _ = json.Marshal(m)
	_ = os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o600)
	b, err := release.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	old := h.p.live[10]
	oldBinding := Binding{UpgradeID: "original-terminal-201", InstanceID: "server", ProcessInstanceID: old.LaunchID, ReleaseID: "old-release", ArtifactSHA256: old.SHA256, ManifestSHA256: strings.Repeat("a", 64)}
	candidate := h.s.state.Current
	candidate.ReleaseID = oldBinding.ReleaseID
	candidate.ManifestSHA256 = oldBinding.ManifestSHA256
	h.s.state.Operation = &Operation{ID: oldBinding.UpgradeID, Kind: "upgrade", Binding: oldBinding, Candidate: candidate, CandidateProcess: old, Phase: "checking", Irreversible: true, LastError: "old health false"}
	if err := h.s.save(); err != nil {
		t.Fatal(err)
	}
	handoff := ResourceHandoff{WindowClaim: "original-window", OperatorUID: os.Getuid(), ExpiresAtMillis: h.now.Add(20 * time.Minute).UnixMilli(), RunnerCoverageUntilMillis: h.now.Add(25 * time.Minute).UnixMilli(), WriterProcesses: []Identity{{PID: 2147483647, StartTicks: 1, LaunchID: "retained-writer", SHA256: strings.Repeat("b", 64)}}, Source: "isolated fixture", PendingOperationsReconciled: "fixture has none", WorkspaceResponsibility: "retained, fenced"}
	path := filepath.Join(t.TempDir(), "resource.json")
	raw, _ = json.Marshal(handoff)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	p := protocol.ForwardRecoveryPlan{LinkRequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHEX", RecoveryID: "forward-201", RequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHEW", OriginalRequestID: "req_01M6F01JSS6Q9BFV6DJNSSBHEV", Original: oldBinding, OriginalProcess: old, OriginalLedger: protocol.UpgradeView{UpgradeID: oldBinding.UpgradeID, ReleaseID: oldBinding.ReleaseID, Status: "completed", Phase: "completed", Revision: 8}, LedgerRevision: 8, ExpectedGeneration: h.s.state.Generation, StateSHA256: digest(h.s.state), ConfigSHA256: digest(h.s.cfg), ReleaseDir: dir, Successor: Binding{UpgradeID: "forward-201", InstanceID: "server", ProcessInstanceID: "successor-launch", ReleaseID: b.Manifest.ReleaseID, ManifestSHA256: b.ManifestSHA256, ArtifactSHA256: b.Manifest.Artifacts[0].SHA256}, OperatorUID: os.Getuid(), WindowClaim: handoff.WindowClaim, ResourceEvidence: path, ResourceSHA256: bytesDigest(raw), ExpiresAtMillis: h.now.Add(10 * time.Minute).UnixMilli()}
	return h, p, a
}
func authorizeForward(t *testing.T, h *harness, p protocol.ForwardRecoveryPlan) {
	t.Helper()
	if err := h.s.AdoptRecovery(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	h.reopen(t)
	if err := h.s.AuthorizeRecovery(context.Background(), p, h.s.Status().Generation); err != nil {
		t.Fatal(err)
	}
}
func TestForwardRecoveryPreservesUnconfirmedOriginalAndAcceptedData(t *testing.T) {
	h, p, _ := forwardFixture(t)
	original := h.s.Status()
	authorizeForward(t, h, p)
	accepted := []byte("accepted business data must survive")
	if err := os.WriteFile(h.data, accepted, 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		h.step(t)
		h.reopen(t)
	}
	state := h.s.Status()
	r := state.Recovery
	if r.Phase != "successor_committed" || r.Receipt == nil || state.Current != r.Target || state.CommittedProof.Binding != p.Successor {
		t.Fatalf("missing atomic forward commit: %+v", r)
	}
	if !reflect.DeepEqual(state.Operation, original.Operation) || len(state.Terminals) != len(original.Terminals) {
		t.Fatal("original unconfirmed history was rewritten")
	}
	if CommittedReceipt(state) != nil {
		t.Fatal("forward receipt became legacy receipt")
	}
	if err := h.s.LinkRecovery(*r.Receipt, state.Generation); err != nil {
		t.Fatal(err)
	}
	if err := h.s.LinkRecovery(*r.Receipt, state.Generation); err != nil {
		t.Fatal(err)
	}
	if h.s.Status().Operation != nil || len(h.s.Status().Recoveries) != 1 {
		t.Fatal("unfinished reservation not resolved with retained archive")
	}
	if err := h.s.Upgrade(context.Background(), p.Original.UpgradeID, p.ReleaseDir); err == nil {
		t.Fatal("old identity reused as new start")
	}
	if h.p.starts != 1 || h.p.stops != 1 {
		t.Fatalf("process effects repeated: %d/%d", h.p.starts, h.p.stops)
	}
	data, _ := os.ReadFile(h.data)
	if string(data) != string(accepted) {
		t.Fatal("accepted data rolled back")
	}
}
func TestForwardRecoveryReconcilesUnknownStartWithoutResend(t *testing.T) {
	h, p, _ := forwardFixture(t)
	authorizeForward(t, h, p)
	h.step(t)
	h.p.ambiguousStart = true
	if err := h.s.Step(context.Background()); !errors.Is(err, ErrUnknown) {
		t.Fatalf("want unknown got %v", err)
	}
	h.reopen(t)
	h.step(t)
	h.step(t)
	h.step(t)
	if h.p.starts != 1 || h.p.stops != 1 || h.s.Status().Recovery.Phase != "successor_committed" {
		t.Fatal("unknown start was reissued or not adopted")
	}
}
func TestForwardRecoveryRejectsCASReplayAndReadinessFailures(t *testing.T) {
	h, p, a := forwardFixture(t)
	if err := h.s.AdoptRecovery(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	state := h.s.Status()
	if err := h.s.AuthorizeRecovery(context.Background(), p, state.Generation-1); err == nil {
		t.Fatal("stale generation accepted")
	}
	conflicting := p
	conflicting.WindowClaim = "other"
	if err := h.s.AuthorizeRecovery(context.Background(), conflicting, state.Generation); err == nil {
		t.Fatal("conflicting replay accepted")
	}
	if err := h.s.AuthorizeRecovery(context.Background(), p, state.Generation); err != nil {
		t.Fatal(err)
	}
	h.step(t)
	h.step(t)
	h.step(t) // first healthy sample
	a.fail = true
	if err := h.s.Step(context.Background()); err == nil {
		t.Fatal("transport failure counted")
	}
	a.fail = false
	if h.s.Status().Recovery.HealthCount != 0 {
		t.Fatal("transport failure retained health count")
	}
	h.step(t)
	a.unready = true
	if err := h.s.Step(context.Background()); err == nil {
		t.Fatal("unready counted")
	}
	a.unready = false
	h.step(t)
	h.step(t)
	if h.s.Status().Recovery.Phase != "successor_committed" {
		t.Fatal("two real later samples did not commit")
	}
}
func TestForwardRecoveryDurabilityFailureFencesProcessEffects(t *testing.T) {
	h, p, _ := forwardFixture(t)
	authorizeForward(t, h, p)
	h.s.writeState = func(string, State) error { return errors.New("injected fsync failure") }
	if err := h.s.Step(context.Background()); err == nil {
		t.Fatal("lost intent durability ignored")
	}
	if err := h.s.Step(context.Background()); err == nil {
		t.Fatal("poisoned writer continued")
	}
	if h.p.starts != 0 || h.p.stops != 0 || h.s.Status().Recovery.Phase != "authorized" {
		t.Fatal("process effect before durable intent")
	}
	h.reopen(t)
	h.step(t)
	if h.p.stops != 1 {
		t.Fatal("reopened durable authorization did not proceed")
	}
}

func TestForwardRecoveryRenameAcknowledgementLossDoesNotRepeatEffects(t *testing.T) {
	h, p, _ := forwardFixture(t)
	authorizeForward(t, h, p)
	h.step(t)
	h.step(t)
	h.step(t)
	if h.s.Status().Recovery.HealthCount != 1 {
		t.Fatal("expected first readiness sample")
	}
	h.s.writeState = func(path string, state State) error {
		if err := writeJSON(path, state); err != nil {
			return err
		}
		return errors.New("injected loss after rename/directory sync")
	}
	if err := h.s.Step(context.Background()); err == nil {
		t.Fatal("lost acknowledgement ignored")
	}
	if h.s.Status().Recovery.Receipt != nil {
		t.Fatal("unacknowledged write exposed as durable")
	}
	h.reopen(t)
	state := h.s.Status()
	if state.Recovery.Phase != "successor_committed" || state.Recovery.Receipt == nil {
		t.Fatal("reopen did not reconcile actual committed file")
	}
	h.step(t)
	if h.p.starts != 1 || h.p.stops != 1 {
		t.Fatal("receipt reconciliation repeated process effects")
	}
}

type unknownRecoveryStop struct {
	*fakeProcess
	calls int
}

func (p *unknownRecoveryStop) Stop(context.Context, Identity, time.Duration) error {
	p.calls++
	return ErrUnknown
}
func TestForwardRecoveryUnknownStopCannotSignalAgainOrLaunch(t *testing.T) {
	h, plan, _ := forwardFixture(t)
	authorizeForward(t, h, plan)
	p := &unknownRecoveryStop{fakeProcess: h.p}
	h.s.opts.Process = p
	h.opts.Process = p
	if err := h.s.Step(context.Background()); !errors.Is(err, ErrUnknown) {
		t.Fatal(err)
	}
	h.reopen(t)
	for i := 0; i < 3; i++ {
		if err := h.s.Step(context.Background()); !errors.Is(err, ErrUnknown) {
			t.Fatal("unknown stop was guessed", err)
		}
	}
	if p.calls != 1 || h.p.starts != 0 {
		t.Fatal("unknown stop repeated signal or launched")
	}
	delete(h.p.live, plan.OriginalProcess.PID)
	h.step(t)
	h.step(t)
	if p.calls != 1 || h.p.starts != 1 {
		t.Fatal("independently proved exit did not allow single forward launch")
	}
}

func TestForwardRecoveryHealthCannotCombineEpochsOrRepeatedSequences(t *testing.T) {
	h, p, a := forwardFixture(t)
	authorizeForward(t, h, p)
	h.step(t)
	h.step(t)
	h.step(t)
	a.sequence = 0
	if err := h.s.Step(context.Background()); err == nil {
		t.Fatal("repeated sequence counted")
	}
	if h.s.Status().Recovery.HealthCount != 0 {
		t.Fatal("duplicate sequence did not clear count")
	}
	h.step(t)
	a.epoch = "different-server-epoch"
	if err := h.s.Step(context.Background()); err == nil {
		t.Fatal("different epoch combined with prior sample")
	}
	if h.s.Status().Recovery.Receipt != nil {
		t.Fatal("wrong epoch produced receipt")
	}
	h.step(t)
	h.step(t)
	if h.s.Status().Recovery.Receipt == nil || h.s.Status().Recovery.Receipt.Epoch != a.epoch {
		t.Fatal("two independent new epoch samples did not commit")
	}
}

func TestForwardRecoveryCompatibilityUsesMigratedRunningCandidate(t *testing.T) {
	for _, support := range []bool{false, true} {
		t.Run(strconv.FormatBool(support), func(t *testing.T) {
			h, p, _ := forwardFixture(t)
			h.s.state.Operation.Candidate.StorageVersion = 27
			h.s.state.Operation.Candidate.Protocol = 2
			if err := h.s.save(); err != nil {
				t.Fatal(err)
			}
			p.StateSHA256 = digest(h.s.state)
			raw, _ := os.ReadFile(filepath.Join(p.ReleaseDir, "manifest.json"))
			var m release.Manifest
			_ = json.Unmarshal(raw, &m)
			if support {
				m.Storage.ServerSchema = release.Range{Min: 27, Max: 27}
				m.Protocol = release.Range{Min: 2, Max: 3}
			} else {
				m.Storage.ServerSchema = release.Range{Min: 26, Max: 26}
			}
			if err := writeJSON(filepath.Join(p.ReleaseDir, "manifest.json"), m); err != nil {
				t.Fatal(err)
			}
			b, err := release.Load(p.ReleaseDir)
			if err != nil {
				t.Fatal(err)
			}
			p.Successor.ManifestSHA256 = b.ManifestSHA256
			err = h.s.AdoptRecovery(context.Background(), p)
			if support && err != nil {
				t.Fatal("valid successor rejected by stale Current", err)
			}
			if !support && err == nil {
				t.Fatal("candidate unable to read accepted schema was adopted")
			}
			if h.p.stops != 0 || h.p.starts != 0 {
				t.Fatal("compatibility check disturbed live process")
			}
		})
	}
}

func TestForwardRecoveryAuthorizationUsesBootDeadline(t *testing.T) {
	for _, changedBoot := range []bool{false, true} {
		t.Run(strconv.FormatBool(changedBoot), func(t *testing.T) {
			h, p, _ := forwardFixture(t)
			authorizeForward(t, h, p)
			if changedBoot {
				h.opts.BootClock = func() (string, int64, error) { return "different-boot", h.boot, nil }
				h.reopen(t)
			} else {
				h.boot += (20 * time.Minute).Milliseconds()
			}
			if err := h.s.Step(context.Background()); err == nil {
				t.Fatal("expired monotonic/boot authorization allowed a stop")
			}
			if h.p.stops != 0 || h.p.starts != 0 {
				t.Fatal("old authorization was reused after clock rollback/reboot")
			}
		})
	}
}

func TestPinnedUpdaterCopyRejectsRebuiltCandidateBeforePublication(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "candidate")
	target := filepath.Join(dir, "fixed-updater")
	if err := os.WriteFile(source, []byte("reviewed updater"), 0o700); err != nil {
		t.Fatal(err)
	}
	expected, err := hashFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("original updater"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("rebuilt after preflight"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := copyPinnedAtomic(source, target, 0o755, expected); err == nil {
		t.Fatal("rebuilt candidate was published")
	}
	raw, err := os.ReadFile(target)
	if err != nil || string(raw) != "original updater" {
		t.Fatal("digest failure replaced original fixed updater", err, string(raw))
	}
}
func TestHandoffRejectsPollerStateDriftAfterStop(t *testing.T) {
	h, p, _ := forwardFixture(t)
	h.s.state.Operation.LastError = "poll completed while manager stopped updater"
	if err := h.s.save(); err != nil {
		t.Fatal(err)
	}
	if err := h.s.Close(); err != nil {
		t.Fatal(err)
	}
	owner, err := lockDir(h.cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if err := validateStoppedSnapshot(h.cfg, HandoffPlan{Recovery: p}); err == nil {
		t.Fatal("stale pre-stop snapshot passed under the original flock")
	}
	if h.p.stops != 0 || h.p.starts != 0 {
		t.Fatal("stale installation guard affected the child")
	}
}

func TestForwardRecoveryRequiresJournalOnFormatTwoRestart(t *testing.T) {
	h, p, _ := forwardFixture(t)
	authorizeForward(t, h, p)
	h.s.cfg.CoordinatorSocket = "changed-after-adoption"
	if err := AdoptPendingHandoff(t.Context(), h.s); !errors.Is(err, ErrUnknown) {
		t.Fatalf("format-2 restart accepted missing mandatory journal: %v", err)
	}
	if h.p.stops != 0 || h.p.starts != 0 {
		t.Fatal("journal validation affected the retained process")
	}
}

func TestForwardRecoveryHistoricalWriterHashUnknownStillRequiresExit(t *testing.T) {
	h, p, _ := forwardFixture(t)
	raw, err := os.ReadFile(p.ResourceEvidence)
	if err != nil {
		t.Fatal(err)
	}
	var evidence ResourceHandoff
	if err := json.Unmarshal(raw, &evidence); err != nil {
		t.Fatal(err)
	}
	writeEvidence := func() {
		t.Helper()
		raw, err := json.Marshal(evidence)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p.ResourceEvidence, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		p.ResourceSHA256 = bytesDigest(raw)
	}
	evidence.WriterProcesses[0].SHA256 = ""
	writeEvidence()
	if err := verifyResourceEvidence(p, h.now); err != nil {
		t.Fatal("unknown historical hash blocked proven exited writer", err)
	}
	if validIdentity(evidence.WriterProcesses[0]) {
		t.Fatal("unknown historical hash became a valid live service identity")
	}
	evidence.WriterProcesses[0].SHA256 = "unknown"
	writeEvidence()
	if err := verifyResourceEvidence(p, h.now); err == nil {
		t.Fatal("malformed supplied historical hash was accepted")
	}
	actual, err := (LinuxProcess{}).Inspect(t.Context(), os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	evidence.WriterProcesses[0].SHA256 = ""
	evidence.WriterProcesses[0].PID = actual.PID
	evidence.WriterProcesses[0].StartTicks = actual.StartTicks
	writeEvidence()
	if err := verifyResourceEvidence(p, h.now); err == nil {
		t.Fatal("empty hash bypassed an actually running writer")
	}
}

func TestForwardRecoveryRemoteWriterFencePinsOperatorEvidence(t *testing.T) {
	h, p, _ := forwardFixture(t)
	raw, err := os.ReadFile(p.ResourceEvidence)
	if err != nil {
		t.Fatal(err)
	}
	var evidence ResourceHandoff
	if err := json.Unmarshal(raw, &evidence); err != nil {
		t.Fatal(err)
	}
	// Remote evidence deliberately has the current process's PID. It must not
	// be interpreted as a local kernel identity.
	remote := []byte(fmt.Sprintf(`{"pid":%d,"exited":true,"observation":"remote kernel"}`, os.Getpid()))
	path := filepath.Join(t.TempDir(), "remote.json")
	if err := os.WriteFile(path, remote, 0o600); err != nil {
		t.Fatal(err)
	}
	evidence.WriterProcesses = nil
	evidence.RemoteWriterFence = &RemoteWriterFence{Host: "remote-worker", Path: path, SHA256: bytesDigest(remote), Source: "authenticated remote close and two exit observations reviewed by operator"}
	write := func() {
		t.Helper()
		raw, err := json.Marshal(evidence)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p.ResourceEvidence, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		p.ResourceSHA256 = bytesDigest(raw)
	}
	write()
	if err := verifyResourceEvidence(p, h.now); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"host", "source", "sha256"} {
		saved := *evidence.RemoteWriterFence
		switch field {
		case "host":
			evidence.RemoteWriterFence.Host = ""
		case "source":
			evidence.RemoteWriterFence.Source = ""
		case "sha256":
			evidence.RemoteWriterFence.SHA256 = strings.Repeat("a", 64)
		}
		write()
		if err := verifyResourceEvidence(p, h.now); err == nil {
			t.Fatalf("accepted invalid remote %s", field)
		}
		*evidence.RemoteWriterFence = saved
	}
	evidence.RemoteWriterFence = nil
	write()
	if err := verifyResourceEvidence(p, h.now); err == nil {
		t.Fatal("accepted no writer evidence")
	}
}
