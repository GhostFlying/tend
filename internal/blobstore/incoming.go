package blobstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ReceivePart stages a retryable transfer separately from committed originals.
// The caller owns the key (including source and content digest). A completed
// stage remains until the caller has committed the business record.
func (s *Store) ReceivePart(ctx context.Context, key, digest string, size, offset int64, data []byte) (int64, []byte, error) {
	if offset < 0 || size <= offset || len(data) == 0 || int64(len(data)) > size-offset {
		return 0, nil, errors.New("blobstore: invalid part range")
	}
	s.incomingMu.Lock()
	defer s.incomingMu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, nil, err
	}
	path := s.incomingPath(key)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return 0, nil, err
	}
	if err := syncDir(s.root); err != nil {
		return 0, nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return 0, nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, nil, err
	}
	n := info.Size()
	if n > size || offset > n {
		return 0, nil, fmt.Errorf("blobstore: invalid part position %d at %d", offset, n)
	}
	overlap := min(int64(len(data)), n-offset)
	if overlap > 0 {
		prior := make([]byte, overlap)
		if _, err = f.ReadAt(prior, offset); err != nil {
			return 0, nil, err
		}
		if !bytes.Equal(prior, data[:overlap]) {
			return 0, nil, errors.New("blobstore: conflicting part")
		}
	}
	if overlap < int64(len(data)) {
		if _, err = f.WriteAt(data[overlap:], n); err != nil {
			return 0, nil, err
		}
		n = offset + int64(len(data))
	}
	if err = f.Sync(); err != nil {
		return 0, nil, err
	}
	if err = syncDir(dir); err != nil {
		return 0, nil, err
	}
	if n < size {
		return n, nil, nil
	}
	raw, err := io.ReadAll(contextReader{ctx, io.NewSectionReader(f, 0, size)})
	if err != nil {
		return 0, nil, err
	}
	sum := sha256.Sum256(raw)
	if int64(len(raw)) != size || hex.EncodeToString(sum[:]) != digest {
		if err := os.Remove(path); err != nil {
			return 0, nil, err
		}
		return 0, nil, errors.New("blobstore: event transfer digest mismatch")
	}
	return n, raw, nil
}

func (s *Store) incomingPath(key string) string {
	id := sha256.Sum256([]byte(key))
	return filepath.Join(s.root, "incoming-events", hex.EncodeToString(id[:]))
}

func (s *Store) ForgetTransfer(key string) error {
	s.incomingMu.Lock()
	defer s.incomingMu.Unlock()
	err := os.Remove(s.incomingPath(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
