// Package webhookauth stores immutable private webhook keys outside the ledger.
package webhookauth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/GhostFlying/tend/internal/protocol"
)

type Files struct{ Dir string }

func (f Files) Get(ctx context.Context, id string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !protocol.ValidID(protocol.PrefixRequest, id) {
		return nil, fmt.Errorf("invalid key reference")
	}
	return os.ReadFile(filepath.Join(f.Dir, id))
}

// Put stages a key under the original owner request identity. Atomic link makes
// only complete files visible, and retries never replace an existing credential.
func (f Files) Put(ctx context.Context, id string, key []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !protocol.ValidID(protocol.PrefixRequest, id) || len(key) < 32 || len(key) > 4096 {
		return protocol.Errorf(protocol.CodeInvalidRequest, "key requires a request reference and 32..4096 bytes")
	}
	if err := os.MkdirAll(f.Dir, 0o700); err != nil {
		return fmt.Errorf("create private key directory: %w", err)
	}
	if err := syncDirectory(filepath.Dir(f.Dir)); err != nil {
		return err
	}
	file, err := os.CreateTemp(f.Dir, ".stage-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err = file.Write(key); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	err = os.Link(file.Name(), filepath.Join(f.Dir, id))
	if errors.Is(err, os.ErrExist) {
		old, e := f.Get(ctx, id)
		if e != nil {
			return e
		}
		if !bytes.Equal(old, key) {
			return protocol.Errorf(protocol.CodeConflictPayload, "key reference already has different content")
		}
		return syncDirectory(f.Dir)
	}
	if err != nil {
		return err
	}
	return syncDirectory(f.Dir)
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open private key directory: %w", err)
	}
	return errors.Join(dir.Sync(), dir.Close())
}
