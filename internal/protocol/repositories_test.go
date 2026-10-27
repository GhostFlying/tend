package protocol

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestRepositoryRemoteFingerprintPreservesExactIdentity(t *testing.T) {
	remote := "https://user:secret@example.com/team/repo.git"
	identity := RepositoryRemoteKey(remote)
	if len(identity) != 64 || strings.Contains(identity, "secret") {
		t.Fatal("invalid or credential-bearing fingerprint", identity)
	}
	if _, err := hex.DecodeString(identity); err != nil {
		t.Fatal(err)
	}
	if identity != RepositoryRemoteKey(remote) {
		t.Fatal("unstable fingerprint")
	}
	for _, pair := range [][2]string{
		{"/srv/repo", "/srv/repo.git"},
		{"file:///srv/repo", "file:///srv/repo.git"},
		{"git@example.com:srv/repo.git", "git@example.com:/srv/repo.git"},
		{"git@example.com:team/repo.git", "ssh://git@example.com/team/repo.git"},
		{"https://example.com/repo", "https://example.com/repo.git"},
	} {
		if RepositoryRemoteKey(pair[0]) == RepositoryRemoteKey(pair[1]) {
			t.Fatal("distinct addresses collapsed", pair)
		}
	}
}
