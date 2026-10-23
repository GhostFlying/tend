//go:build !linux

package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"time"
)

type LinuxProcess struct{}

func hashFile(path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		return "", e
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func (LinuxProcess) Inspect(context.Context, int) (Identity, error) {
	return Identity{}, ErrUnsupported
}
func (LinuxProcess) Find(context.Context, string, string) ([]Identity, error) {
	return nil, ErrUnsupported
}
func (LinuxProcess) Start(context.Context, string, Config, map[string]string) (Identity, error) {
	return Identity{}, ErrUnsupported
}
func (LinuxProcess) Stop(context.Context, Identity, time.Duration) error { return ErrUnsupported }
