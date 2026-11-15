package blobstore

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenStoreReadsExistingWithoutInitializingMissingRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "objects")
	if _, err := OpenStore(root); err == nil {
		t.Fatal("missing store opened")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("read-only open created root", err)
	}
	writer, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	object, err := writer.Put(t.Context(), bytes.NewBufferString("durable"))
	if err != nil {
		t.Fatal(err)
	}
	reader, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	f, err := reader.Open(object.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	body, err := io.ReadAll(f)
	if err != nil || string(body) != "durable" {
		t.Fatal(string(body), err)
	}
}
