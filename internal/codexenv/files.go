package codexenv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/mod/semver"
)

func digest(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }

func makeDirectoryDurable(ctx context.Context, path string) error {
	var missing []string
	for cursor := path; ; cursor = filepath.Dir(cursor) {
		info, err := os.Stat(cursor)
		if err == nil {
			if !info.IsDir() {
				return fmt.Errorf("store ancestor is not a directory")
			}
			break
		}
		if !os.IsNotExist(err) {
			return err
		}
		missing = append(missing, cursor)
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.Mkdir(missing[i], 0700); err != nil && !os.IsExist(err) {
			return err
		}
		parent, err := os.Open(filepath.Dir(missing[i]))
		if err != nil {
			return err
		}
		err = parent.Sync()
		closeErr := parent.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func syncParents(path, boundary string) error {
	path, boundary = canonicalPath(path), canonicalPath(boundary)
	if !within(path, boundary) {
		return fmt.Errorf("publication is outside the environment store")
	}
	for ; ; path = filepath.Dir(path) {
		dir, err := os.Open(path)
		if err != nil {
			return err
		}
		err = dir.Sync()
		closeErr := dir.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if path == boundary {
			return nil
		}
	}
}

func syncTree(ctx context.Context, root string) error {
	var dirs []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			dirs = append(dirs, path)
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		err = file.Sync()
		closeErr := file.Close()
		if err != nil {
			return err
		}
		return closeErr
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		dir, err := os.Open(dirs[i])
		if err != nil {
			return err
		}
		err = dir.Sync()
		closeErr := dir.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
func atomicWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".write-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func writeJSON(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, raw)
}
func loadRecord(path string) (record, error) {
	var r record
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	err = json.Unmarshal(raw, &r)
	if r.Requests == nil {
		r.Requests = map[string]receipt{}
	}
	if r.Status.Adoptions == nil {
		r.Status.Adoptions = map[string]Adoption{}
	}
	return r, err
}

// materialize follows links into complete ordinary copies. A canonical-directory
// stack rejects cycles; source and target are never hard-linked.
func materialize(ctx context.Context, source, target string) error {
	if within(target, source) {
		return fmt.Errorf("package destination overlaps its source")
	}
	return copyPath(ctx, source, target, map[string]bool{})
}
func copyPath(ctx context.Context, source, target string, stack map[string]bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	real, err := filepath.EvalSymlinks(source)
	if err != nil {
		return fmt.Errorf("resolve package source: %w", err)
	}
	info, err := os.Stat(real)
	if err != nil {
		return err
	}
	if info.IsDir() {
		if within(target, real) {
			return fmt.Errorf("resolved package directory contains its destination")
		}
		if stack[real] {
			return fmt.Errorf("package contains a symlink cycle")
		}
		stack[real] = true
		defer delete(stack, real)
		if err = os.MkdirAll(target, 0700); err != nil {
			return err
		}
		entries, err := os.ReadDir(real)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.Name() == ".git" {
				continue
			}
			if err = copyPath(ctx, filepath.Join(real, entry.Name()), filepath.Join(target, entry.Name()), stack); err != nil {
				return err
			}
		}
		return nil
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("package contains a non-regular file")
	}
	raw, err := os.ReadFile(real)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	return os.WriteFile(target, raw, 0600|info.Mode().Perm()&0111)
}
func treeDigest(ctx context.Context, root string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			_, _ = fmt.Fprintf(h, "dir:%d:%s:", len(rel), rel)
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(h, "%d:%s:%o:%d:", len(rel), rel, info.Mode().Perm()&0111, len(raw))
		_, _ = h.Write(raw)
		return nil
	})
	return hex.EncodeToString(h.Sum(nil)), err
}
func cacheSource(home, id string) (string, error) {
	name, market, ok := strings.Cut(id, "@")
	if !ok || !safeSegment(name) || !safeSegment(market) {
		return "", fmt.Errorf("plugin requires plugin@marketplace identity")
	}
	root := filepath.Join(home, "plugins", "cache", market, name)
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	var versions []string
	for _, entry := range entries {
		if entry.IsDir() {
			versions = append(versions, entry.Name())
		}
	}
	// Match native precedence: local wins, semantic versions compare as such,
	// and non-semantic cache names use lexical comparison.
	for _, version := range versions {
		if version == "local" {
			return filepath.Join(root, version), nil
		}
	}
	sort.Slice(versions, func(i, j int) bool {
		a, b := "v"+versions[i], "v"+versions[j]
		if semver.IsValid(a) && semver.IsValid(b) {
			return semver.Compare(a, b) < 0
		}
		return versions[i] < versions[j]
	})
	if len(versions) == 0 {
		return "", fmt.Errorf("source plugin has no installed bundle")
	}
	return filepath.Join(root, versions[len(versions)-1]), nil
}
func safeSegment(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\\x00")
}
