package sandbox

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const markerName = ".tend-sandbox"
const markerContents = "tend-sandbox-v1\n"

func prepareData(path string, reset bool) (string, func(), error) {
	if path == "" {
		return "", nil, errors.New("-data is required")
	}
	root, err := filepath.Abs(path)
	if err != nil {
		return "", nil, err
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return "", nil, err
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || resolved != root {
		return "", nil, errors.New("sandbox data directory must not use symbolic links")
	}
	if err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("sandbox data must not contain symbolic links")
		}
		return nil
	}); err != nil {
		return "", nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", nil, err
	}
	marker := filepath.Join(root, markerName)
	if len(entries) == 0 {
		if err = os.WriteFile(marker, []byte(markerContents), 0600); err != nil {
			return "", nil, err
		}
	} else {
		info, err := os.Lstat(marker)
		if err != nil || !info.Mode().IsRegular() || info.Size() != int64(len(markerContents)) {
			return "", nil, errors.New("sandbox marker must be a regular versioned marker file")
		}
		contents, err := os.ReadFile(marker)
		if err != nil || string(contents) != markerContents {
			return "", nil, errors.New("refusing nonempty data directory without a valid sandbox marker")
		}
	}
	// An exclusive run marker protects a running sandbox from another reset.
	// After an unclean exit an operator must verify it is stopped and remove it.
	lock := filepath.Join(root, ".sandbox-running")
	file, err := os.OpenFile(lock, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", nil, fmt.Errorf("sandbox already running or stale run marker: %w", err)
	}
	if err = file.Close(); err != nil {
		return "", nil, err
	}
	release := func() { _ = os.Remove(lock) }
	if reset {
		for _, entry := range entries {
			if entry.Name() == markerName {
				continue
			}
			if err = os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
				release()
				return "", nil, err
			}
		}
	}
	for _, dir := range []string{"runner", "workspaces"} {
		if err = os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			release()
			return "", nil, err
		}
	}
	return root, release, nil
}
