package updater

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// dataSnapshotPaths includes durable originals beside each configured ledger.
// Services are stopped before snapshotting. Temporary uploads have no ledger
// reference and are deliberately excluded; committed original files are copied.
func dataSnapshotPaths(cfg Config) ([]string, error) {
	paths := dataPaths(cfg)
	var originals []string
	for _, ledger := range cfg.DataFiles {
		root := ledger + ".files"
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				if path == root && errors.Is(walkErr, os.ErrNotExist) {
					return nil
				}
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return errors.New("updater: original backup contains a symlink")
			}
			if entry.IsDir() {
				return nil
			}
			if strings.HasPrefix(entry.Name(), ".upload-") {
				return nil
			}
			if !entry.Type().IsRegular() {
				return errors.New("updater: original backup requires regular files")
			}
			originals = append(originals, path)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(originals)
	return append(paths, originals...), nil
}

func configuredOriginal(cfg Config, path string) bool {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	for _, ledger := range cfg.DataFiles {
		root := filepath.Clean(ledger + ".files")
		relative, err := filepath.Rel(root, path)
		if err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func prepareOriginalRestore(cfg Config, path string) error {
	if !configuredOriginal(cfg, path) {
		return errors.New("updater: original restore escapes configured files")
	}
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			return errors.New("updater: unsafe original restore parent")
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		isRoot := false
		for _, ledger := range cfg.DataFiles {
			isRoot = isRoot || parent == filepath.Clean(ledger+".files")
		}
		if isRoot {
			break
		}
		if filepath.Dir(parent) == parent {
			return errors.New("updater: original restore parent not found")
		}
	}
	return os.MkdirAll(filepath.Dir(path), 0700)
}
