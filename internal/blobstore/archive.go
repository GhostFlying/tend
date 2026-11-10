package blobstore

import (
	"bufio"
	"bytes"
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

// Window identifies immutable bytes of a durable append-only original. Appending
// an original never changes a committed prefix. SHA256 verifies this exact range,
// not the current length of the growing original.
type Window struct {
	Archive string `json:"archive"`
	Offset  int64  `json:"offset"`
	Length  int64  `json:"length"`
	SHA256  string `json:"sha256"`
}

func (s *Store) archivePath(id string) (string, error) {
	if strings.HasPrefix(id, "object:") {
		return s.Path(strings.TrimPrefix(id, "object:"))
	}
	if b, e := hex.DecodeString(id); e != nil || len(b) != sha256.Size || hex.EncodeToString(b) != id {
		return "", errors.New("blobstore: invalid archive identity")
	}
	return filepath.Join(s.root, "archives", id), nil
}

// AppendOriginal retains one observed stream in a single durable original rather
// than creating an object per event. The store has one owning server process;
// concurrent writers in that process are serialized independently of SQLite.
func (s *Store) AppendOriginal(ctx context.Context, stream string, body []byte) (Window, error) {
	if e := ctx.Err(); e != nil {
		return Window{}, e
	}
	streamHash := sha256.Sum256([]byte(stream))
	id := hex.EncodeToString(streamHash[:])
	digest := sha256.Sum256(body)
	s.archiveMu.Lock()
	defer s.archiveMu.Unlock()
	p, _ := s.archivePath(id)
	if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		return Window{}, e
	}
	if e := syncDir(s.root); e != nil {
		return Window{}, e
	}
	f, e := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return Window{}, e
	}
	defer f.Close()
	at, e := f.Seek(0, io.SeekEnd)
	if e != nil {
		return Window{}, e
	}
	if _, e = f.Write(body); e != nil {
		return Window{}, e
	}
	if e = ctx.Err(); e != nil {
		return Window{}, e
	}
	if e = f.Sync(); e != nil {
		return Window{}, e
	}
	if e = syncDir(filepath.Dir(p)); e != nil {
		return Window{}, e
	}
	return Window{Archive: id, Offset: at, Length: int64(len(body)), SHA256: hex.EncodeToString(digest[:])}, nil
}

// ReadWindow checks length and digest before returning any bytes to the caller.
func (s *Store) ReadWindow(ctx context.Context, w Window) ([]byte, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if w.Offset < 0 || w.Length < 0 || w.Length > int64(int(^uint(0)>>1)) {
		return nil, errors.New("blobstore: invalid archive window")
	}
	p, e := s.archivePath(w.Archive)
	if e != nil {
		return nil, e
	}
	if b, e := hex.DecodeString(w.SHA256); e != nil || len(b) != sha256.Size {
		return nil, errors.New("blobstore: invalid window SHA256")
	}
	f, e := os.Open(p)
	if e != nil {
		return nil, fmt.Errorf("blobstore: open original: %w", e)
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return nil, e
	}
	if w.Length > info.Size() || w.Offset > info.Size()-w.Length {
		return nil, errors.New("blobstore: original window exceeds retained file")
	}
	b := make([]byte, int(w.Length))
	if _, e = f.ReadAt(b, w.Offset); e != nil {
		return nil, fmt.Errorf("blobstore: read original window: %w", e)
	}
	h := sha256.Sum256(b)
	if hex.EncodeToString(h[:]) != w.SHA256 {
		return nil, errors.New("blobstore: original window integrity mismatch")
	}
	return b, ctx.Err()
}

// CopyOriginalPrefix copies the snapshot's referenced immutable prefix. Later
// appends are deliberately excluded. Restore uses the same relative identity.
func (s *Store) CopyOriginalPrefix(ctx context.Context, dst *Store, id string, length int64) error {
	if length < 0 {
		return errors.New("blobstore: invalid original prefix")
	}
	p, e := s.archivePath(id)
	if e != nil {
		return e
	}
	source, e := os.Open(p)
	if e != nil {
		return e
	}
	defer source.Close()
	target, e := dst.archivePath(id)
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(target), 0700); e != nil {
		return e
	}
	out, e := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	_, copyErr := io.CopyN(out, contextReader{ctx, source}, length)
	e = errors.Join(copyErr, out.Sync(), out.Close())
	if e != nil {
		return e
	}
	return errors.Join(syncDir(filepath.Dir(target)), syncDir(dst.root))
}

