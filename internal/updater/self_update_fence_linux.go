//go:build linux

package updater

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func ensureSelfJournalDirectory(cfg Config) error {
	path := filepath.Dir(selfPath(cfg))
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 || !ownedFile(info) {
		return errors.New("updater: private self-update journal directory required")
	}
	// Persist the new directory entry before an intent in it authorizes effects.
	return syncDir(cfg.StateDir)
}

func captureSelfStateDirectory(cfg Config, p *SelfUpdatePlan) error {
	if os.Geteuid() == 0 {
		return errors.New("updater: legacy bootstrap requires an unprivileged state writer")
	}
	info, err := os.Lstat(cfg.StateDir)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || !ownedFile(info) || info.Mode().Perm()&0o700 != 0o700 || info.Mode().Perm()&0o022 != 0 {
		return errors.New("updater: legacy bootstrap requires an owned state directory")
	}
	for _, file := range cfg.DataFiles {
		parent, err := os.Stat(filepath.Dir(file))
		if err != nil {
			return err
		}
		if os.SameFile(info, parent) {
			return errors.New("updater: business files cannot share the fenced state directory")
		}
	}
	status, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(p.Updater.PID), "status"))
	if err != nil {
		return err
	}
	var capabilities uint64
	found := false
	for _, line := range strings.Split(string(status), "\n") {
		if strings.HasPrefix(line, "CapEff:") {
			capabilities, err = strconv.ParseUint(strings.TrimSpace(strings.TrimPrefix(line, "CapEff:")), 16, 64)
			found = err == nil
		}
	}
	if !found || capabilities&(1<<1) != 0 {
		return errors.New("updater: legacy writer can bypass the state write fence")
	}
	p.StateDirectoryDevice, p.StateDirectoryInode, p.StateDirectoryMode = uint64(st.Dev), st.Ino, uint32(info.Mode()&(0o777|os.ModeSetuid|os.ModeSetgid|os.ModeSticky))
	return nil
}

type selfStateDirectoryFence struct {
	cfg      Config
	plan     SelfUpdatePlan
	restored bool
}

func (f *selfStateDirectoryFence) info() (os.FileInfo, error) {
	info, err := os.Lstat(f.cfg.StateDir)
	if err != nil {
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || !ownedFile(info) || uint64(st.Dev) != f.plan.StateDirectoryDevice || st.Ino != f.plan.StateDirectoryInode || f.plan.StateDirectoryMode&0o700 != 0o700 || f.plan.StateDirectoryMode&0o022 != 0 {
		return nil, errors.New("updater: fenced state directory identity changed")
	}
	mode := info.Mode() & (0o777 | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
	if mode != os.FileMode(f.plan.StateDirectoryMode) && mode != os.FileMode(f.plan.StateDirectoryMode)&^0o222 {
		return nil, errors.New("updater: fenced state directory permissions changed")
	}
	return info, nil
}

func fenceSelfStateDirectory(cfg Config, p SelfUpdatePlan) (*selfStateDirectoryFence, error) {
	f := &selfStateDirectoryFence{cfg: cfg, plan: p}
	if _, err := f.info(); err != nil {
		return nil, err
	}
	if err := os.Chmod(cfg.StateDir, os.FileMode(p.StateDirectoryMode)&^0o222); err != nil {
		return nil, err
	}
	if err := syncDir(cfg.StateDir); err != nil {
		return nil, errors.Join(err, f.restore())
	}
	return f, nil
}

func (f *selfStateDirectoryFence) restore() error {
	if f.restored {
		return nil
	}
	if _, err := f.info(); err != nil {
		return err
	}
	if err := os.Chmod(f.cfg.StateDir, os.FileMode(f.plan.StateDirectoryMode)); err != nil {
		return err
	}
	if err := syncDir(f.cfg.StateDir); err != nil {
		return err
	}
	f.restored = true
	return nil
}

func selfServiceState(cfg Config, p SelfUpdatePlan) (State, error) {
	s, err := readState(filepath.Join(cfg.StateDir, "state.json"))
	if err != nil {
		return s, err
	}
	if s.InstanceID != cfg.InstanceID || s.Process != p.Child || !done(s.Operation) || !maintenanceTerminal(s.Maintenance) || s.Recovery != nil && s.Recovery.Phase != "ledger_linked" {
		return s, errors.New("updater: service operation changed before supervisor handoff")
	}
	return s, nil
}
