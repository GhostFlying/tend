package webhookauth

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestImmutablePrivateKeyStaging(t *testing.T) {
	files := Files{Dir: filepath.Join(t.TempDir(), "keys")}
	id := protocol.NewULIDGen(func() time.Time { return time.Unix(1, 0) }, nil).New(protocol.PrefixRequest)
	key := bytes.Repeat([]byte("a"), 32)
	if err := files.Put(t.Context(), id, key); err != nil {
		t.Fatal(err)
	}
	if err := files.Put(t.Context(), id, key); err != nil {
		t.Fatal("same staging retry", err)
	}
	if err := files.Put(t.Context(), id, bytes.Repeat([]byte("b"), 32)); protocol.CodeOf(err) != protocol.CodeConflictPayload {
		t.Fatal("overwrote private key", err)
	}
	got, err := files.Get(t.Context(), id)
	if err != nil || !bytes.Equal(got, key) {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(files.Dir, id))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
	if err = files.Put(t.Context(), "../escape", key); err == nil {
		t.Fatal("path escape accepted")
	}
}
