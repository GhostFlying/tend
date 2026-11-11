package rollout

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Open confines all transcript reads to configured native directories, including symlinks.
func Open(roots []string, path string) (*os.File, error) {
	missing := false
	for _, root := range roots {
		rel, err := relativeToRoot(root, path)
		if err != nil {
			missing = missing || errors.Is(err, os.ErrNotExist)
			continue
		}
		dir, err := os.OpenRoot(root)
		if err != nil {
			missing = missing || errors.Is(err, os.ErrNotExist)
			continue
		}
		f, err := dir.Open(rel)
		_ = dir.Close()
		if err == nil {
			return f, nil
		}
		if errors.Is(err, os.ErrNotExist) {
			return nil, os.ErrNotExist
		}
	}
	if missing {
		return nil, os.ErrNotExist
	}
	return nil, fs.ErrPermission
}

// relativeToRoot recognizes native canonical paths and configured directory
// aliases. Only ancestors are resolved: the leaf is still opened through
// os.Root, so resolving an alias never authorizes a symlink outside the root.
func relativeToRoot(root, path string) (string, error) {
	if rel, err := filepath.Rel(root, path); err == nil && filepath.IsLocal(rel) {
		return rel, nil
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(canonicalRoot, filepath.Join(parent, filepath.Base(path)))
	if err != nil || !filepath.IsLocal(rel) {
		return "", fs.ErrPermission
	}
	return rel, nil
}

// Locate uses names only to narrow candidates; the native header proves the identity.
func Locate(ctx context.Context, roots []string, session string) (string, error) {
	if session == "" || strings.ContainsAny(session, "/\\\x00") {
		return "", nil
	}
	for _, root := range roots {
		found := ""
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if walkErr != nil {
				if path == root && errors.Is(walkErr, os.ErrNotExist) {
					return fs.SkipDir
				}
				return nil
			}
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), session+".jsonl") {
				return nil
			}
			f, err := Open(roots, path)
			if err != nil {
				return nil
			}
			var header struct {
				Type    string `json:"type"`
				Payload struct {
					ID string `json:"id"`
				} `json:"payload"`
			}
			err = json.NewDecoder(io.LimitReader(f, maxNativeLine)).Decode(&header)
			_ = f.Close()
			if err == nil && header.Type == "session_meta" && header.Payload.ID == session {
				found = path
				return fs.SkipAll
			}
			return nil
		})
		if err != nil {
			return "", err
		}
		if found != "" {
			return found, nil
		}
	}
	return "", nil
}
