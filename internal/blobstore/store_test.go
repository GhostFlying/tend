package blobstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type repeatingReader struct {
	remaining  int64
	maxRequest int
}

func (r *repeatingReader) Read(p []byte) (int, error) {
	r.maxRequest = max(r.maxRequest, len(p))
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := int(min(int64(len(p)), r.remaining))
	for i := range n {
		p[i] = 'x'
	}
	r.remaining -= int64(n)
	return n, nil
}

func TestStreamBeyondOldLimitAndReopen(t *testing.T) {
	root := filepath.Join(t.TempDir(), "files")
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	const size = 25<<20 + 19
	input := &repeatingReader{remaining: size}
	object, err := s.Put(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, &repeatingReader{remaining: size}); err != nil {
		t.Fatal(err)
	}
	if object.Size != size || object.SHA256 != hex.EncodeToString(digest.Sum(nil)) || input.maxRequest > 64<<10 {
		t.Fatalf("stream object=%+v maxRead=%d", object, input.maxRequest)
	}
	reopened, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	file, err := reopened.Open(object.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var window [5]byte
	if n, err := file.ReadAt(window[:], size-5); err != nil || n != 5 || string(window[:]) != "xxxxx" {
		t.Fatalf("original last bytes=%q %d %v", window, n, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".upload-") {
			t.Fatal("temporary file retained")
		}
	}
}

type failedReader struct{}

func (failedReader) Read([]byte) (int, error) { return 0, errors.New("interrupted source") }

func TestInterruptedAndCancelledUploadsPublishNothing(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "files"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(context.Background(), io.MultiReader(strings.NewReader("prefix"), failedReader{})); err == nil {
		t.Fatal("failed source succeeded")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Put(ctx, strings.NewReader("cancelled")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("incomplete objects retained: %v", entries)
	}
}

func TestConcurrentPublicationAndCorruptionDetection(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "files"))
	if err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("same bytes"), 8192)
	digest := sha256.Sum256(data)
	key := hex.EncodeToString(digest[:])
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			object, err := s.Put(context.Background(), bytes.NewReader(data))
			if err != nil || object.SHA256 != key || object.Size != int64(len(data)) {
				t.Errorf("concurrent publication=%+v %v", object, err)
			}
		})
	}
	group.Wait()
	path, err := s.Path(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("disk corruption"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(context.Background(), bytes.NewReader(data)); err == nil {
		t.Fatal("corrupt existing original silently replaced or accepted")
	}
	existing, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(existing) != "disk corruption" {
		t.Fatal("existing object overwritten")
	}
}

func TestEmptyContentAndOnlySHAPaths(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "files"))
	if err != nil {
		t.Fatal(err)
	}
	object, err := s.Put(context.Background(), bytes.NewReader(nil))
	if err != nil || object.Size != 0 {
		t.Fatalf("empty original=%+v %v", object, err)
	}
	for _, key := range []string{"../../outside", strings.ToUpper(object.SHA256), object.SHA256[:63], ""} {
		if _, err := s.Path(key); err == nil {
			t.Fatalf("invalid key accepted: %q", key)
		}
	}
}

func TestRelativeRootProducesAbsoluteOriginalPath(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, filepath.Join(t.TempDir(), "relative-files"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(relative)
	if err != nil {
		t.Fatal(err)
	}
	object, err := s.Put(context.Background(), strings.NewReader("absolute path"))
	if err != nil {
		t.Fatal(err)
	}
	path, err := s.Path(object.SHA256)
	if err != nil || !filepath.IsAbs(path) {
		t.Fatalf("original path=%q %v", path, err)
	}
}
