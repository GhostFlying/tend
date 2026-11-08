package release

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
)

// Component is an independently installed executable from the same release.
// It does not change the four-component distribution or service wire manifest.
type Component struct {
	Format    int      `json:"format"`
	ReleaseID string   `json:"release_id"`
	Commit    string   `json:"commit"`
	State     Range    `json:"state"`
	Artifact  Artifact `json:"artifact"`
}

func (c Component) Validate() error {
	a := c.Artifact
	commit, err := hex.DecodeString(c.Commit)
	if c.Format != 1 || !ValidID(c.ReleaseID) || len(commit) != 20 || err != nil || c.Commit != hex.EncodeToString(commit) || !c.State.Valid() || a.Role != "maintainer" || a.OS != "linux" || (a.Arch != "amd64" && a.Arch != "arm64") || !safeRelative(a.Path) || a.Size <= 0 || a.Size > MaxArtifactBytes || !ValidSHA(a.SHA256) {
		return errors.New("release: invalid standalone component")
	}
	return nil
}

// LoadService selects the manifest family from the fixed managed role.
func LoadService(dir, role string) (*Bundle, error) {
	if role == "maintainer" {
		return LoadComponent(dir)
	}
	return Load(dir)
}

func LoadComponent(dir string) (*Bundle, error) {
	path, err := RegularPath(dir, "component.json")
	if err != nil {
		return nil, err
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
		return nil, errors.New("release: component manifest too large")
	}
	var c Component
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return nil, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, errors.New("release: trailing component data")
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	b := &Bundle{Dir: dir, Manifest: Manifest{Format: 1, ReleaseID: c.ReleaseID, Protocol: Range{Min: 1, Max: 1}, Storage: Storage{ServerSchema: c.State}, Artifacts: []Artifact{c.Artifact}}, ManifestBytes: raw, ManifestSHA256: hex.EncodeToString(sum[:])}
	if err := b.Verify(c.Artifact); err != nil {
		return nil, err
	}
	p, err := RegularPath(dir, c.Artifact.Path)
	if err != nil {
		return nil, err
	}
	source, err := ExecutableSource(p)
	if err != nil || source.Modified || source.Commit != c.Commit {
		return nil, errors.New("release: component executable source differs")
	}
	return b, nil
}
