package release

import (
	"errors"
	"io"
	"os"
	"path/filepath"
)

// CopyTo atomically stages the complete verified bundle, rather than retaining
// a caller-controlled source directory that could change during a rollout.
// Existing destinations are replayable only when their complete identity agrees.
func (b *Bundle) CopyTo(destination string) (*Bundle, error) {
	if _, err := os.Lstat(destination); err == nil {
		existing, err := Load(destination)
		if err != nil {
			return nil, err
		}
		if existing.ManifestSHA256 != b.ManifestSHA256 {
			return nil, errors.New("release: staging destination belongs to another manifest")
		}
		return existing, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp(parent, ".bundle-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	for _, a := range b.Manifest.Artifacts {
		src, err := RegularPath(b.Dir, a.Path)
		if err != nil {
			return nil, err
		}
		target := filepath.Join(tmp, a.Path)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return nil, err
		}
		if err := copyFile(src, target, 0o755); err != nil {
			return nil, err
		}
	}
	mf, err := os.OpenFile(filepath.Join(tmp, "manifest.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err := mf.Write(b.ManifestBytes); err != nil {
		_ = mf.Close()
		return nil, err
	}
	if err := mf.Sync(); err != nil {
		_ = mf.Close()
		return nil, err
	}
	if err := mf.Close(); err != nil {
		return nil, err
	}
	if _, err := Load(tmp); err != nil {
		return nil, err
	}
	// Flush every directory after file data; rename publishes only a complete set.
	if err := filepath.WalkDir(tmp, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		return f.Sync()
	}); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, destination); err != nil {
		return nil, err
	}
	f, err := os.Open(parent)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		return nil, err
	}
	return Load(destination)
}
func copyFile(source, destination string, mode os.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
