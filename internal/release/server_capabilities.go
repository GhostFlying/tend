package release

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"time"
)

const ServerCapabilityManagerPark = "manager-upgrade-park-v1"
const MaxServerCapabilitiesBytes = 64 << 10

// ServerCapabilities is a read-only self-report bound to the executed artifact.
type ServerCapabilities struct {
	BinarySHA256 string   `json:"binary_sha256"`
	Capabilities []string `json:"capabilities"`
}

func (r ServerCapabilities) Validate(expected string) error {
	if !ValidSHA(expected) || r.BinarySHA256 != expected || len(r.Capabilities) > 64 {
		return errors.New("release: capability executable identity mismatch")
	}
	seen := map[string]bool{}
	for _, capability := range r.Capabilities {
		if !ValidID(capability) || seen[capability] {
			return errors.New("release: invalid server capabilities")
		}
		seen[capability] = true
	}
	return nil
}
func (r ServerCapabilities) Supports(capability string) bool {
	return slices.Contains(r.Capabilities, capability)
}

// CurrentServerCapabilities reads the kernel's actual executable, rather than a
// pathname that may already refer to another installed version.
func CurrentServerCapabilities() (ServerCapabilities, error) {
	if runtime.GOOS != "linux" {
		return ServerCapabilities{}, errors.New("release: exact executable capability proof requires Linux")
	}
	f, err := os.Open("/proc/self/exe")
	if err != nil {
		return ServerCapabilities{}, err
	}
	defer f.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(f, MaxArtifactBytes+1))
	if err != nil || n > MaxArtifactBytes {
		return ServerCapabilities{}, errors.New("release: cannot hash the running executable")
	}
	return ServerCapabilities{BinarySHA256: hex.EncodeToString(hash.Sum(nil)), Capabilities: []string{ServerCapabilityManagerPark}}, nil
}

// ProbeServerCapabilities never falls back to running a candidate normally.
func ProbeServerCapabilities(ctx context.Context, path, expected string) (ServerCapabilities, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if !ValidSHA(expected) || !filepath.IsAbs(path) {
		return ServerCapabilities{}, errors.New("release: invalid capability artifact identity")
	}
	source, err := os.Open(path)
	if err != nil {
		return ServerCapabilities{}, err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return ServerCapabilities{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxArtifactBytes {
		return ServerCapabilities{}, errors.New("release: invalid capability artifact")
	}
	directory, err := os.MkdirTemp("", "tend-server-capabilities-")
	if err != nil {
		return ServerCapabilities{}, err
	}
	defer os.RemoveAll(directory)
	pinned := filepath.Join(directory, "server")
	target, err := os.OpenFile(pinned, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return ServerCapabilities{}, err
	}
	sum := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(sum, target), io.LimitReader(capabilityReader{ctx, source}, MaxArtifactBytes+1))
	closeErr := target.Close()
	if copyErr != nil {
		return ServerCapabilities{}, copyErr
	}
	if closeErr != nil {
		return ServerCapabilities{}, closeErr
	}
	if n > MaxArtifactBytes || hex.EncodeToString(sum.Sum(nil)) != expected {
		return ServerCapabilities{}, errors.New("release: capability artifact changed")
	}
	if err := os.Chmod(pinned, 0o700); err != nil {
		return ServerCapabilities{}, err
	}
	cmd := exec.CommandContext(ctx, pinned, "--capabilities")
	cmd.Env = []string{}
	var output capabilityOutput
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return ServerCapabilities{}, errors.New("release: server capability probe failed")
	}
	var report ServerCapabilities
	decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return report, errors.New("release: invalid server capability response")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return report, errors.New("release: trailing server capability response")
	}
	return report, report.Validate(expected)
}

type capabilityOutput struct{ bytes.Buffer }

func (w *capabilityOutput) Write(p []byte) (int, error) {
	if w.Len()+len(p) > MaxServerCapabilitiesBytes {
		return 0, errors.New("release: capability response too large")
	}
	return w.Buffer.Write(p)
}

type capabilityReader struct {
	ctx context.Context
	io.Reader
}

func (r capabilityReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}
