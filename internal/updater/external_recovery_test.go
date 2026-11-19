//go:build linux

package updater

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
)

type externalTestAuthority struct{ version int64 }

func (a *externalTestAuthority) InstallationReadiness(_ context.Context, binding Binding) (protocol.InstallationReadiness, error) {
	return protocol.InstallationReadiness{Binding: binding, Ready: true, Protocol: a.version, StorageVersion: 9}, nil
}

type restartingExternalManager struct{ *externalTestManager }

func (m *restartingExternalManager) Start(ctx context.Context, unit externalRecoveryUnit, config string) error {
	if err := m.externalTestManager.Start(ctx, unit, config); err != nil {
		return err
	}
	state := m.h.s.Status()
	delete(m.h.p.live, state.Process.PID)
	for range 3 {
		if err := m.h.s.Step(ctx); err != nil {
			return err
		}
	}
	return nil
}

func TestExternalRecoveryCompletesAfterCommittedProcessRestart(t *testing.T) {
	h, request, base := externalFixture(t)
	manager := &restartingExternalManager{base}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	record, err := externalRecovery(ctx, h.cfg, request, manager)
	state := h.s.Status()
	proof := CommittedReceipt(state)
	if proof == nil || !proof.ExternalRecovery || proof.Binding.UpgradeID != request.RequestID || state.Process.LaunchID == proof.Binding.ProcessInstanceID {
		t.Fatal("restart lost the original installation proof", proof)
	}
	if err != nil || record.Phase != "complete" || base.stops != 1 || base.starts != 1 {
		t.Fatalf("committed recovery did not reconcile after restart: record=%+v err=%v stops=%d starts=%d", record, err, base.stops, base.starts)
	}
}
func (*externalTestAuthority) Authorize(context.Context, Binding) (Grant, error) {
	return Grant{}, errors.New("old server is unavailable")
}
func (*externalTestAuthority) Health(context.Context, Binding) (Health, error) {
	return Health{}, errors.New("old server is unavailable")
}

type externalTestManager struct {
	version             int64
	h                   *harness
	unit                externalRecoveryUnit
	stops, starts       int
	loseStart, failStop bool
}

func (m *externalTestManager) Inspect(context.Context, string, string) (externalRecoveryUnit, error) {
	return m.unit, nil
}
func (m *externalTestManager) Stop(context.Context, externalRecoveryUnit, string) error {
	m.stops++
	if m.failStop {
		return errors.New("could not prove old writers exited")
	}
	_ = m.h.s.Close()
	m.h.p.live = map[int]Identity{}
	return nil
}
func (m *externalTestManager) Start(ctx context.Context, _ externalRecoveryUnit, _ string) error {
	if m.starts > 0 {
		return nil
	} // systemd start is idempotent for an active unit.
	m.starts++
	m.h.opts.Authority = &externalTestAuthority{version: m.version}
	supervisor, err := Open(m.h.cfg, m.h.opts)
	if err != nil {
		return err
	}
	m.h.s = supervisor
	handled, err := AdoptSelfUpdate(ctx, supervisor)
	if err != nil {
		return err
	}
	if !handled {
		if err := AdoptPendingHandoff(ctx, supervisor); err != nil {
			return err
		}
	}
	for range 5 {
		if supervisor.Status().Operation.Phase == "committed" {
			break
		}
		if err := supervisor.Step(ctx); err != nil {
			return err
		}
	}
	if m.loseStart {
		return errors.New("start reply lost")
	}
	return nil
}

func externalFixture(t *testing.T) (*harness, ExternalRecoveryRequest, *externalTestManager) {
	t.Helper()
	return externalFixtureProtocol(t, 2)
}
func externalFixtureProtocol(t *testing.T, version int64) (*harness, ExternalRecoveryRequest, *externalTestManager) {
	t.Helper()
	h := newHarness(t)
	h.cfg.AuthoritySocket = filepath.Join(t.TempDir(), "unavailable-old-service.sock")
	h.cfg.StorageVersion = 9
	config := filepath.Join(t.TempDir(), "updater.json")
	if err := writeJSON(config, h.cfg); err != nil {
		t.Fatal(err)
	}
	bundle, err := release.Load(h.bundle)
	if err != nil {
		t.Fatal(err)
	}
	m := bundle.Manifest
	m.Protocol.Max = version
	artifacts := append([]release.Artifact{}, m.Artifacts...)
	for _, role := range []string{"server", "updater", "cli"} {
		a := m.Artifacts[0]
		a.Role, a.Path = role, role
		if err := copyAtomic(filepath.Join(h.bundle, "binary"), filepath.Join(h.bundle, role), 0755); err != nil {
			t.Fatal(err)
		}
		artifacts = append(artifacts, a)
		if role == "server" {
			m.Artifacts = append(m.Artifacts, a)
		}
	}
	if err := writeJSON(filepath.Join(h.bundle, "manifest.json"), m); err != nil {
		t.Fatal(err)
	}
	bundle, err = release.Load(h.bundle)
	if err != nil {
		t.Fatal(err)
	}
	set := release.Set{Format: 2, ReleaseID: m.ReleaseID, Commit: strings.Repeat("a", 40), OS: "linux", Arch: m.Artifacts[0].Arch, ManifestSHA256: bundle.ManifestSHA256, Artifacts: artifacts}
	if err := writeJSON(filepath.Join(h.bundle, "release-set.json"), set); err != nil {
		t.Fatal(err)
	}
	manager := &externalTestManager{version: version, h: h, unit: externalRecoveryUnit{Name: "fixture.service", Binary: filepath.Join(t.TempDir(), "updater"), Digest: strings.Repeat("b", 64)}}
	request := ExternalRecoveryRequest{Release: h.bundle, ConfigFile: config, Force: true, RequestID: "external-fixture"}
	return h, request, manager
}

