package updater

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/GhostFlying/tend/internal/blobstore"
)

func backupObjectStore(cfg Config) (*blobstore.Store, error) {
	return blobstore.New(filepath.Join(cfg.StateDir, "backup-objects"))
}

// persistBackupFile keeps mutable ledger snapshots local to their checkpoint;
// immutable original/spool bytes share the existing content-addressed facility.
func persistBackupFile(ctx context.Context, cfg Config, op *Operation, f *BackupFile, source string) error {
	if configuredOriginal(cfg, source) && cfg.StateDir != "" {
		if reused, err := reuseBackupPrefix(ctx, cfg, op, f, source); reused || err != nil {
			return err
		}
		s, err := backupObjectStore(cfg)
		if err != nil {
			return err
		}
		input, err := os.Open(source)
		if err != nil {
			return err
		}
		obj, err := s.Put(ctx, input)
		err = errors.Join(err, input.Close())
		if err != nil {
			return err
		}
		if obj.SHA256 != f.SHA256 {
			return errors.New("updater: original changed during backup")
		}
		f.ObjectSHA256 = obj.SHA256
		return nil
	}
	target := filepath.Join(op.BackupDir, f.Name)
	if prior, err := os.Lstat(target); err == nil {
		if !prior.Mode().IsRegular() || prior.Mode()&os.ModeSymlink != 0 {
			return errors.New("updater: unsafe existing backup")
		}
		hash, err := hashFile(target)
		if err != nil || hash != f.SHA256 {
			return errors.New("updater: existing backup bytes changed")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return copyAtomic(source, target, 0600)
}

func backupFilePath(cfg Config, directory string, f BackupFile) (string, error) {
	if len(f.ObjectParts) != 0 {
		return "", errors.New("updater: multipart backup requires a streaming reader")
	}
	if f.ObjectSHA256 != "" {
		if f.ObjectSHA256 != f.SHA256 || !configuredOriginal(cfg, f.Path) || cfg.StateDir == "" {
			return "", errors.New("updater: invalid shared backup object")
		}
		s, err := backupObjectStore(cfg)
		if err != nil {
			return "", err
		}
		return s.Path(f.ObjectSHA256)
	}
	if filepath.Base(f.Name) != f.Name || strings.ContainsAny(f.Name, `/\\`) || f.Name == "." || f.Name == "" {
		return "", errors.New("updater: invalid backup filename")
	}
	return filepath.Join(directory, f.Name), nil
}

func verifyBackupFiles(cfg Config, directory string, files []BackupFile) error {
	paths := dataPaths(cfg)
	if len(files) < len(paths) {
		return errors.New("updater: incomplete backup ledger set")
	}
	for i, f := range files {
		if f.Name != backupName(i) || (i < len(paths) && f.Path != paths[i]) || (i >= len(paths) && (!f.Existed || !configuredOriginal(cfg, f.Path))) {
			return errors.New("updater: backup restore root differs from fixed configuration")
		}
		if !f.Existed {
			continue
		}
		if err := copyBackupContents(context.Background(), cfg, directory, f, io.Discard); err != nil {
			return err
		}
	}
	return nil
}

type backupReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *backupReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func hashBackupFile(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return hashDiagnostic(ctx, f)
}
