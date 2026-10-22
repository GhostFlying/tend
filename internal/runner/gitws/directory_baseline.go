package gitws

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type directoryFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Mode   uint32 `json:"mode"`
	SHA256 string `json:"sha256,omitempty"`
	Link   string `json:"link,omitempty"`
}

func directoryBaseline(ctx context.Context, home, taskID string) (json.RawMessage, error) {
	path := filepath.Join(home, ".tend", "baselines", taskID+".json")
	if b, err := os.ReadFile(path); err == nil {
		return b, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	files := []directoryFile{}
	err := filepath.WalkDir(home, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		rel, err := filepath.Rel(home, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if rel == ".tend" {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		if rel == filepath.Join(".codex", "config.toml") || rel == filepath.Join(".trae", "traecli.toml") {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.HasPrefix(string(b), "# Written by tend-runner for the run in this worktree;") {
				return nil
			}
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		file := directoryFile{Path: filepath.ToSlash(rel), Size: info.Size(), Mode: uint32(info.Mode())}
		switch {
		case info.Mode().IsRegular():
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			digest := sha256.New()
			_, copyErr := io.Copy(digest, f)
			if err := errors.Join(copyErr, f.Close()); err != nil {
				return err
			}
			file.SHA256 = hex.EncodeToString(digest.Sum(nil))
		case info.Mode()&os.ModeSymlink != 0:
			file.Link, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		files = append(files, file)
		return nil
	})
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(struct {
		Kind  string          `json:"kind"`
		Files []directoryFile `json:"files"`
	}{"directory", files})
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path+".tmp", b, 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		return nil, err
	}
	return b, nil
}
