// Package release validates immutable executable release bundles.
package release

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const MaxManifestBytes = 1 << 20
const MaxArtifactBytes int64 = 1 << 30

var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

type Range struct {
	Min int64 `json:"min"`
	Max int64 `json:"max"`
}

func (r Range) Valid() bool           { return r.Min >= 0 && r.Max >= r.Min }
func (r Range) Contains(n int64) bool { return r.Valid() && n >= r.Min && n <= r.Max }

// Storage ranges name readable formats. Max also names the binary's current
// migration target, so an installed Version can retain it without reading data.
type Storage struct {
	ServerSchema  Range `json:"server_schema"`
	RunnerJournal Range `json:"runner_journal"`
}
type Artifact struct {
	Role   string `json:"role"`
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type Manifest struct {
	Format    int        `json:"format"`
	ReleaseID string     `json:"release_id"`
	Protocol  Range      `json:"protocol"`
	Storage   Storage    `json:"storage"`
	Artifacts []Artifact `json:"artifacts"`
}
type Bundle struct {
	Dir            string
	Manifest       Manifest
	ManifestSHA256 string
	ManifestBytes  []byte
}

func ValidID(s string) bool { return identifier.MatchString(s) && s != "." && s != ".." }
func ValidSHA(s string) bool {
	if len(s) != 64 || strings.ToLower(s) != s {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
func (m Manifest) Validate() error {
	if m.Format != 1 || !ValidID(m.ReleaseID) || !m.Protocol.Valid() || !m.Storage.ServerSchema.Valid() || !m.Storage.RunnerJournal.Valid() {
		return errors.New("release: invalid manifest identity or compatibility range")
	}
	if len(m.Artifacts) == 0 || len(m.Artifacts) > 64 {
		return errors.New("release: invalid artifact count")
	}
	seen := map[string]bool{}
	paths := map[string]bool{}
	for _, a := range m.Artifacts {
		if a.Role != "server" && a.Role != "runner" {
			return errors.New("release: unknown artifact role")
		}
		if !ValidID(a.OS) || !ValidID(a.Arch) || a.Size <= 0 || a.Size > MaxArtifactBytes || !ValidSHA(a.SHA256) || !safeRelative(a.Path) {
			return errors.New("release: invalid artifact metadata")
		}
		key := a.Role + "/" + a.OS + "/" + a.Arch
		if seen[key] || paths[a.Path] {
			return errors.New("release: duplicate artifact target or path")
		}
		seen[key] = true
		paths[a.Path] = true
	}
	return nil
}
func safeRelative(s string) bool {
	if s == "" || strings.Contains(s, "\\") || strings.ContainsRune(s, 0) || filepath.IsAbs(s) || filepath.Clean(s) != s {
		return false
	}
	for _, part := range strings.Split(s, "/") {
		if part == "." || part == ".." || part == "" {
			return false
		}
	}
	return true
}

// RegularPath rejects symlinks at every component, including the bundle root.
func RegularPath(root, relative string) (string, error) {
	if !safeRelative(relative) {
		return "", errors.New("release: unsafe relative path")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	// Resolve the containing parent once, but never a symlink supplied as the root.
	info, err := os.Lstat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("release: bundle root is not a regular directory")
	}
	p := abs
	parts := strings.Split(relative, "/")
	for i, v := range parts {
		p = filepath.Join(p, v)
		info, err = os.Lstat(p)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("release: symlink artifact rejected")
		}
		if i < len(parts)-1 && !info.IsDir() {
			return "", errors.New("release: invalid artifact parent")
		}
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("release: artifact is not a regular file")
	}
	return p, nil
}
func Load(dir string) (*Bundle, error) {
	path, err := RegularPath(dir, "manifest.json")
	if err != nil {
		return nil, fmt.Errorf("release: manifest path: %w", err)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, MaxManifestBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxManifestBytes {
		return nil, errors.New("release: manifest too large")
	}
	var m Manifest
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&m); err != nil {
		return nil, fmt.Errorf("release: decode manifest: %w", err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, errors.New("release: trailing manifest data")
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	b := &Bundle{Dir: dir, Manifest: m, ManifestBytes: raw, ManifestSHA256: hex.EncodeToString(sum[:])}
	for _, a := range m.Artifacts {
		if err := b.Verify(a); err != nil {
			return nil, err
		}
	}
	return b, nil
}
func (b *Bundle) Artifact(role, goos, arch string) (Artifact, error) {
	for _, a := range b.Manifest.Artifacts {
		if a.Role == role && a.OS == goos && a.Arch == arch {
			return a, nil
		}
	}
	return Artifact{}, errors.New("release: no artifact for this role and platform")
}
func (b *Bundle) Verify(a Artifact) error {
	p, err := RegularPath(b.Dir, a.Path)
	if err != nil {
		return fmt.Errorf("release: artifact path: %w", err)
	}
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() != a.Size {
		return errors.New("release: artifact size mismatch")
	}
	sum := sha256.New()
	n, err := io.Copy(sum, io.LimitReader(f, a.Size+1))
	if err != nil {
		return err
	}
	if n != a.Size || hex.EncodeToString(sum.Sum(nil)) != a.SHA256 {
		return errors.New("release: artifact hash mismatch")
	}
	return nil
}
