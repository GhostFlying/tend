package release

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// MergeTo stages a fleet bundle from both verified platform distributions.
// Keeping the complete service manifest lets the existing coordinator select
// artifacts for heterogeneous hosts without changing its compatibility rules.
func MergeTo(amd64, arm64, target string) (*Bundle, error) {
	a, err := Load(amd64)
	if err != nil {
		return nil, err
	}
	b, err := Load(arm64)
	if err != nil {
		return nil, err
	}
	if a.Set == nil || b.Set == nil || a.Set.Arch != "amd64" || b.Set.Arch != "arm64" || a.Set.Commit != b.Set.Commit || a.Manifest.ReleaseID != b.Manifest.ReleaseID || a.Manifest.Protocol != b.Manifest.Protocol || a.Manifest.Storage != b.Manifest.Storage {
		return nil, errors.New("release: platform distributions have different source or compatibility")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(target), ".fleet-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	m := a.Manifest
	m.Artifacts = nil
	s := *a.Set
	s.Arch, s.Artifacts = "multi", nil
	for _, source := range []*Bundle{a, b} {
		for _, artifact := range source.Artifacts() {
			path, err := RegularPath(source.Dir, artifact.Path)
			if err != nil {
				return nil, err
			}
			artifact.Path = filepath.Join("linux-"+artifact.Arch, artifact.Path)
			to := filepath.Join(tmp, artifact.Path)
			if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
				return nil, err
			}
			if err := copyFile(path, to, 0o755); err != nil {
				return nil, err
			}
			s.Artifacts = append(s.Artifacts, artifact)
			if artifact.Role == "server" || artifact.Role == "runner" {
				m.Artifacts = append(m.Artifacts, artifact)
			}
		}
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(tmp, "manifest.json"), raw, 0o600); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	s.ManifestSHA256 = hex.EncodeToString(sum[:])
	raw, err = json.Marshal(s)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(tmp, "release-set.json"), raw, 0o600); err != nil {
		return nil, err
	}
	merged, err := Load(tmp)
	if err != nil {
		return nil, err
	}
	return merged.CopyTo(target)
}
