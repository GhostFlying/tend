//go:build linux

package updater

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestSelfBootstrapFenceBlocksStateWritesAndPreservesJournalRecovery(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("legacy permission fence explicitly requires a non-root writer")
	}
	cfg := Config{StateDir: t.TempDir(), InstanceID: "fixture"}
	p := SelfUpdatePlan{Updater: Identity{PID: os.Getpid()}, Child: Identity{PID: 42}}
	if err := captureSelfStateDirectory(cfg, &p); err != nil {
		t.Fatal(err)
	}
	state := State{Format: 1, InstanceID: cfg.InstanceID, Process: p.Child}
	if err := writeJSON(filepath.Join(cfg.StateDir, "state.json"), state); err != nil {
		t.Fatal(err)
	}
	j := SelfUpdateJournal{Format: 1, Phase: "prepared", Plan: p}
	if err := writeSelf(cfg, j); err != nil {
		t.Fatal(err)
	}
	inflight, err := os.CreateTemp(cfg.StateDir, ".old-writer-")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inflight.WriteString(`{"operation":"pending"}`); err != nil {
		t.Fatal(err)
	}
	if err := inflight.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := fenceSelfStateDirectory(cfg, p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.restore() })
	if err := os.Rename(inflight.Name(), filepath.Join(cfg.StateDir, "state.json")); err == nil {
		t.Fatal("an in-flight old writer renamed across the directory fence")
	}
	state.Operation = &Operation{ID: "intervening-service-operation", Phase: "staged"}
	if err := writeJSON(filepath.Join(cfg.StateDir, "state.json"), state); err == nil {
		t.Fatal("old writer committed a service operation across the fence")
	}
	j.Phase = "stopping"
	if err := writeSelf(cfg, j); err != nil {
		t.Fatal("handoff intent could not commit while the legacy writer was fenced", err)
	}
	if _, err := selfServiceState(cfg, p); err != nil {
		t.Fatal("failed external write changed durable ownership", err)
	}
	// A fresh installer can recover the original directory mode using only the
	// durable plan, even when its predecessor exited with the fence still set.
	recovered, err := fenceSelfStateDirectory(cfg, p)
	if err != nil {
		t.Fatal(err)
	}
	if err := recovered.restore(); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(cfg.StateDir, "state.json"), state); err != nil {
		t.Fatal(err)
	}
	if _, err := selfServiceState(cfg, p); err == nil {
		t.Fatal("stop admission ignored an operation staged after plan preparation")
	}
	if info, err := os.Stat(cfg.StateDir); err != nil || info.Mode().Perm() != os.FileMode(p.StateDirectoryMode) {
		t.Fatal("original permissions were not restored", info, err)
	}
}

func TestSelfBootstrapFenceRejectsDirectBusinessState(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("legacy permission fence explicitly requires a non-root writer")
	}
	cfg := Config{StateDir: t.TempDir()}
	cfg.DataFiles = []string{filepath.Join(cfg.StateDir, "business.db")}
	p := SelfUpdatePlan{Updater: protocol.RecoveryIdentity{PID: os.Getpid()}}
	if err := captureSelfStateDirectory(cfg, &p); err == nil {
		t.Fatal("bootstrap could fence direct business data")
	}
	alias := filepath.Join(t.TempDir(), "state-alias")
	if err := os.Symlink(cfg.StateDir, alias); err != nil {
		t.Fatal(err)
	}
	cfg.DataFiles = []string{filepath.Join(alias, "business.db")}
	if err := captureSelfStateDirectory(cfg, &p); err == nil {
		t.Fatal("bootstrap could fence business data through a directory alias")
	}
}
