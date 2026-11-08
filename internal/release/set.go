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
)

// Set binds the operator tools to the unchanged service manifest. Old bundles
// remain readable; a present set is mandatory evidence, never an optional hint.
type Set struct {
	Format         int        `json:"format"`
	ReleaseID      string     `json:"release_id"`
	Commit         string     `json:"commit"`
	OS             string     `json:"os"`
	Arch           string     `json:"arch"`
	ManifestSHA256 string     `json:"manifest_sha256"`
	Artifacts      []Artifact `json:"artifacts"`
}

func loadSet(b *Bundle) error {
	_, err := os.Lstat(b.Dir + "/release-set.json")
	if os.IsNotExist(err) {
		return nil
	}
	path, err := RegularPath(b.Dir, "release-set.json")
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, MaxManifestBytes+1))
	if err != nil || len(raw) > MaxManifestBytes {
		return errors.New("release: cannot read release set")
	}
	var set Set
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&set); err != nil {
		return fmt.Errorf("release: decode release set: %w", err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("release: trailing release set data")
	}
	if err := set.Validate(b); err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	b.Set, b.SetBytes, b.SetSHA256 = &set, raw, hex.EncodeToString(sum[:])
	return nil
}

func (s Set) Validate(b *Bundle) error {
	commit, err := hex.DecodeString(s.Commit)
	if s.Format != 1 || len(commit) != 20 || err != nil || s.Commit != hex.EncodeToString(commit) || s.ReleaseID != b.Manifest.ReleaseID || s.ManifestSHA256 != b.ManifestSHA256 || s.OS != "linux" || (s.Arch != "amd64" && s.Arch != "arm64") || len(s.Artifacts) != 4 || len(b.Manifest.Artifacts) != 2 {
		return errors.New("release: invalid release set identity")
	}
	roles, paths := map[string]bool{}, map[string]bool{}
	for _, a := range s.Artifacts {
		if (a.Role != "server" && a.Role != "runner" && a.Role != "updater" && a.Role != "cli") || roles[a.Role] || paths[a.Path] || !safeRelative(a.Path) || a.Path == "manifest.json" || a.Path == "release-set.json" || a.Size <= 0 || a.Size > MaxArtifactBytes || !ValidSHA(a.SHA256) || a.OS != s.OS || a.Arch != s.Arch {
			return errors.New("release: invalid release set artifact")
		}
		roles[a.Role], paths[a.Path] = true, true
		if a.Role == "server" || a.Role == "runner" {
			service, err := b.Artifact(a.Role, a.OS, a.Arch)
			if err != nil || service != a {
				return errors.New("release: service differs from release set")
			}
		}
		if err := b.Verify(a); err != nil {
			return err
		}
	}
	return nil
}

// Artifacts includes operator tools only when their complete set was verified.
func (b *Bundle) Artifacts() []Artifact {
	if b.Set != nil {
		return b.Set.Artifacts
	}
	return b.Manifest.Artifacts
}

func (b *Bundle) SetArtifact(role, goos, arch string) (Artifact, error) {
	if b.Set != nil {
		for _, a := range b.Set.Artifacts {
			if a.Role == role && a.OS == goos && a.Arch == arch {
				return a, nil
			}
		}
	}
	return Artifact{}, errors.New("release: complete release set is required")
}
