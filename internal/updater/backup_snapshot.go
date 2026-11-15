package updater

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/GhostFlying/tend/internal/blobstore"
)

type backupCandidate struct {
	file BackupFile
	dir  string
	at   int64
}

// A snapshot owns one bounded pass over immutable checkpoint metadata. Neither
// later operations nor retries inherit this in-memory index or its object store.
type backupSnapshot struct {
	cfg     Config
	op      *Operation
	frozen  map[string]BackupFile
	prior   map[string]backupCandidate
	objects *blobstore.Store
}

func newBackupSnapshot(cfg Config, op *Operation) *backupSnapshot {
	return &backupSnapshot{cfg: cfg, op: op}
}
func (b *backupSnapshot) objectStore() (*blobstore.Store, error) {
	if b.objects == nil {
		s, err := backupObjectStore(b.cfg)
		if err != nil {
			return nil, err
		}
		b.objects = s
	}
	return b.objects, nil
}
func (b *backupSnapshot) loadFrozen() error {
	if b.frozen != nil {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(b.op.BackupDir, "files.json"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var files []BackupFile
	if err == nil {
		if err = json.Unmarshal(raw, &files); err != nil {
			return err
		}
	}
	b.frozen = make(map[string]BackupFile, len(files))
	for _, f := range files {
		if _, duplicate := b.frozen[f.Path]; duplicate {
			return errors.New("updater: duplicate frozen backup source")
		}
		b.frozen[f.Path] = f
	}
	return nil
}
func (b *backupSnapshot) priorBackupFile(ctx context.Context, source string) (BackupFile, string, bool, error) {
	if b.prior == nil {
		index := map[string]backupCandidate{}
		for _, root := range []string{"backups", "maintenance-backups"} {
			entries, err := os.ReadDir(filepath.Join(b.cfg.StateDir, root))
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
				dir := filepath.Join(b.cfg.StateDir, root, entry.Name())
				if dir == b.op.BackupDir {
					continue
				}
				record, err := readCheckpoint(b.cfg, dir)
				// Unknown checkpoints are never evidence that a prefix can be reused.
				// Retention independently protects their unknown references.
				if err != nil {
					continue
				}
				for _, f := range record.Files {
					if !f.Existed || !sharedBackupFile(f) {
						continue
					}
					previous, ok := index[f.Path]
					if !ok || record.CreatedAtMillis > previous.at || record.CreatedAtMillis == previous.at && dir > previous.dir {
						index[f.Path] = backupCandidate{f, dir, record.CreatedAtMillis}
					}
				}
			}
		}
		b.prior = index
	}
	candidate, ok := b.prior[source]
	return candidate.file, candidate.dir, ok, nil
}
