package updater

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/GhostFlying/tend/internal/release"
)

func installVersion(stateDir string, b *release.Bundle, a release.Artifact) (Version, error) {
	if err := b.Verify(a); err != nil {
		return Version{}, err
	}
	storage := b.Manifest.Storage.ServerSchema.Max
	if a.Role == "runner" {
		storage = b.Manifest.Storage.RunnerJournal.Max
	}
	dir := filepath.Join(stateDir, "versions", b.Manifest.ReleaseID+"-"+a.SHA256)
	binary := filepath.Join(dir, "tend-"+a.Role)
	if info, err := os.Lstat(dir); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return Version{}, errors.New("updater: unsafe installed version directory")
		}
		sum, err := hashFile(binary)
		if err != nil || sum != a.SHA256 {
			return Version{}, errors.New("updater: existing version binary is corrupt")
		}
		raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
		if err != nil || string(raw) != string(b.ManifestBytes) {
			return Version{}, errors.New("updater: release ID already has a different manifest")
		}
		return Version{Protocol: b.Manifest.Protocol.Max, StorageVersion: storage, ReleaseID: b.Manifest.ReleaseID, ManifestSHA256: b.ManifestSHA256, Artifact: a, Binary: binary}, nil
	} else if !os.IsNotExist(err) {
		return Version{}, err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return Version{}, err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dir), ".install-")
	if err != nil {
		return Version{}, err
	}
	defer os.RemoveAll(tmp)
	path, err := release.RegularPath(b.Dir, a.Path)
	if err != nil {
		return Version{}, err
	}
	target := filepath.Join(tmp, "tend-"+a.Role)
	if err := copyAtomic(path, target, 0o755); err != nil {
		return Version{}, err
	}
	sum, err := hashFile(target)
	if err != nil || sum != a.SHA256 {
		return Version{}, errors.New("updater: installed artifact verification failed")
	}
	if err := atomicWrite(filepath.Join(tmp, "manifest.json"), b.ManifestBytes, 0o600); err != nil {
		return Version{}, err
	}
	if err := syncDir(tmp); err != nil {
		return Version{}, err
	}
	if err := os.Rename(tmp, dir); err != nil {
		return Version{}, err
	}
	if err := syncDir(filepath.Dir(dir)); err != nil {
		return Version{}, err
	}
	return Version{Protocol: b.Manifest.Protocol.Max, StorageVersion: storage, ReleaseID: b.Manifest.ReleaseID, ManifestSHA256: b.ManifestSHA256, Artifact: a, Binary: binary}, nil
}
func (s *Supervisor) stage(dir string) (Version, error) {
	b, err := release.LoadService(dir, s.cfg.Role)
	if err != nil {
		return Version{}, err
	}
	a, err := b.Artifact(s.cfg.Role, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return Version{}, err
	}
	if !b.Manifest.Protocol.Contains(s.state.Current.Protocol) {
		return Version{}, errors.New("updater: candidate cannot communicate with installed protocol")
	}
	storage := b.Manifest.Storage.ServerSchema
	if s.cfg.Role == "runner" {
		storage = b.Manifest.Storage.RunnerJournal
	}
	if !storage.Contains(s.state.Current.StorageVersion) {
		return Version{}, errors.New("updater: candidate cannot read installed storage format")
	}
	return installVersion(s.cfg.StateDir, b, a)
}
func dataPaths(cfg Config) []string {
	var paths []string
	for _, p := range cfg.DataFiles {
		paths = append(paths, p, p+"-wal", p+"-shm")
	}
	return paths
}
func backupData(cfg Config, op *Operation) error {
	return backupDataContext(context.Background(), cfg, op)
}
func backupDataContext(ctx context.Context, cfg Config, op *Operation) error {
	if err := os.MkdirAll(op.BackupDir, 0o700); err != nil {
		return err
	}
	paths, err := dataSnapshotPaths(cfg)
	if err != nil {
		return err
	}
	files := make([]BackupFile, 0)
	for i, p := range paths {
		name := backupName(i)
		f := BackupFile{Path: p, Name: name}
		info, err := os.Lstat(p)
		if os.IsNotExist(err) {
			files = append(files, f)
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("updater: data backup requires regular files")
		}
		f.Existed = true
		f.Mode = uint32(info.Mode().Perm())
		f.SHA256, err = hashBackupFile(ctx, p)
		if err != nil {
			return err
		}
		if err := persistBackupFile(ctx, cfg, op, &f, p); err != nil {
			return err
		}
		dst, err := backupFilePath(cfg, op.BackupDir, f)
		if err != nil {
			return err
		}
		sum, err := hashBackupFile(ctx, dst)
		if err != nil || sum != f.SHA256 {
			return errors.New("updater: backup verification failed")
		}
		files = append(files, f)
	}
	op.Backups = files
	if err := writeJSON(filepath.Join(op.BackupDir, "files.json"), files); err != nil {
		return err
	}
	return writeBackupMetadata(cfg, op)
}
func restoreData(cfg Config, op *Operation) error {
	if op.Irreversible {
		return errors.New("updater: data rollback is permanently forbidden after new work")
	}
	if err := verifyBackupFiles(cfg, op.BackupDir, op.Backups); err != nil {
		return err
	}
	for _, f := range op.Backups {
		if configuredOriginal(cfg, f.Path) {
			if err := prepareOriginalRestore(cfg, f.Path); err != nil {
				return err
			}
		}
		if f.Existed {
			source, err := backupFilePath(cfg, op.BackupDir, f)
			if err != nil {
				return err
			}
			if err := copyAtomic(source, f.Path, os.FileMode(f.Mode)); err != nil {
				return err
			}
		} else {
			if err := os.Remove(f.Path); err != nil && !os.IsNotExist(err) {
				return err
			}
			if err := syncDir(filepath.Dir(f.Path)); err != nil {
				return err
			}
		}
	}
	return nil
}

func backupName(i int) string { return fmt.Sprintf("data-%03d", i) }
