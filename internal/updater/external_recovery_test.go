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

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
)

type externalTestAuthority struct{}

func (a *externalTestAuthority) InstallationReadiness(_ context.Context, binding Binding) (protocol.InstallationReadiness, error) {
	return protocol.InstallationReadiness{Binding: binding, Ready: true, Protocol: 2, StorageVersion: 9}, nil
}
func (*externalTestAuthority) Authorize(context.Context, Binding) (Grant, error) {
	return Grant{}, errors.New("old server is unavailable")
}
func (*externalTestAuthority) Health(context.Context, Binding) (Health, error) {
	return Health{}, errors.New("old server is unavailable")
}

type externalTestManager struct {
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
	m.h.opts.Authority = &externalTestAuthority{}
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
	manager := &externalTestManager{h: h, unit: externalRecoveryUnit{Name: "fixture.service", Binary: filepath.Join(t.TempDir(), "updater"), Digest: strings.Repeat("b", 64)}}
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
			if record.BackupDir != "" {
				t.Fatal("binary-only recovery copied the ledger")
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
