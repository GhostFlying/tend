// Package blobstore retains immutable original files outside the ledger.
package blobstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Object identifies completed durable content; names are ledger metadata only.
type Object struct {
	SHA256 string
	Size   int64
}

// Store is a content-addressed directory. Publication never overwrites a file.
type Store struct{ root string }

// New creates a private store at root.
func New(root string) (*Store, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("blobstore: resolve root: %w", err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("blobstore: create root: %w", err)
	}
	if err := syncDir(filepath.Dir(root)); err != nil {
		return nil, err
	}
	return &Store{root: root}, nil
}

// Path returns the path of a SHA256 object, rejecting non-content keys.
func (s *Store) Path(hash string) (string, error) {
	raw, err := hex.DecodeString(hash)
	if err != nil || len(raw) != sha256.Size || hash != strings.ToLower(hash) {
		return "", errors.New("blobstore: invalid SHA256 key")
	}
	return filepath.Join(s.root, hash[:2], hash), nil
}

// Open opens an original file without loading it into memory.
func (s *Store) Open(hash string) (*os.File, error) {
	p, err := s.Path(hash)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

// Put streams and hashes an original file, then publishes a durable immutable
// object. A failed read removes only this call's incomplete temporary file.
func (s *Store) Put(ctx context.Context, r io.Reader) (object Object, err error) {
	if err := ctx.Err(); err != nil {
		return object, err
	}
	temp, err := os.CreateTemp(s.root, ".upload-")
	if err != nil {
		return object, fmt.Errorf("blobstore: create temporary file: %w", err)
	}
	defer func() { _ = temp.Close(); _ = os.Remove(temp.Name()) }()
	digest := sha256.New()
	size, err := io.CopyBuffer(io.MultiWriter(temp, digest), contextReader{ctx, r}, make([]byte, 64<<10))
	if err != nil {
		return object, fmt.Errorf("blobstore: write content: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return object, err
	}
	if err := temp.Sync(); err != nil {
		return object, fmt.Errorf("blobstore: sync content: %w", err)
	}
	hash := hex.EncodeToString(digest.Sum(nil))
	target, _ := s.Path(hash)
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return object, err
	}
	if err := syncDir(s.root); err != nil {
		return object, err
	}
	if err := os.Link(temp.Name(), target); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return object, fmt.Errorf("blobstore: publish content: %w", err)
		}
		// A concurrent or recovered publication must contain the same complete bytes.
		existing, err := s.Open(hash)
		if err != nil {
			return object, err
		}
		actual := sha256.New()
		n, copyErr := io.CopyBuffer(actual, contextReader{ctx, existing}, make([]byte, 64<<10))
		syncErr := existing.Sync()
		closeErr := existing.Close()
		if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
			return object, err
		}
		if n != size || hex.EncodeToString(actual.Sum(nil)) != hash {
			return object, errors.New("blobstore: existing content failed integrity verification")
		}
	}
	if err := syncDir(filepath.Dir(target)); err != nil {
		return object, err
	}
	return Object{SHA256: hash, Size: size}, nil
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("blobstore: open directory for sync: %w", err)
	}
	return errors.Join(dir.Sync(), dir.Close())
}