func TestExternalRecoveryUsesNewSupervisorWithUnreadableOldState(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(map[bool]string{false: "unreadable", true: "lost_start_reply"}[lost], func(t *testing.T) {
			h, request, manager := externalFixture(t)
			manager.loseStart = lost
			original := []byte("broken updater state; original facts remain unknown")
			if err := os.WriteFile(h.s.statePath(), original, 0600); err != nil {
				t.Fatal(err)
			}
			record, err := externalRecovery(t.Context(), h.cfg, request, manager)
			if lost {
				if err == nil || record.Phase != "installed" {
					t.Fatal(record, err)
				}
				record, err = externalRecovery(t.Context(), h.cfg, request, manager)
			}
			if err != nil || record.Phase != "complete" {
				t.Fatal(record, err)
			}
			archived, err := os.ReadFile(filepath.Join(record.Archive, "state.json"))
			if err != nil || string(archived) != string(original) {
				t.Fatal("original evidence changed", err)
			}
			if record.BackupDir == "" {
				t.Fatal("unknown installed schema was treated as a proven binary-only replacement")
			}
			state := h.s.Status()
			if state.Operation.Kind != "recover" || state.Operation.Phase != "committed" || h.p.starts != 1 || manager.stops != 1 || manager.starts != 1 {
				t.Fatal("recovery was not committed exactly once", state.Operation)
			}
			request.Status, request.Force = true, false
			status, err := externalRecovery(t.Context(), h.cfg, request, manager)
			if err != nil || status.Phase != "complete" || manager.stops != 1 {
				t.Fatal(status, err)
			}
			if _, err := json.Marshal(state); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestExternalRecoveryDoesNotInstallWithoutExitProof(t *testing.T) {
	h, request, manager := externalFixture(t)
	manager.failStop = true
	before, err := os.ReadFile(h.s.statePath())
	if err != nil {
		t.Fatal(err)
	}
	record, err := externalRecovery(t.Context(), h.cfg, request, manager)
	if err == nil || record.Phase != "stopping" || manager.starts != 0 {
		t.Fatal(record, err)
	}
	after, err := os.ReadFile(h.s.statePath())
	if err != nil || string(after) != string(before) {
		t.Fatal("state replaced before original writers stopped", err)
	}
}

func TestExternalRecoveryArchivesUnreadableStartupJournals(t *testing.T) {
	for _, name := range []string{"self-update/journal.json", "self-update.json", "handoff.json", "committed-handoff.json"} {
		t.Run(name, func(t *testing.T) {
			h, request, manager := externalFixture(t)
			path := filepath.Join(h.cfg.StateDir, name)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			original := []byte("unreadable prior updater journal")
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			record, err := externalRecovery(t.Context(), h.cfg, request, manager)
			if err != nil || record.Phase != "complete" {
				t.Fatal("recovery could not pass ordinary startup adoption", record, err)
			}
			raw, err := os.ReadFile(filepath.Join(record.Archive, name))
			if err != nil || string(raw) != string(original) {
				t.Fatal("original startup journal was not retained", err)
			}
		})
	}
}

func TestExternalRecoveryRejectsInvalidConfigurationBeforeStopping(t *testing.T) {
	h, request, manager := externalFixture(t)
	cfg := h.cfg
	cfg.Role = "server"
	cfg.ServiceConfig = request.ConfigFile
	original, err := os.ReadFile(filepath.Join(cfg.StateDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := externalRecovery(t.Context(), cfg, request, manager); err == nil {
		t.Fatal("invalid supervisor configuration was accepted")
	}
	after, err := os.ReadFile(filepath.Join(cfg.StateDir, "state.json"))
	if err != nil || string(original) != string(after) || manager.stops != 0 || manager.starts != 0 {
		t.Fatal("configuration error crossed the stop boundary", err)
	}
}

func TestExternalRecoveryUsesInstalledStorageInsteadOfBootstrapConfig(t *testing.T) {
	for _, storage := range []int64{0, 8, 9} {
		t.Run(map[int64]string{0: "unknown", 8: "migration", 9: "binary-only"}[storage], func(t *testing.T) {
			h, request, manager := externalFixture(t)
			h.cfg.StorageVersion = 7
			if err := writeJSON(request.ConfigFile, h.cfg); err != nil {
				t.Fatal(err)
			}
			if storage > 0 {
				state := h.s.Status()
				state.Current.StorageVersion = storage
				if err := writeJSON(h.s.statePath(), state); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(h.s.statePath(), []byte("unreadable old state"), 0600); err != nil {
				t.Fatal(err)
			}
			record, err := externalRecovery(t.Context(), h.cfg, request, manager)
			if err != nil || record.Phase != "complete" {
				t.Fatal(record, err)
			}
			if (record.BackupDir == "") != (storage == 9) {
				t.Fatal("snapshot choice did not preserve unknown installed storage", record)
			}
		})
	}
}

func TestExternalRecoveryPreservesRollingJournal(t *testing.T) {
	h, request, manager := externalFixtureProtocol(t, 6)
	auth := &retirementAuthority{fakeAuthority: h.a, ready: map[string]bool{}, confirmed: map[string]int{}}
	h.opts.RollingAuthority, h.s.opts.RollingAuthority = auth, auth
	if err := h.s.criticalState(); err != nil {
		t.Fatal(err)
	}
	h.s.state.Format = rollingStateFormat
	h.s.state.CurrentJournal = "original-accepting-generation"
	h.s.state.RetiredJournals = []string{""}
	if err := h.s.save(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	record, err := externalRecovery(ctx, h.cfg, request, manager)
	if err != nil {
		t.Fatal(record, err)
	}
	if len(h.s.Status().RetiredJournals) != 1 || h.s.Status().RetiredJournals[0] != "" || h.s.Status().CurrentJournal != "original-accepting-generation" || len(h.p.journalIDs) != 1 || h.p.journalIDs[0] != "original-accepting-generation" {
		t.Fatalf("external recovery silently discarded generation ownership: phase=%s journal=%q launch_journals=%q retired=%q", record.Phase, h.s.Status().CurrentJournal, h.p.journalIDs, h.s.Status().RetiredJournals)
	}
}

func TestExternalRecoveryProtectsUnresolvedRollingOwnershipBeforeStop(t *testing.T) {
	for _, scenario := range []string{"draining", "unreadable"} {
		t.Run(scenario, func(t *testing.T) {
			h, request, manager := externalFixtureProtocol(t, 6)
			if err := h.s.criticalState(); err != nil {
				t.Fatal(err)
			}
			h.s.state.Format, h.s.state.CurrentJournal = rollingStateFormat, "accepting"
			h.s.state.DrainingProcesses = map[string]DrainingProcess{"old": {Process: h.s.state.Process}}
			if err := h.s.save(); err != nil {
				t.Fatal(err)
			}
			if scenario == "unreadable" {
				if err := atomicWrite(filepath.Join(h.cfg.StateDir, rollingOwnershipMarker), []byte("1\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := atomicWrite(h.s.statePath(), []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := externalRecovery(t.Context(), h.cfg, request, manager); err == nil || manager.stops != 0 {
				t.Fatal("unresolved writer ownership permitted a whole-unit stop", err, manager.stops)
			}
		})
	}
}

func TestEmergencyPrimaryRecoversCompatibleReleaseAfterConfirmedExit(t *testing.T) {
	h, request, manager := externalFixtureProtocol(t, 6)
	original := h.s.Status().Process
	h.s.state.EmergencyPrimary = &DrainingProcess{WindowID: "window", Process: original, Stopped: true, AuthorizationID: "stop"}
	if err := h.s.savePredecessor(*h.s.state.EmergencyPrimary); err != nil {
		t.Fatal(err)
	}
	if _, err := externalRecovery(t.Context(), h.cfg, request, manager); err == nil || manager.stops != 0 {
		t.Fatal("recovery passed an unconfirmed exit", err)
	}
	h.s.state.EmergencyPrimary.Confirmed = true
	if err := h.s.save(); err != nil {
		t.Fatal(err)
	}
	record, err := externalRecovery(t.Context(), h.cfg, request, manager)
	if err != nil || record.Phase != "complete" || h.s.Status().Process == original || h.s.Status().EmergencyPrimary != nil {
		t.Fatal(record, err)
	}
}
