package release

import (
	"debug/buildinfo"
	"fmt"
)

// SourceIdentity is read from executable bytes already pinned by the manifest.
// It is not a source declaration supplied by a remote worker or a new manifest
// field, so old runners keep their original immutable bundle format.
type SourceIdentity struct {
	Commit   string
	Modified bool
}

func ExecutableSource(path string) (SourceIdentity, error) {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return SourceIdentity{}, fmt.Errorf("release: read executable source: %w", err)
	}
	var out SourceIdentity
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			out.Commit = s.Value
		case "vcs.modified":
			out.Modified = s.Value == "true"
		}
	}
	return out, nil
}
