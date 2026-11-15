package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"

	"github.com/GhostFlying/tend/internal/protocol"
)

func sharedBackupFile(f BackupFile) bool { return f.ObjectSHA256 != "" || len(f.ObjectParts) != 0 }

// backupParts validates flat references without trusting sizes from a manifest.
// A legacy whole object is represented as one part only inside this reader.
func backupParts(cfg Config, f BackupFile) ([]protocol.BackupObjectPart, error) {
	if !configuredOriginal(cfg, f.Path) || cfg.StateDir == "" || !validBackupSHA256(f.SHA256) {
		return nil, errors.New("updater: invalid shared backup root or digest")
	}
	if len(f.ObjectParts) != 0 {
		if f.ObjectSHA256 != "" {
			return nil, errors.New("updater: mixed backup object representations")
		}
		var size int64
		for _, p := range f.ObjectParts {
			if !validBackupSHA256(p.SHA256) || p.Size <= 0 || size > math.MaxInt64-p.Size {
				return nil, errors.New("updater: invalid backup object part")
			}
			size += p.Size
		}
		return f.ObjectParts, nil
	}
	if f.ObjectSHA256 != f.SHA256 {
		return nil, errors.New("updater: invalid shared backup object")
	}
	s, err := openBackupObjectStore(cfg)
	if err != nil {
		return nil, err
	}
	path, err := s.Path(f.ObjectSHA256)
	if err != nil {
		return nil, err
	}
	size, _, err := regularSize(path)
	if err != nil {
		return nil, err
	}
	return []protocol.BackupObjectPart{{SHA256: f.ObjectSHA256, Size: size}}, nil
}

// copyBackupContents validates each physical object and the complete file while
// streaming. Callers publish restore bytes only after this returns successfully.
func copyBackupContents(ctx context.Context, cfg Config, dir string, f BackupFile, dst io.Writer) error {
	if !validBackupSHA256(f.SHA256) {
		return errors.New("updater: invalid backup digest")
	}
	whole := sha256.New()
	write := io.MultiWriter(dst, whole)
	if sharedBackupFile(f) {
		parts, err := backupParts(cfg, f)
		if err != nil {
			return err
		}
		s, err := openBackupObjectStore(cfg)
		if err != nil {
			return err
		}
		for _, part := range parts {
			path, err := s.Path(part.SHA256)
			if err != nil {
				return err
			}
			size, _, err := regularSize(path)
			if err != nil {
				return err
			}
			if size != part.Size {
				return errors.New("updater: backup part size differs")
			}
			input, err := os.Open(path)
			if err != nil {
				return err
			}
			hash := sha256.New()
			n, copyErr := io.Copy(io.MultiWriter(write, hash), &backupReader{ctx, input})
			err = errors.Join(copyErr, input.Close())
			if err != nil {
				return err
			}
			if n != part.Size || hex.EncodeToString(hash.Sum(nil)) != part.SHA256 {
				return errors.New("updater: corrupt backup object part")
			}
		}
	} else {
		path, err := backupFilePath(cfg, dir, f)
		if err != nil {
			return err
		}
		if _, _, err = regularSize(path); err != nil {
			return err
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(write, &backupReader{ctx, input})
		if err = errors.Join(copyErr, input.Close()); err != nil {
			return err
		}
	}
	if hex.EncodeToString(whole.Sum(nil)) != f.SHA256 {
		return errors.New("updater: corrupt reconstructed backup content")
	}
	return ctx.Err()
}

func restoreBackupFile(ctx context.Context, cfg Config, dir string, f BackupFile) error {
	out, err := os.CreateTemp(filepath.Dir(f.Path), ".updater-restore-")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	defer out.Close()
	if err = out.Chmod(os.FileMode(f.Mode)); err != nil {
		return err
	}
	if err = copyBackupContents(ctx, cfg, dir, f, out); err != nil {
		return err
	}
	if err = out.Sync(); err != nil {
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	if err = os.Rename(out.Name(), f.Path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(f.Path))
}

func appendBackupOriginal(cfg Config, path string) bool {
	for _, root := range originalRoots(cfg) {
		if filepath.Dir(path) == filepath.Join(root, "archives") && validBackupSHA256(filepath.Base(path)) {
			return true
		}
	}
	return false
}

func (b *backupSnapshot) reuseBackupPrefix(ctx context.Context, f *BackupFile, source string) (bool, error) {
	cfg, op := b.cfg, b.op
	// Retrying an already materialized checkpoint preserves its exact manifest
	// references even if other checkpoints have since appeared or disappeared.
	if err := b.loadFrozen(); err != nil {
		return false, err
	}
	if saved, ok := b.frozen[source]; ok {
		if saved.Name != f.Name || saved.SHA256 != f.SHA256 || saved.Mode != f.Mode || !saved.Existed {
			return false, errors.New("updater: frozen backup source changed")
		}
		if err := copyBackupContents(ctx, cfg, op.BackupDir, saved, io.Discard); err != nil {
			return false, err
		}
		*f = saved
		return true, nil
	}
	if !appendBackupOriginal(cfg, source) {
		return false, nil
	}
	prior, dir, found, err := b.priorBackupFile(ctx, source)
	if err != nil || !found {
		return false, err
	}
	parts, err := backupParts(cfg, prior)
	if err != nil {
		return false, err
	}
	if err = copyBackupContents(ctx, cfg, dir, prior, io.Discard); err != nil {
		return false, err
	}
	var oldSize int64
	for _, p := range parts {
		oldSize += p.Size
	}
	input, err := os.Open(source)
	if err != nil {
		return false, err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return false, err
	}
	if oldSize == 0 || info.Size() < oldSize {
		return false, nil
	}
	hash := sha256.New()
	if _, err = io.CopyN(hash, &backupReader{ctx, input}, oldSize); err != nil {
		return false, err
	}
	if hex.EncodeToString(hash.Sum(nil)) != prior.SHA256 {
		return false, nil
	}
	if info.Size() == oldSize {
		if f.SHA256 != prior.SHA256 {
			return false, errors.New("updater: original changed during prefix verification")
		}
		f.ObjectSHA256 = prior.ObjectSHA256
		f.ObjectParts = append([]protocol.BackupObjectPart(nil), prior.ObjectParts...)
		return true, nil
	}
	s, err := b.objectStore()
	if err != nil {
		return false, err
	}
	tail, err := s.Put(ctx, io.TeeReader(&backupReader{ctx, input}, hash))
	if err != nil {
		return false, err
	}
	if tail.Size != info.Size()-oldSize || hex.EncodeToString(hash.Sum(nil)) != f.SHA256 {
		return false, errors.New("updater: original changed during tail backup")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	// The reused objects were verified above; Put verified the new immutable
	// object. The continuing source hash pins their complete concatenation,
	// including for bounded backups before their manifest is committed.
	f.ObjectParts = append(append([]protocol.BackupObjectPart(nil), parts...), protocol.BackupObjectPart{SHA256: tail.SHA256, Size: tail.Size})
	return true, nil
}

func backupManifestFormat(files []BackupFile) int {
	for _, f := range files {
		if len(f.ObjectParts) != 0 {
			return 2
		}
	}
	return 1
}
