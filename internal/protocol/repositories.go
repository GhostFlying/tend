package protocol

import (
	"crypto/sha256"
	"encoding/hex"
)

// RepositoryRegistration is the owner's persistent repository catalog entry.
type RepositoryRegistration struct {
	Repo            string   `json:"repo"`
	RemoteURL       string   `json:"remote_url"`
	BaseCandidates  []string `json:"base_candidates,omitempty"`
	Retired         bool     `json:"retired,omitempty"`
	ControlRevision int64    `json:"control_revision"`
}

// RegisterRepositoryPayload adopts a repository into the online catalog.
type RegisterRepositoryPayload struct {
	RemoteURL      string   `json:"remote_url"`
	BaseCandidates []string `json:"base_candidates,omitempty"`
	Retired        bool     `json:"retired,omitempty"`
}

// RunnerRepository reports a loaded local binding, without local paths.
type RunnerRepository struct {
	Repo           string `json:"repo"`
	RemoteIdentity string `json:"remote_identity"`
}

// RepositoryRemoteKey fingerprints an exact Git remote address. Transport
// aliases, suffixes and SCP path roots are not inferred to be equivalent.
// Fingerprints keep credentials and local remote paths out of telemetry.
func RepositoryRemoteKey(remote string) string {
	sum := sha256.Sum256([]byte(remote))
	return hex.EncodeToString(sum[:])
}
