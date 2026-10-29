package archive

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Verify checks every payload, including raw SQLite/sidecars and originals.
// The digest establishes copy integrity, not authenticity against a malicious owner.
func Verify(ctx context.Context, root string) (Manifest, error) {
	var manifest Manifest
	absolute, err := filepath.Abs(root)
	if err != nil {
		return manifest, err
	}
	if err = readJSON(filepath.Join(absolute, "manifest.json"), &manifest); err != nil {
		return manifest, err
	}
	if manifest.FormatVersion != FormatVersion || manifest.Snapshot != "snapshot.sqlite" || manifest.CreatedAt.IsZero() {
		return manifest, errors.New("archive: unsupported/incomplete manifest")
	}
	files := map[string]Payload{}
	for _, file := range manifest.Files {
		if _, exists := files[file.Path]; exists {
			return manifest, errors.New("archive: duplicate payload path")
		}
		path, err := payloadPath(absolute, file.Path)
		if err != nil {
			return manifest, err
		}
		if _, err = regularFile(path); err != nil {
			return manifest, err
		}
		size, hash, err := hashFile(ctx, path)
		if err != nil {
			return manifest, err
		}
		if size != file.Size || hash != file.SHA256 {
			return manifest, fmt.Errorf("archive: payload integrity mismatch: %s", file.Path)
		}
		files[file.Path] = file
	}
	if _, exists := files[manifest.Snapshot]; !exists {
		return manifest, errors.New("archive: snapshot not listed")
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err = os.Stat(filepath.Join(absolute, manifest.Snapshot+suffix)); !os.IsNotExist(err) {
			return manifest, errors.New("archive: immutable snapshot must have no sidecars")
		}
	}
	err = filepath.WalkDir(absolute, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(absolute, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == "manifest.json" {
			return nil
		}
		if _, exists := files[relative]; !exists {
			return fmt.Errorf("archive: unlisted payload: %s", relative)
		}
		return nil
	})
	if err != nil {
		return manifest, err
	}
	originals := map[string]bool{}
	for _, original := range manifest.Originals {
		key := original.Kind + "/" + original.ID
		if originals[key] || original.ID == "" {
			return manifest, errors.New("archive: duplicate/empty original identity")
		}
		originals[key] = true
		file, exists := files[original.Path]
		if !exists || !strings.HasPrefix(original.Path, "originals/") || file.Size != original.Size || file.SHA256 != original.SHA256 {
			return manifest, errors.New("archive: invalid original mapping")
		}
	}
	for _, resource := range manifest.Resources {
		for _, path := range resource.Files {
			if _, exists := files[path]; !exists {
				return manifest, errors.New("archive: unlisted resource payload")
			}
		}
	}
	db, err := openSnapshot(ctx, filepath.Join(absolute, manifest.Snapshot))
	if err != nil {
		return manifest, err
	}
	defer db.Close()
	identity, migrations, err := inspectLedger(ctx, db)
	if err != nil {
		return manifest, err
	}
	if identity != manifest.DeploymentID || len(migrations) != manifest.Schema || len(migrations) != len(manifest.Migrations) {
		return manifest, errors.New("archive: snapshot identity/schema differs from manifest")
	}
	for i, m := range migrations {
		if m != manifest.Migrations[i] {
			return manifest, errors.New("archive: migration list differs from snapshot")
		}
	}
	if err = checkIntegrity(ctx, db); err != nil {
		return manifest, err
	}
	return manifest, nil
}

func Open(ctx context.Context, root string) (*Reader, error) {
	manifest, err := Verify(ctx, root)
	if err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	db, err := openSnapshot(ctx, filepath.Join(absolute, manifest.Snapshot))
	if err != nil {
		return nil, err
	}
	return &Reader{root: absolute, manifest: manifest, db: db}, nil
}
