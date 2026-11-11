package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"

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
	s, err := backupObjectStore(cfg)
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
		s, err := backupObjectStore(cfg)
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

// priorBackupFile reads actual immutable checkpoint manifests. It never creates
// a parent checkpoint dependency: the selected file's flat parts are copied.
func priorBackupFile(ctx context.Context, cfg Config, op *Operation, source string) (BackupFile, string, bool, error) {
	type candidate struct {
		file BackupFile
		dir  string
		at   int64
	}
	var candidates []candidate
	for _, root := range []string{"backups", "maintenance-backups"} {
		entries, err := os.ReadDir(filepath.Join(cfg.StateDir, root))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return BackupFile{}, "", false, err
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return BackupFile{}, "", false, err
			}
			dir := filepath.Join(cfg.StateDir, root, entry.Name())
			if dir == op.BackupDir {
				continue
			}
			record, err := readCheckpoint(cfg, dir)
			// An unreadable checkpoint is not evidence that a prefix can be reused.
			// Retention independently protects its unknown references.
			if err != nil {
				continue
			}
			for _, f := range record.Files {
				if f.Path == source && f.Existed && sharedBackupFile(f) {
					candidates = append(candidates, candidate{f, dir, record.CreatedAtMillis})
				}
			}
		}
	}
	if len(candidates) == 0 {
		return BackupFile{}, "", false, nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].at != candidates[j].at {
			return candidates[i].at > candidates[j].at
		}
		return candidates[i].dir > candidates[j].dir
	})
	c := candidates[0]
	return c.file, c.dir, true, nil
}

func reuseBackupPrefix(ctx context.Context, cfg Config, op *Operation, f *BackupFile, source string) (bool, error) {
	// Retrying an already materialized checkpoint preserves its exact manifest
	// references even if other checkpoints have since appeared or disappeared.
	raw, err := os.ReadFile(filepath.Join(op.BackupDir, "files.json"))
	if err == nil {
		var prior []BackupFile
		if err := json.Unmarshal(raw, &prior); err != nil {
			return false, err
		}
		for _, saved := range prior {
			if saved.Path != source {
				continue
			}
			if saved.Name != f.Name || saved.SHA256 != f.SHA256 || saved.Mode != f.Mode || !saved.Existed {
				return false, errors.New("updater: frozen backup source changed")
			}
			if err := copyBackupContents(ctx, cfg, op.BackupDir, saved, io.Discard); err != nil {
				return false, err
			}
			*f = saved
			return true, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if !appendBackupOriginal(cfg, source) {
		return false, nil
	}
	prior, dir, found, err := priorBackupFile(ctx, cfg, op, source)
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
	s, err := backupObjectStore(cfg)
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
