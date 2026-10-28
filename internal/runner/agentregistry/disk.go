package agentregistry

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"
)

type FaultHook func(step string) error

type Options struct {
	Now           func() time.Time
	NewSnapshotID func() string
	Before        FaultHook
	After         FaultHook
}

// Storage belongs to the trusted owner. Parse its versioned structure without
// requiring the original whitespace or object-key ordering.
func strictDecode(raw []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return fail("invalid_storage_json")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fail("invalid_storage_json")
	}
	return nil
}

func (r *Registry) boundary(ctx context.Context, step string, f func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.opts.Before != nil {
		if err := r.opts.Before(step); err != nil {
			return err
		}
	}
	if err := f(); err != nil {
		return err
	}
	if r.opts.After != nil {
		return r.opts.After(step)
	}
	return nil
}
func (r *Registry) read(name string) ([]byte, error) {
	return os.ReadFile(filepath.Join(r.path, name))
}
func (r *Registry) syncDirectory(ctx context.Context, name, label string) error {
	return r.boundary(ctx, label, func() error {
		f, err := os.Open(filepath.Join(r.path, name))
		if err != nil {
			return err
		}
		defer f.Close()
		return f.Sync()
	})
}

// Both temporary file and target are in the same trusted local directory.
// After attempting rename, failure can mean publication happened.
func (r *Registry) persist(ctx context.Context, target string, raw []byte, label string) (bool, error) {
	dir := filepath.Dir(filepath.Join(r.path, target))
	f, err := os.CreateTemp(dir, ".stage-*")
	if err != nil {
		return false, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = r.boundary(ctx, label+".write", func() error {
		n, e := f.Write(raw)
		if e == nil && n != len(raw) {
			e = io.ErrShortWrite
		}
		return e
	}); err != nil {
		return false, err
	}
	if err = r.boundary(ctx, label+".file_sync", f.Sync); err != nil {
		return false, err
	}
	if err = r.boundary(ctx, label+".close", f.Close); err != nil {
		return false, err
	}
	if err = r.boundary(ctx, label+".rename", func() error {
		return os.Rename(f.Name(), filepath.Join(r.path, target))
	}); err != nil {
		return true, err
	}
	subdir := "."
	if label == "snapshot" {
		subdir = "snapshots"
	}
	if err = r.syncDirectory(ctx, subdir, label+".dir_sync"); err != nil {
		return true, err
	}
	return false, nil
}
