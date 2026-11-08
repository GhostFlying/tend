package runnerapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/release"
	"github.com/GhostFlying/tend/internal/store"
)

func TestReleaseSetDownloadRemainsBoundToBatch(t *testing.T) {
	e := newEnv(t, 0)
	root := t.TempDir()
	dir := filepath.Join(root, "upgrade-fixture")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte("binary fixture")
	sum := sha256.Sum256(body)
	metadata := release.Manifest{Format: 1, ReleaseID: "fixture", Protocol: release.Range{Min: 1, Max: 2}, Storage: release.Storage{ServerSchema: release.Range{Min: 1, Max: 999}, RunnerJournal: release.Range{Min: 1, Max: 99}}}
	set := release.Set{Format: 1, ReleaseID: metadata.ReleaseID, Commit: strings.Repeat("a", 40), OS: "linux", Arch: "amd64"}
	for _, role := range []string{"server", "runner", "updater", "cli"} {
		a := release.Artifact{Role: role, OS: "linux", Arch: "amd64", Path: role, Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:])}
		set.Artifacts = append(set.Artifacts, a)
		if role == "server" || role == "runner" {
			metadata.Artifacts = append(metadata.Artifacts, a)
		}
		if err := os.WriteFile(filepath.Join(dir, role), body, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(metadata)
	sum = sha256.Sum256(raw)
	set.ManifestSHA256 = hex.EncodeToString(sum[:])
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(set)
	if err := os.WriteFile(filepath.Join(dir, "release-set.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := release.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	row := store.UpgradeRow{View: protocol.UpgradeView{UpgradeID: "upgrade-fixture", ReleaseID: metadata.ReleaseID, Status: "running", Phase: "runners"}, Manifest: metadata, ManifestSHA256: b.ManifestSHA256, ReleaseSet: b.Set, ReleaseSetSHA256: b.SetSHA256, ReleaseSetSize: int64(len(b.SetBytes)), Hosts: []store.UpgradeHost{{HostID: "linux-1"}}}
	put := func() {
		t.Helper()
		if err := e.store.Write(t.Context(), func(tx *store.Tx) error { return tx.PutUpgrade(t.Context(), row) }); err != nil {
			t.Fatal(err)
		}
	}
	put()
	srv := httptest.NewServer(New(e.app, e.peers, secret, Config{ReleaseDir: root}, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler())
	defer srv.Close()
	read := func() *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), "GET", srv.URL+"/runner/v1/releases/upgrade-fixture/set", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+hostToken)
		req.Header.Set(HeaderHost, "linux-1")
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	res := read()
	got, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil || res.StatusCode != 200 || !bytes.Equal(got, b.SetBytes) {
		t.Fatal("set download did not preserve exact bytes", res.StatusCode, err)
	}
	row.ReleaseSetSHA256 = strings.Repeat("f", 64)
	put()
	res = read()
	res.Body.Close()
	if res.StatusCode == 200 {
		t.Fatal("batch accepted different set under same service manifest")
	}
}