// ReadTextWindow preserves character-based cursors with bounded memory. The
// complete byte range is hashed before any selected text is returned.
func (s *Store) ReadTextWindow(ctx context.Context, w Window, offset, limit int64) (string, int64, error) {
	if offset < 0 || limit < 0 || w.Offset < 0 || w.Length < 0 {
		return "", 0, errors.New("blobstore: invalid text window")
	}
	p, e := s.archivePath(w.Archive)
	if e != nil {
		return "", 0, e
	}
	f, e := os.Open(p)
	if e != nil {
		return "", 0, e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return "", 0, e
	}
	if w.Length > info.Size() || w.Offset > info.Size()-w.Length {
		return "", 0, errors.New("blobstore: original text window exceeds retained file")
	}
	digest := sha256.New()
	source := io.NewSectionReader(f, w.Offset, w.Length)
	reader := bufio.NewReader(io.TeeReader(contextReader{ctx, source}, digest))
	var selected strings.Builder
	var count int64
	for {
		r, _, err := reader.ReadRune()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", 0, err
		}
		if count >= offset && count-offset < limit {
			selected.WriteRune(r)
		}
		count++
	}
	if source.Size() != w.Length {
		return "", 0, errors.New("blobstore: original window length mismatch")
	}
	if hex.EncodeToString(digest.Sum(nil)) != w.SHA256 {
		return "", 0, errors.New("blobstore: original window integrity mismatch")
	}
	return selected.String(), count, nil
}

// AppendOriginalAt resumes one provider-owned file generation without rewriting
// a committed prefix. A lost database reply or partial publication is retried
// with the original offset and bytes; conflicting bytes are a real error.
func (s *Store) AppendOriginalAt(ctx context.Context, stream string, offset int64, data []byte) (Window, error) {
	if offset < 0 || int64(len(data)) > int64(^uint64(0)>>1)-offset {
		return Window{}, errors.New("blobstore: invalid append offset")
	}
	if err := ctx.Err(); err != nil {
		return Window{}, err
	}
	streamHash := sha256.Sum256([]byte(stream))
	id := hex.EncodeToString(streamHash[:])
	digest := sha256.Sum256(data)
	s.archiveMu.Lock()
	defer s.archiveMu.Unlock()
	path, _ := s.archivePath(id)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return Window{}, err
	}
	if err := syncDir(s.root); err != nil {
		return Window{}, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return Window{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return Window{}, err
	}
	if info.Size() < offset {
		return Window{}, errors.New("blobstore: original append has a gap")
	}
	overlap := min(int64(len(data)), info.Size()-offset)
	if overlap > 0 {
		old := make([]byte, int(overlap))
		if _, err = f.ReadAt(old, offset); err != nil {
			return Window{}, err
		}
		if !bytes.Equal(old, data[:overlap]) {
			return Window{}, errors.New("blobstore: original append conflicts with retained bytes")
		}
	}
	if overlap < int64(len(data)) {
		if _, err = f.Seek(offset+overlap, io.SeekStart); err != nil {
			return Window{}, err
		}
		if _, err = f.Write(data[overlap:]); err != nil {
			return Window{}, err
		}
	}
	if err = f.Sync(); err != nil {
		return Window{}, err
	}
	if err = syncDir(filepath.Dir(path)); err != nil {
		return Window{}, err
	}
	return Window{Archive: id, Offset: offset, Length: int64(len(data)), SHA256: hex.EncodeToString(digest[:])}, nil
}

type originalSection struct {
	*io.SectionReader
	file *os.File
}

func (s *originalSection) Close() error { return s.file.Close() }

// OpenOriginalRange is used only after retained chunk checksums are verified.
func (s *Store) OpenOriginalRange(id string, offset, length int64) (io.ReadCloser, error) {
	if offset < 0 || length < 0 {
		return nil, errors.New("blobstore: invalid original range")
	}
	path, err := s.archivePath(id)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if length > info.Size() || offset > info.Size()-length {
		f.Close()
		return nil, errors.New("blobstore: original range exceeds retained file")
	}
	return &originalSection{SectionReader: io.NewSectionReader(f, offset, length), file: f}, nil
}
